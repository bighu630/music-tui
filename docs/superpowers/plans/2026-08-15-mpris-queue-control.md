# MPRIS 队列控制（Next/Previous + LoopStatus/Shuffle）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** MPRIS 支持 Next/Previous 操作 + LoopStatus/Shuffle 属性读写，与 UI 模式显示双向同步，playerctl 实测通过。

**Architecture:** mpris 包注入 controller 接口（PlayNext/PlayPrevious/SetMode/Mode/Len，由 ui.Model 实现，经 bubbletea 消息循环执行与 UI 键位同一编排路径）；UI 模式切换经 onModeChanged 回调（SyncMode）投影同步 MPRIS 属性。queue 包并发安全化（RWMutex）支持 D-Bus goroutine 同步读。

**Tech Stack:** Go、godbus/dbus v5 + godbus/prop、bubbletea。Linux-only MPRIS（mpris_linux.go）；非 Linux 桩同步接口。

**Spec:** `docs/superpowers/specs/2026-08-15-mpris-queue-control-design.md`（commit 741a2f3）

**执行顺序：Task 1（queue）必须先完成并提交，Task 2/3 编译依赖 queue.ErrEmpty。**

---

## Task 1: queue 并发安全化 + ErrEmpty 哨兵

**Files:**
- Modify: `queue/queue.go`
- Test: `queue/queue_test.go`

- [ ] **Step 1: 写并发访问测试（-race 检测）**

在 `queue/queue_test.go` 追加：

```go
// TestConcurrentAccess 并发读+写队列不产生数据竞争（-race 下运行；
// 服务 MPRIS 从 D-Bus goroutine 同步读 Len/Mode 的前提）。
func TestConcurrentAccess(t *testing.T) {
	q := New()
	for i := 0; i < 50; i++ {
		q.Add(model.Track{ID: fmt.Sprintf("t%d", i), Title: "t"})
	}
	q.SetMode(Shuffle)
	var wg sync.WaitGroup
	// 读者：D-Bus goroutine 模拟（Mode/Len/Tracks/Current/Snapshot）
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = q.Mode()
				_ = q.Len()
				_ = q.Current()
				_ = q.CurrentIndex()
				_ = q.Tracks()
				_ = q.Snapshot()
			}
		}()
	}
	// 写者：UI goroutine 模拟
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			q.Add(model.Track{ID: "x", Title: "x"})
			q.Remove(0)
			q.Next()
			q.Prev()
			q.SetMode(Sequential)
			q.SetMode(Shuffle)
			q.JumpTo(3)
		}
	}()
	wg.Wait()
}
```

- [ ] **Step 2: 运行确认（未加锁时 -race 应报数据竞争）**

Run: `go test -race -run TestConcurrentAccess ./queue/`
Expected: `DATA RACE` 输出或偶发失败（race 检测不保证每次复现，若未复现也继续）

- [ ] **Step 3: queue.go 加锁实现**

`queue/queue.go` 修改：

```go
import (
	"errors"
	"math/rand"
	"sync"

	"music-tui/model"
)

// ErrEmpty 队列为空时 MPRIS 控制器操作的哨兵错误（mpris 映射 NotSupported）。
var ErrEmpty = errors.New("queue: empty")
```

`Queue` 结构体加锁字段：

```go
// Queue 播放队列。tracks 的下标即 UI 展示的序号（currentIdx 高亮）；
// 三种模式下 Next 播完列表均回绕到队首（循环）。
// 并发安全：UI 在 bubbletea 循环内写；MPRIS D-Bus goroutine 经 RLock 读
// （Mode/Len/Current/Tracks/Snapshot/CurrentIndex）。
type Queue struct {
	mu         sync.RWMutex
	tracks     []model.Track
	currentIdx int // 当前曲目下标；-1 = 无当前曲目
	mode       Mode
}
```

所有公开方法加锁（机械改动，行为不变）：
- `Add`/`InsertNext`/`Replace`/`ReplaceAll`/`Remove`/`Move`/`Clear`/`Next`/`Prev`/`SetMode`/`JumpTo`/`Restore` 方法体首行加 `q.mu.Lock()`，末尾 `defer q.mu.Unlock()`
- `PeekNext`/`Current`/`Tracks`/`CurrentIndex`/`Mode`/`Len` 用 `q.mu.RLock()` / `defer q.mu.RUnlock()`
- `Snapshot` 用 RLock（Tracks 拷贝须在锁内完成，现有 `append([]model.Track(nil), q.tracks...)` 保持）

注意 `Next` 内 `return q.tracks[q.currentIdx], true` 返回的是 model.Track 值拷贝，锁内读取安全。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race ./queue/`
Expected: 全部 PASS（含新并发测试与既有行为回归）

- [ ] **Step 5: Commit**

```bash
git add queue/queue.go queue/queue_test.go
git status
git commit -m "feat(queue): RWMutex 并发安全 + ErrEmpty 哨兵（MPRIS 队列控制前置）"
```

---

## Task 2: mpris 包——controller 接口 + 属性映射 + D-Bus 行为

**Files:**
- Modify: `mpris/mpris_linux.go`
- Modify: `mpris/mpris_linux_test.go`
- Modify: `mpris/mpris_unsupported.go`

前置：Task 1 已提交（依赖 `queue.ErrEmpty`）。

### Step 1-2: 映射纯函数（TDD）

- [ ] **Step 1: 写映射纯函数测试**

`mpris/mpris_linux_test.go` 追加（import 增加 `"music-tui/queue"`）：

```go
// ---- 模式 ↔ MPRIS 属性映射 ----

func TestLoopStatusFor(t *testing.T) {
	cases := []struct {
		mode queue.Mode
		want string
	}{
		{queue.Sequential, "Playlist"}, // 列表循环（播完回绕）
		{queue.RepeatOne, "Track"},     // 单曲循环
		{queue.Shuffle, "Playlist"},    // 随机播完也回绕，语义即列表循环
	}
	for _, c := range cases {
		if got := loopStatusFor(c.mode); got != c.want {
			t.Errorf("loopStatusFor(%v) = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestShuffleFor(t *testing.T) {
	if !shuffleFor(queue.Shuffle) {
		t.Error("Shuffle 模式应映射 Shuffle=true")
	}
	for _, m := range []queue.Mode{queue.Sequential, queue.RepeatOne} {
		if shuffleFor(m) {
			t.Errorf("模式 %v 不应映射 Shuffle=true", m)
		}
	}
}

func TestModeForLoopStatus(t *testing.T) {
	cases := []struct {
		val string
		cur queue.Mode
		want queue.Mode
	}{
		{"Track", queue.Sequential, queue.RepeatOne},
		{"Track", queue.Shuffle, queue.RepeatOne},
		{"None", queue.Shuffle, queue.Sequential},        // 方案 A：None 归入列表循环
		{"None", queue.RepeatOne, queue.Sequential},
		{"Playlist", queue.RepeatOne, queue.Sequential},  // 列表循环：单曲循环切走
		{"Playlist", queue.Sequential, queue.Sequential}, // 已是列表循环：不变
		{"Playlist", queue.Shuffle, queue.Shuffle},       // 随机保持：投影已是 Playlist，写 Playlist 不应关随机
	}
	for _, c := range cases {
		if got := modeForLoopStatus(c.val, c.cur); got != c.want {
			t.Errorf("modeForLoopStatus(%q, %v) = %v, want %v", c.val, c.cur, got, c.want)
		}
	}
}

func TestModeForShuffle(t *testing.T) {
	cases := []struct {
		b    bool
		cur  queue.Mode
		want queue.Mode
	}{
		{true, queue.Sequential, queue.Shuffle},
		{true, queue.RepeatOne, queue.Shuffle},
		{false, queue.Shuffle, queue.Sequential}, // 关随机：随机模式切回列表循环
		{false, queue.RepeatOne, queue.RepeatOne}, // 关随机：不动单曲循环
		{false, queue.Sequential, queue.Sequential},
	}
	for _, c := range cases {
		if got := modeForShuffle(c.b, c.cur); got != c.want {
			t.Errorf("modeForShuffle(%v, %v) = %v, want %v", c.b, c.cur, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test -run 'TestLoopStatusFor|TestShuffleFor|TestModeFor' ./mpris/`
Expected: 编译失败（函数未定义）

- [ ] **Step 3: 实现映射纯函数**

`mpris/mpris_linux.go` import 增加 `"music-tui/queue"`，文件末尾纯函数区追加：

```go
// loopStatusFor 映射播放模式到 MPRIS LoopStatus：单曲循环=Track；
// 列表循环与随机（播完均回绕到队首，语义即列表循环）=Playlist。
func loopStatusFor(m queue.Mode) string {
	if m == queue.RepeatOne {
		return "Track"
	}
	return "Playlist"
}

// shuffleFor 映射播放模式到 MPRIS Shuffle：仅随机模式为 true。
func shuffleFor(m queue.Mode) bool { return m == queue.Shuffle }

// modeForLoopStatus 映射 MPRIS LoopStatus 写入到播放模式。
// Playlist 对 Shuffle 模式保持（随机模式下播完回绕，投影已是 Playlist，
// 写 Playlist 不应关闭随机）；None 归入 Sequential（设计决策：无第四态）。
func modeForLoopStatus(s string, cur queue.Mode) queue.Mode {
	switch s {
	case "Track":
		return queue.RepeatOne
	case "None":
		return queue.Sequential
	default: // "Playlist"
		if cur == queue.Shuffle {
			return cur
		}
		return queue.Sequential
	}
}

// modeForShuffle 映射 MPRIS Shuffle 写入到播放模式：true → 随机模式；
// false 仅当当前是随机模式时切回列表循环（关闭随机不动其他循环设置）。
func modeForShuffle(b bool, cur queue.Mode) queue.Mode {
	if b {
		return queue.Shuffle
	}
	if cur == queue.Shuffle {
		return queue.Sequential
	}
	return cur
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test -run 'TestLoopStatusFor|TestShuffleFor|TestModeFor' ./mpris/`
Expected: PASS

### Step 5-8: controller 接口 + SetController/SyncMode/refreshNav（TDD）

- [ ] **Step 5: 写 fakeController 与同步测试**

`mpris/mpris_linux_test.go` 追加：

```go
// fakeController 实现 controller：记录调用并支持注入队列状态/错误。
type fakeController struct {
	mu        sync.Mutex
	mode      queue.Mode
	len       int
	nextErr   error
	prevErr   error
	nexts     int
	prevs     int
	setModes  []queue.Mode
}

func newFakeController() *fakeController { return &fakeController{} }

func (f *fakeController) PlayNext() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nexts++
	return f.nextErr
}
func (f *fakeController) PlayPrevious() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prevs++
	return f.prevErr
}
func (f *fakeController) SetMode(m queue.Mode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModes = append(f.setModes, m)
}
func (f *fakeController) Mode() queue.Mode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}
func (f *fakeController) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.len
}

// TestSetControllerSyncsProperties 注入 controller 即初始化队列相关属性。
func TestSetControllerSyncsProperties(t *testing.T) {
	s := newTestServer()
	fpr := s.props.(*fakeProps)
	fc := newFakeController()
	fc.mode = queue.RepeatOne
	fc.len = 5

	s.SetController(fc)

	if got := fpr.GetMust(ifacePlayer, "LoopStatus"); got != "Track" {
		t.Errorf("LoopStatus = %v, want Track（RepeatOne 投影）", got)
	}
	if got := fpr.GetMust(ifacePlayer, "Shuffle"); got != false {
		t.Errorf("Shuffle = %v, want false", got)
	}
	if got := fpr.GetMust(ifacePlayer, "CanGoNext"); got != true {
		t.Errorf("CanGoNext = %v, want true（Len>1）", got)
	}
	if got := fpr.GetMust(ifacePlayer, "CanGoPrevious"); got != true {
		t.Errorf("CanGoPrevious = %v, want true", got)
	}
}

// TestSyncModeUpdatesProperties UI 模式变更后投影同步。
func TestSyncModeUpdatesProperties(t *testing.T) {
	s := newTestServer()
	fpr := s.props.(*fakeProps)
	s.SetController(newFakeController())

	s.SyncMode(queue.Shuffle)

	if got := fpr.GetMust(ifacePlayer, "LoopStatus"); got != "Playlist" {
		t.Errorf("LoopStatus = %v, want Playlist（Shuffle 投影）", got)
	}
	if got := fpr.GetMust(ifacePlayer, "Shuffle"); got != true {
		t.Errorf("Shuffle = %v, want true", got)
	}
}

// TestRefreshNav 单曲/空队列不可跳转；多曲可跳转。
func TestRefreshNav(t *testing.T) {
	s := newTestServer()
	fpr := s.props.(*fakeProps)
	fc := newFakeController()

	s.SetController(fc)
	fc.mu.Lock()
	fc.len = 1
	fc.mu.Unlock()
	s.refreshNav()
	if got := fpr.GetMust(ifacePlayer, "CanGoNext"); got != false {
		t.Errorf("单曲队列 CanGoNext = %v, want false", got)
	}

	fc.mu.Lock()
	fc.len = 2
	fc.mu.Unlock()
	s.refreshNav()
	if got := fpr.GetMust(ifacePlayer, "CanGoNext"); got != true {
		t.Errorf("多曲队列 CanGoNext = %v, want true", got)
	}
}
```

- [ ] **Step 6: 运行确认失败**

Run: `go test -run 'TestSetController|TestSyncMode|TestRefreshNav' ./mpris/`
Expected: 编译失败（controller 未定义、SetController 不存在）

- [ ] **Step 7: 实现 controller 接口与同步方法**

`mpris/mpris_linux.go`：

在 `playerLike` 接口定义后追加：

```go
// controller 是 mpris 服务依赖的队列控制能力：播放编排（Next/Previous）与
// 播放模式读写。由 ui 层实现（与首页 ,/. 键、s 键同一编排路径），
// main 组装时经 SetController 注入。与 mpris_unsupported.go 中定义保持一致
// （两文件互斥编译，接口仅用于 SetController 签名匹配）。
type controller interface {
	PlayNext() error     // 播放下一首（与 , 键同一编排）；queue.ErrEmpty = 无曲可播
	PlayPrevious() error // 播放上一首（与 . 键同一编排）；queue.ErrEmpty = 无曲可播
	SetMode(queue.Mode)  // 绝对模式切换（与 s 键同一路径），恒成功（SetLoop 失败仅 toast）
	Mode() queue.Mode    // 当前播放模式（并发安全）
	Len() int            // 队列长度（并发安全）
}
```

`Server` 结构体加字段：

```go
// Server 是 MPRIS D-Bus 服务端：播放器事件推送属性，D-Bus 方法转调播放器。
type Server struct {
	p    playerLike
	ctrl controller // 队列控制注入（main 经 SetController 注入；nil = 未注入）

	conn     bus
	props    propsStore
	closeCh  chan struct{}
	pumpDone chan struct{} // pump 退出信号：Close 等它关闭后才断开连接

	closed atomic.Bool // Close 幂等保护；Close 后 pump 可能短暂存活，字段不再置 nil

	mu    sync.Mutex
	track *model.Track // 当前/最后曲目（ui 通过 SetTrack 回调写入）

	lastPos float64 // 上次 ProgressEvent 位置（秒），仅 pump goroutine 访问
}
```

`NewServer` 后追加（放在 SetTrack 前）：

```go
// SetController 注入队列控制器并初始化队列相关属性（LoopStatus/Shuffle/
// CanGoNext/CanGoPrevious 按当前模式与队列长度投影）。幂等，可重复调用；
// Start 前后均可调用（Start 前调用时属性存储未建，同步延后到 Start 后首次
// 事件/操作）。未注入（nil）时 Next/Previous 返回 NotSupported、写回调 Failed。
func (s *Server) SetController(ctrl controller) {
	s.ctrl = ctrl
	if ctrl == nil || s.props == nil {
		return
	}
	s.syncMode(ctrl.Mode())
	s.refreshNav()
}

// SyncMode 由 ui 在播放模式变更后调用：同步 LoopStatus/Shuffle 属性
// （EmitTrue → PropertiesChanged 广播）。与 controller.SetMode 配合完成
// 双向同步：D-Bus 写 → SetMode → ui 切换 → SyncMode 回写投影。
func (s *Server) SyncMode(m queue.Mode) { s.syncMode(m) }

func (s *Server) syncMode(m queue.Mode) {
	if s.props == nil {
		return
	}
	s.props.SetMust(ifacePlayer, "LoopStatus", loopStatusFor(m))
	s.props.SetMust(ifacePlayer, "Shuffle", shuffleFor(m))
}

// refreshNav 按队列长度刷新 CanGoNext/CanGoPrevious（Len>1 才可跳转；
// 单曲/空队列均不可）。调用时机：SetController、每次播放事件后、
// 每次 Next/Previous 转调后。
func (s *Server) refreshNav() {
	if s.props == nil || s.ctrl == nil {
		return
	}
	can := s.ctrl.Len() > 1
	s.props.SetMust(ifacePlayer, "CanGoNext", can)
	s.props.SetMust(ifacePlayer, "CanGoPrevious", can)
}
```

`handleEvent` 末尾（switch 结束后）追加 `s.refreshNav()`：

```go
// handleEvent 把播放器事件映射为 MPRIS 属性更新与 Seeked 信号。
func (s *Server) handleEvent(ev player.Event) {
	switch e := ev.(type) {
	// ... 现有分支不变 ...
	case player.ErrorEvent:
		s.props.SetMust(ifacePlayer, "PlaybackStatus", "Stopped")
	}
	// 队列长度可能随播放推进变化：每次事件后刷新可跳转状态
	s.refreshNav()
}
```

- [ ] **Step 8: 运行确认通过**

Run: `go test -run 'TestSetController|TestSyncMode|TestRefreshNav' ./mpris/`
Expected: PASS

### Step 9-12: Next/Previous 转调（TDD）

- [ ] **Step 9: 写 Next/Previous 测试**

`mpris/mpris_linux_test.go` 追加：

```go
// TestNextPreviousTransfer 非空队列转调 controller 并刷新导航属性。
func TestNextPreviousTransfer(t *testing.T) {
	s := newTestServer()
	fpr := s.props.(*fakeProps)
	fc := newFakeController()
	fc.mode = queue.Sequential
	fc.len = 3
	s.SetController(fc)

	if err := s.Next(); err != nil {
		t.Fatalf("Next 转调失败: %v", err)
	}
	if err := s.Previous(); err != nil {
		t.Fatalf("Previous 转调失败: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.nexts != 1 || fc.prevs != 1 {
		t.Errorf("转调次数 nexts=%d prevs=%d, want 1/1", fc.nexts, fc.prevs)
	}
	_ = fpr // CanGoNext 已在 SetController 时置 true
}

// TestNextEmptyQueueNotSupported 空队列（ErrEmpty）→ NotSupported。
func TestNextEmptyQueueNotSupported(t *testing.T) {
	s := newTestServer()
	fc := newFakeController()
	fc.nextErr = queue.ErrEmpty
	s.SetController(fc)

	err := s.Next()
	if err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
		t.Fatalf("空队列 Next 应返回 NotSupported, got %v", err)
	}
}

// TestNextPreviousNilController 未注入 controller → NotSupported。
func TestNextPreviousNilController(t *testing.T) {
	s := newTestServer()
	if err := s.Next(); err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
		t.Fatalf("未注入 controller 时 Next 应返回 NotSupported, got %v", err)
	}
	if err := s.Previous(); err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
		t.Fatalf("未注入 controller 时 Previous 应返回 NotSupported, got %v", err)
	}
}
```

- [ ] **Step 10: 运行确认失败**

Run: `go test -run 'TestNext' ./mpris/`
Expected: FAIL（Next 仍返回 NotSupported）

- [ ] **Step 11: 实现 Next/Previous handler**

`mpris/mpris_linux.go` 替换：

```go
// Next 转调队列控制器播放下一首（与首页 , 键同一编排路径）；
// 空队列返回 NotSupported。
func (h *playerHandler) Next() *dbus.Error {
	if h.s.ctrl == nil {
		return notSupported()
	}
	if err := h.s.ctrl.PlayNext(); err != nil {
		if errors.Is(err, queue.ErrEmpty) {
			return notSupported()
		}
		return dbus.MakeFailedError(err)
	}
	h.s.refreshNav()
	return nil
}

// Previous 转调队列控制器播放上一首（与首页 . 键同一编排路径）；
// 空队列返回 NotSupported。
func (h *playerHandler) Previous() *dbus.Error {
	if h.s.ctrl == nil {
		return notSupported()
	}
	if err := h.s.ctrl.PlayPrevious(); err != nil {
		if errors.Is(err, queue.ErrEmpty) {
			return notSupported()
		}
		return dbus.MakeFailedError(err)
	}
	h.s.refreshNav()
	return nil
}
```

（`errors` 已 import；新增 `"music-tui/queue"` import。）

- [ ] **Step 12: 运行确认通过**

Run: `go test -run 'TestNext|TestPrevious' ./mpris/`
Expected: PASS

### Step 13-16: LoopStatus/Shuffle 写回调（TDD）

- [ ] **Step 13: 写回调测试**

`mpris/mpris_linux_test.go` 追加：

```go
// TestLoopStatusCallback 合法值转调 SetMode（含保持逻辑），非法值 InvalidArgs。
func TestLoopStatusCallback(t *testing.T) {
	s := newTestServer()
	fc := newFakeController()
	fc.mode = queue.Sequential
	s.SetController(fc)

	// Track → RepeatOne
	if err := s.loopStatusCallback(&prop.Change{Value: "Track"}); err != nil {
		t.Fatalf("写 LoopStatus=Track 被拒: %v", err)
	}
	// None → Sequential
	if err := s.loopStatusCallback(&prop.Change{Value: "None"}); err != nil {
		t.Fatalf("写 LoopStatus=None 被拒: %v", err)
	}
	// Playlist + 当前 Shuffle → 保持 Shuffle
	fc.mu.Lock()
	fc.mode = queue.Shuffle
	fc.mu.Unlock()
	if err := s.loopStatusCallback(&prop.Change{Value: "Playlist"}); err != nil {
		t.Fatalf("写 LoopStatus=Playlist 被拒: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.setModes) != 3 || fc.setModes[0] != queue.RepeatOne || fc.setModes[1] != queue.Sequential || fc.setModes[2] != queue.Shuffle {
		t.Errorf("setModes = %v, want [RepeatOne Sequential Shuffle]", fc.setModes)
	}

	// 非法值 → InvalidArgs 且不转调
	fc.setModes = nil
	if err := s.loopStatusCallback(&prop.Change{Value: "Loop"}); err == nil {
		t.Fatal("非法 LoopStatus 应报错")
	}
	if err := s.loopStatusCallback(&prop.Change{Value: 42}); err == nil {
		t.Fatal("非字符串 LoopStatus 应报错")
	}
	if len(fc.setModes) != 0 {
		t.Error("非法值不应转调 SetMode")
	}
}

// TestLoopStatusCallbackNilController 未注入 controller → Failed。
func TestLoopStatusCallbackNilController(t *testing.T) {
	s := newTestServer()
	if err := s.loopStatusCallback(&prop.Change{Value: "Track"}); err == nil {
		t.Fatal("未注入 controller 时应报错")
	}
}

// TestShuffleCallback 写 Shuffle 转调 SetMode（含保持逻辑）。
func TestShuffleCallback(t *testing.T) {
	s := newTestServer()
	fc := newFakeController()
	fc.mode = queue.Sequential
	s.SetController(fc)

	// true → Shuffle
	if err := s.shuffleCallback(&prop.Change{Value: true}); err != nil {
		t.Fatalf("写 Shuffle=true 被拒: %v", err)
	}
	// false + 当前 Shuffle → Sequential
	fc.mu.Lock()
	fc.mode = queue.Shuffle
	fc.mu.Unlock()
	if err := s.shuffleCallback(&prop.Change{Value: false}); err != nil {
		t.Fatalf("写 Shuffle=false 被拒: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.setModes) != 2 || fc.setModes[0] != queue.Shuffle || fc.setModes[1] != queue.Sequential {
		t.Errorf("setModes = %v, want [Shuffle Sequential]", fc.setModes)
	}

	// 非 bool → InvalidArgs 且不转调
	fc.setModes = nil
	if err := s.shuffleCallback(&prop.Change{Value: "yes"}); err == nil {
		t.Fatal("非布尔 Shuffle 应报错")
	}
	if len(fc.setModes) != 0 {
		t.Error("非法值不应转调 SetMode")
	}
}
```

- [ ] **Step 14: 运行确认失败**

Run: `go test -run 'TestLoopStatusCallback|TestShuffleCallback' ./mpris/`
Expected: 编译失败（回调方法不存在）

- [ ] **Step 15: 实现回调与 propertyMap 变更**

`mpris/mpris_linux.go`：

propertyMap 中 `ifacePlayer` 段替换：

```go
		"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
		"LoopStatus": {Value: "Playlist", Writable: true, Emit: prop.EmitTrue,
			Callback: s.loopStatusCallback},
		"Rate":         {Value: 1.0, Emit: prop.EmitConst},
		"Shuffle":      {Value: false, Writable: true, Emit: prop.EmitTrue, Callback: s.shuffleCallback},
		"Metadata":     {Value: map[string]dbus.Variant{}, Emit: prop.EmitTrue},
		"Volume": {Value: 1.0, Writable: true, Emit: prop.EmitTrue,
			Callback: s.volumeCallback},
		"Position":      {Value: int64(0), Emit: prop.EmitFalse},
		"MinimumRate":   {Value: 1.0, Emit: prop.EmitConst},
		"MaximumRate":   {Value: 1.0, Emit: prop.EmitConst},
		"CanGoNext":     {Value: false, Emit: prop.EmitTrue},
		"CanGoPrevious": {Value: false, Emit: prop.EmitTrue},
```

（LoopStatus 初始 "Playlist" = Sequential 投影；CanGoNext/CanGoPrevious 改 EmitTrue 动态。）

`volumeCallback` 后追加：

```go
// loopStatusCallback 处理客户端对 LoopStatus 的写入：校验枚举值并转调
// 控制器切换播放模式。注意：回调在 Properties.Set 持锁期间执行，不得
// 再读取本服务的 props（投影回写由 ui 经 SyncMode 完成）。
func (s *Server) loopStatusCallback(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(string)
	if !ok || (v != "None" && v != "Track" && v != "Playlist") {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if s.ctrl == nil {
		return dbus.MakeFailedError(errors.New("MPRIS 控制器未注入"))
	}
	s.ctrl.SetMode(modeForLoopStatus(v, s.ctrl.Mode()))
	return nil
}

// shuffleCallback 处理客户端对 Shuffle 的写入：校验布尔并转调控制器。
// 锁内约束同 loopStatusCallback。
func (s *Server) shuffleCallback(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(bool)
	if !ok {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if s.ctrl == nil {
		return dbus.MakeFailedError(errors.New("MPRIS 控制器未注入"))
	}
	s.ctrl.SetMode(modeForShuffle(v, s.ctrl.Mode()))
	return nil
}
```

- [ ] **Step 16: 运行确认通过 + 全量回归**

Run: `go test ./mpris/`
Expected: 全部 PASS（含既有 handleEvent/Volume 测试回归）

### Step 17-18: unsupported 桩 + 提交

- [ ] **Step 17: 更新非 Linux 桩**

`mpris/mpris_unsupported.go` 完整替换：

```go
//go:build !linux

// Package mpris 在非 Linux 平台提供 no-op 桩：D-Bus 媒体控制仅 Linux 可用，
// 桩保持与 Linux 实现相同的 API，保证 main.go 无平台分支。
package mpris

import (
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// playerLike 与 mpris_linux.go 中定义保持一致（两文件互斥编译，
// 仅用于 NewServer 签名匹配；桩不使用该参数）。
type playerLike interface {
	Subscribe() (<-chan player.Event, func())
	Play(url string) error
	Pause() error
	Resume() error
	Seek(seconds float64) error
	SetVolume(percent float64) error
	Volume() (float64, error)
}

// controller 与 mpris_linux.go 中定义保持一致（两文件互斥编译，
// 仅用于 SetController 签名匹配；桩不使用该参数）。
type controller interface {
	PlayNext() error
	PlayPrevious() error
	SetMode(queue.Mode)
	Mode() queue.Mode
	Len() int
}

// Server 非 Linux 平台下的 no-op 桩。
type Server struct{}

// NewServer 创建 no-op 桩（不连接任何总线）。
func NewServer(p playerLike) *Server { return &Server{} }

// Start 非 Linux 平台直接成功（什么都不做）。
func (s *Server) Start() error { return nil }

// Close no-op。
func (s *Server) Close() error { return nil }

// SetTrack no-op。
func (s *Server) SetTrack(t *model.Track) {}

// SetController no-op（注入队列控制器）。
func (s *Server) SetController(ctrl controller) {}

// SyncMode no-op（同步播放模式投影）。
func (s *Server) SyncMode(m queue.Mode) {}
```

- [ ] **Step 18: 全量验证 + Commit**

Run: `go build ./... && go vet ./... && go test -race ./mpris/ ./queue/`
Expected: 全绿

```bash
git add mpris/mpris_linux.go mpris/mpris_linux_test.go mpris/mpris_unsupported.go
git status
git commit -m "feat(mpris): Next/Previous 转调队列 + LoopStatus/Shuffle 读写映射（controller 注入 + 双向同步）"
```

---

## Task 3: ui 桥接 + main 组装 + 设计文档修订

**Files:**
- Create: `ui/mpris.go`
- Test: `ui/mpris_test.go`
- Modify: `ui/root.go`（Model 字段、NewModel、Init、Update case、cycleMode 重构）
- Modify: `main.go`
- Modify: `docs/superpowers/specs/2026-08-13-music-tui-design.md`（13.3 节）

前置：Task 1、Task 2 已提交。

### Step 1-4: MprisController 桥（TDD）

- [ ] **Step 1: 写路由测试**

新建 `ui/mpris_test.go`：

```go
package ui

import (
	"errors"
	"testing"

	"music-tui/model"
	"music-tui/queue"
)

// 队列三首曲：t1 当前，t2/t3 后续。
func mprisTestQueue(t *testing.T, m *Model) {
	t.Helper()
	m.queue.Replace(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Add(model.Track{ID: "t2", Title: "T2", Duration: 10})
	m.queue.Add(model.Track{ID: "t3", Title: "T3", Duration: 10})
}

// TestMprisReqNextPlaysNext 请求下一首：编排路径与 nextTrackMsg 一致。
func TestMprisReqNextPlaysNext(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	mprisTestQueue(t, &m)

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqNext, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqNext 应成功: %v", err)
	}
	if cur, ok := m2.queue.Current(); !ok || cur.ID != "t2" {
		t.Errorf("当前曲 = %v/%v, want t2", cur.ID, ok)
	}
	if len(m2.failedTracks) != 0 || m2.queueSkip {
		t.Error("reqNext 应重置重试预算与解耦标记（同 nextTrackMsg）")
	}
}

// TestMprisReqPrevPlaysPrev 请求上一首：编排路径与 prevTrackMsg 一致（含回绕）。
func TestMprisReqPrevPlaysPrev(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	mprisTestQueue(t, &m)
	m.queue.JumpTo(0) // 当前 t1，队首回绕到末尾 t3

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqPrev, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqPrev 应成功: %v", err)
	}
	if cur, ok := m2.queue.Current(); !ok || cur.ID != "t3" {
		t.Errorf("当前曲 = %v/%v, want t3（队首回绕）", cur.ID, ok)
	}
}

// TestMprisReqEmptyQueueErrEmpty 空队列 → queue.ErrEmpty（MPRIS 映射 NotSupported）。
func TestMprisReqEmptyQueueErrEmpty(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqNext, reply: reply}})

	if err := <-reply; !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列应回 queue.ErrEmpty, got %v", err)
	}
	_ = m2
}

// TestMprisReqSetModeSwitchesAndNotifies SetMode 切换模式并通知 sink。
func TestMprisReqSetModeSwitchesAndNotifies(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqSetMode, mode: queue.Shuffle, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqSetMode 应成功: %v", err)
	}
	if got := m2.queue.Mode(); got != queue.Shuffle {
		t.Errorf("模式 = %v, want Shuffle", got)
	}
	if len(notified) != 1 || notified[0] != queue.Shuffle {
		t.Errorf("sink 通知 = %v, want [Shuffle]", notified)
	}
	// SetLoop 同步：切入 Shuffle（非 RepeatOne）→ SetLoop(false)
	fp := m2.player.(*fakePlayer)
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.loops) == 0 || fp.loops[len(fp.loops)-1] != false {
		t.Errorf("SetLoop 调用 = %v, want 末尾 false", fp.loops)
	}
}

// TestMprisControllerPreCheck 控制器侧空队列预检查返回 ErrEmpty（不投递）。
func TestMprisControllerPreCheck(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	c := m.mprisCtrl
	if err := c.PlayNext(); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列 PlayNext 应返回 ErrEmpty, got %v", err)
	}
	if err := c.PlayPrevious(); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列 PlayPrevious 应返回 ErrEmpty, got %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if c.Mode() != queue.Sequential {
		t.Errorf("Mode = %v, want Sequential（默认）", c.Mode())
	}
}

// TestMprisControllerDispatchRoundTrip 非空队列投递-执行-回包闭环。
func TestMprisControllerDispatchRoundTrip(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m.queue.Add(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Replace(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Add(model.Track{ID: "t2", Title: "T2", Duration: 10})

	// 模拟 bubbletea 消费循环：goroutine 读 reqs 并 Update。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for req := range m.mprisCtrl.reqs {
			m2, _ := update(m, mprisReqMsg{req: req})
			m = m2
		}
	}()

	if err := m.mprisCtrl.PlayNext(); err != nil {
		t.Fatalf("PlayNext: %v", err)
	}
	if cur, ok := m.queue.Current(); !ok || cur.ID != "t2" {
		t.Errorf("当前曲 = %v/%v, want t2", cur.ID, ok)
	}
	close(m.mprisCtrl.reqs)
	<-done
}
```

（`fakeSearchAdapter` 与 `update` helper 均在 root_test.go 已有。）

- [ ] **Step 2: 运行确认失败**

Run: `go test -run TestMpris ./ui/`
Expected: 编译失败（mprisCtrl/mprisReqMsg 未定义）

- [ ] **Step 3: 实现 ui/mpris.go**

新建 `ui/mpris.go`：

```go
package ui

import (
	"github.com/charmbracelet/bubbletea"

	"music-tui/queue"
)

// mprisReqKind 区分 MPRIS 控制器请求类型。
type mprisReqKind int

const (
	reqNext mprisReqKind = iota
	reqPrev
	reqSetMode
)

// mprisReq 一条 MPRIS 控制器请求：由 MPRIS D-Bus goroutine 经 dispatch
// 投递，bubbletea 循环消费执行后回写 reply（缓冲 1，D-Bus 侧同步等待）。
type mprisReq struct {
	kind  mprisReqKind
	mode  queue.Mode
	reply chan error
}

// mprisReqMsg 把 MPRIS 控制器请求包装为 bubbletea 消息。
type mprisReqMsg struct{ req mprisReq }

// MprisController 实现 mpris 包的 controller 接口（方法签名一致即隐式
// 满足，ui 不 import mpris；接口匹配由 main 组装处编译期保证）。
// PlayNext/PlayPrevious/SetMode 经 channel 投递到 bubbletea 消息循环，
// 与首页 ,/. 键、s 键走完全相同的编排路径（线程安全 + 行为一致）。
type MprisController struct {
	reqs chan mprisReq
	q    *queue.Queue

	// onModeChanged 模式变更通知（main 注入 mpris.Server.SyncMode）；
	// 启动期单次写入、之后仅 bubbletea goroutine 读，无需加锁。
	onModeChanged func(queue.Mode)
}

// SetModeSink 注册模式变更通知回调（main 注入 mprisSrv.SyncMode）。
func (c *MprisController) SetModeSink(fn func(queue.Mode)) { c.onModeChanged = fn }

// PlayNext 请求播放下一首；队列为空返回 queue.ErrEmpty（MPRIS 映射 NotSupported）。
func (c *MprisController) PlayNext() error {
	if c.q.Len() == 0 {
		return queue.ErrEmpty
	}
	return c.dispatch(mprisReq{kind: reqNext})
}

// PlayPrevious 请求播放上一首；队列为空返回 queue.ErrEmpty。
func (c *MprisController) PlayPrevious() error {
	if c.q.Len() == 0 {
		return queue.ErrEmpty
	}
	return c.dispatch(mprisReq{kind: reqPrev})
}

// SetMode 请求切换播放模式（恒成功：SetLoop 失败与 s 键一致仅 toast）。
func (c *MprisController) SetMode(m queue.Mode) { _ = c.dispatch(mprisReq{kind: reqSetMode, mode: m}) }

// Mode 返回当前播放模式（queue 并发安全，D-Bus goroutine 直接读）。
func (c *MprisController) Mode() queue.Mode { return c.q.Mode() }

// Len 返回队列长度（queue 并发安全）。
func (c *MprisController) Len() int { return c.q.Len() }

// dispatch 投递请求并同步等待执行结果（bubbletea 消费后回包）。
func (c *MprisController) dispatch(req mprisReq) error {
	reply := make(chan error, 1)
	req.reply = reply
	c.reqs <- req
	return <-reply
}

// subscribeMprisReqs 阻塞监听 MPRIS 控制器请求并注入 bubbletea。
// 与 waitForPlayerEvents 同模式：每条请求处理后需重新订阅（cmd 链不丢）。
func subscribeMprisReqs(ch chan mprisReq) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return mprisReqMsg{req: req}
	}
}
```

- [ ] **Step 4: root.go 接线（字段 + NewModel + Init + Update case）**

`ui/root.go` 修改：

(a) Model 结构体 `onTrack` 字段后追加：

```go
	onTrack func(*model.Track) // 外部消费者（MPRIS）感知当前曲目；nil 安全

	mprisCtrl *MprisController // MPRIS 控制器桥（NewModel 创建，main 注入 mpris 服务）
}
```

(b) `NewModel` 中 `m = m.syncQueueViews()` 之前（即续播恢复分支结束后）追加：

```go
	// MPRIS 控制器桥：必须在 queue 最终确定后创建（恢复失败分支会重建 queue）。
	// 方法签名与 mpris 包 controller 接口一致（隐式满足，编译期由 main 检查）。
	m.mprisCtrl = &MprisController{reqs: make(chan mprisReq, 16), q: m.queue}
```

(c) `Init` 追加订阅：

```go
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{waitForPlayerEvents(m.player), spinnerTick, subscribeMprisReqs(m.mprisCtrl.reqs)}
	if m.resume != nil {
		cmds = append(cmds, resumeCmd(m))
	}
	return tea.Batch(cmds...)
}
```

(d) Update 中 `case queueModeMsg:` 之后追加：

```go
	case mprisReqMsg:
		return m.handleMprisReq(msg.req)
```

(e) 追加 `handleMprisReq`（放在 cycleMode 附近）：

```go
// handleMprisReq 消费一条 MPRIS 控制器请求：与对应 UI 键位同一编排路径。
// 所有分支都必须回写 reply（D-Bus goroutine 同步等待）并重新订阅请求流
// （cmd 链不丢，同 TrackEnded 分支约束）。
func (m Model) handleMprisReq(req mprisReq) (Model, tea.Cmd) {
	switch req.kind {
	case reqNext:
		if tr, ok := m.queue.Next(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload() // 切歌后重算预加载目标（preloader 为指针，副本共享）
			req.reply <- nil
			return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
		}
		req.reply <- queue.ErrEmpty
		return m, subscribeMprisReqs(m.mprisCtrl.reqs)
	case reqPrev:
		if tr, ok := m.queue.Prev(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload()
			req.reply <- nil
			return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
		}
		req.reply <- queue.ErrEmpty
		return m, subscribeMprisReqs(m.mprisCtrl.reqs)
	case reqSetMode:
		m2, cmd := m.applyMode(req.mode)
		req.reply <- nil
		return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
	}
	req.reply <- nil
	return m, subscribeMprisReqs(m.mprisCtrl.reqs)
}
```

- [ ] **Step 5: 运行确认通过**

Run: `go test -run TestMpris ./ui/`
Expected: PASS

### Step 6-9: applyMode 重构 + 模式通知（TDD）

- [ ] **Step 6: 写 applyMode/notify 测试**

`ui/mpris_test.go` 追加：

```go
// TestApplyModeNotifiesSink 绝对模式切换（MPRIS SetMode 路径）通知 sink 且
// SetLoop 与模式同步（RepeatOne → true，其余 → false）。
func TestApplyModeNotifiesSink(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), newFakeSearchAdapter(), nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	m2, _ := m.applyMode(queue.RepeatOne)
	if got := m2.queue.Mode(); got != queue.RepeatOne {
		t.Errorf("模式 = %v, want RepeatOne", got)
	}
	if len(notified) != 1 || notified[0] != queue.RepeatOne {
		t.Errorf("sink 通知 = %v, want [RepeatOne]", notified)
	}
	fp := m2.player.(*fakePlayer)
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.loops) == 0 || fp.loops[len(fp.loops)-1] != true {
		t.Errorf("SetLoop 调用 = %v, want 末尾 true", fp.loops)
	}

	// 同模式切换为 no-op：不重复通知（SetMode 同模式直接返回）
	notified = nil
	m2, _ = m2.applyMode(queue.RepeatOne)
	if len(notified) != 0 {
		t.Errorf("同模式切换不应通知, got %v", notified)
	}
}

// TestCycleModeStillNotifiesSink UI 三态循环切换也通知 sink（回归：toggleModeMsg 路径）。
func TestCycleModeStillNotifiesSink(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), newFakeSearchAdapter(), nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	m2, _ := update(m, toggleModeMsg{}) // Sequential → Shuffle
	if got := m2.queue.Mode(); got != queue.Shuffle {
		t.Errorf("模式 = %v, want Shuffle", got)
	}
	if len(notified) != 1 || notified[0] != queue.Shuffle {
		t.Errorf("sink 通知 = %v, want [Shuffle]", notified)
	}
}
```

- [ ] **Step 7: 运行确认失败**

Run: `go test -run 'TestApplyMode|TestCycleModeStill' ./ui/`
Expected: 编译失败（applyMode 未定义）

- [ ] **Step 8: 重构 cycleMode → applyMode**

`ui/root.go` 替换整个 cycleMode 函数：

```go
// applyMode 绝对模式切换（MPRIS 写 LoopStatus/Shuffle 与 UI 三态循环共用）：
// SetMode + 同步 mpv 单曲循环 + 重算预加载 + 重建队列视图 + 通知 MPRIS 同步。
// SetLoop 失败仅 toast 不阻断（模式已切换，与 s 键原行为一致）。
func (m Model) applyMode(mode queue.Mode) (Model, tea.Cmd) {
	m.queue.SetMode(mode)
	// 模式影响预加载门控（RepeatOne 跳过预载）：切换后立即重算目标
	m.refreshPreload()
	m.notifyModeChanged()
	if err := m.player.SetLoop(mode == queue.RepeatOne); err != nil {
		m, cmd := m.showToast("设置循环失败: "+err.Error(), toastError)
		return m.syncQueueViews(), cmd
	}
	return m.syncQueueViews(), nil
}

// cycleMode 三态循环切换播放模式：Sequential→Shuffle→RepeatOne→Sequential。
// 首页模式按钮（toggleModeMsg）与队列页 s 键（queueModeMsg）共用。
func (m Model) cycleMode() (Model, tea.Cmd) {
	var next queue.Mode
	switch m.queue.Mode() {
	case queue.Sequential:
		next = queue.Shuffle
	case queue.Shuffle:
		next = queue.RepeatOne
	default:
		next = queue.Sequential
	}
	return m.applyMode(next)
}

// notifyModeChanged 通知外部消费者（MPRIS）播放模式已变更；nil 安全。
// 注意：同模式 SetMode 为 no-op 不触发（queue.SetMode 同模式直接返回）。
func (m Model) notifyModeChanged() {
	if m.mprisCtrl != nil && m.mprisCtrl.onModeChanged != nil {
		m.mprisCtrl.onModeChanged(m.queue.Mode())
	}
}
```

- [ ] **Step 9: 运行确认通过 + UI 全量回归**

Run: `go test ./ui/`
Expected: 全部 PASS（含既有 toggleModeMsg/queueModeMsg/nextTrackMsg/prevTrackMsg 回归）

### Step 10-11: main 组装 + 文档修订

- [ ] **Step 10: main.go 组装**

`main.go` 中 `model := ui.NewModel(...)` 之后、`p := tea.NewProgram(model)` 之前插入：

```go
	// MPRIS 队列控制注入：ui 侧桥实现 mpris 包的 controller 接口（编译期检查）；
	// 模式变更经 sink 同步回 MPRIS 属性（LoopStatus/Shuffle 投影 + PropertiesChanged）。
	mprisSrv.SetController(model.MprisController())
	model.MprisController().SetModeSink(mprisSrv.SyncMode)
```

注意 `model` 是值类型，`MprisController()` 返回内部指针（NewModel 创建的 `m.mprisCtrl`），`SetModeSink` 修改指针目标、值拷贝共享，`tea.NewProgram(model)` 使用原值即可（mprisCtrl 指针字段已复制）。

- [ ] **Step 11: 修订设计文档 13.3 节**

`docs/superpowers/specs/2026-08-13-music-tui-design.md` 13.3 节两处修改：

(a) "D-Bus → 播放器（方法转调 Player 接口）" 段中：

```diff
- - Next / Previous：CanGoNext=CanGoPrevious=false，方法返回 NotSupported（第一版无播放队列）
+ - Next / Previous：转调队列控制器播放上一首/下一首（与首页 ,/. 键同一编排路径）；
+   空队列返回 NotSupported
```

(b) "属性清单：" 段中：

```diff
- - PlaybackStatus / Position / Metadata / Rate（固定 1.0，Min=Max=1.0）/ LoopStatus（固定 None）/ Shuffle（false）/ Volume（读写，映射 mpv volume）
- - CanControl / CanPlay / CanPause / CanSeek = true；CanGoNext / CanGoPrevious / CanQuit / CanRaise / HasTrackList = false
+ - PlaybackStatus / Position / Metadata / Rate（固定 1.0，Min=Max=1.0）/ LoopStatus（读写，映射播放模式：Sequential→Playlist、RepeatOne→Track、Shuffle→Playlist；写 None 归入 Sequential）/ Shuffle（读写，映射随机模式开关）/ Volume（读写，映射 mpv volume）
+ - CanControl / CanPlay / CanPause / CanSeek = true；CanGoNext / CanGoPrevious 动态（队列长度 >1 为 true，EmitTrue）；CanQuit / CanRaise / HasTrackList = false
```

并在 "D-Bus → 播放器" 段末尾补充双向同步说明：

```diff
 - 错误统一返回 *dbus.Error
+
+队列控制（13.6 之后版本追加，详见 2026-08-15-mpris-queue-control-design.md）：
+- Next/Previous/LoopStatus/Shuffle 经注入的 controller 接口转调 ui（bubbletea 消息循环，
+  与 ,/. 键、s 键同一编排路径）；ui 模式变更经 SyncMode 回调投影同步 LoopStatus/Shuffle
+  （EmitTrue 广播 PropertiesChanged），双向同步闭环
```

### Step 12: 全量验证 + Commit

- [ ] **Step 12: 全量验证**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: 全绿（main_test.go 的既有集成测试保持通过）

```bash
git add ui/mpris.go ui/mpris_test.go ui/root.go main.go docs/superpowers/specs/2026-08-13-music-tui-design.md
git status
git commit -m "feat(ui): MPRIS 控制器桥（bubbletea 消息路由）+ applyMode 重构 + main 组装 + 文档 13 章修订"
```

---

## 最终验收（集成者执行）

Run: `go build ./... && go vet ./... && go test -race ./...`
手工验收清单（交付用户实测）：
- `playerctl next` / `playerctl previous`：切歌生效（含回绕）
- `playerctl loop track|playlist|none`：模式切换 + UI 显示同步
- `playerctl shuffle on|off`：切换 + UI 同步
- UI s 键/模式按钮切换后 `playerctl loop-status` / `playerctl shuffle` 读到同步值
- 空队列 `playerctl next` 报错；`playerctl --no-mpris ...` 查询 CanGoNext=false

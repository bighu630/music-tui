package preload

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"music-tui/cache"
	"music-tui/model"
)

// testTrack 构造测试曲目（ID 即身份，CacheAsync 去重按 ID）。
func testTrack(id string) model.Track {
	return model.Track{
		ID:     id,
		Title:  "曲 " + id,
		Artist: "歌手",
		URL:    "https://youtu.be/" + id,
		Source: "youtube",
	}
}

// fakeCache 是 CacheClient 的测试替身：记录调用顺序，每次调用返回一个
// 由测试手动 close 的完成 channel（模拟下载"何时结束"完全可控）；
// returnNil 时返回 nil（模拟真实 cache 的 no-op：已缓存/禁用/同 ID 在途）。
type fakeCache struct {
	mu        sync.Mutex
	calls     []model.Track // 调用顺序（断言串行）
	lastDone  chan struct{}
	returnNil bool
}

func (f *fakeCache) CacheAsync(track model.Track) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, track)
	if f.returnNil {
		return nil
	}
	f.lastDone = make(chan struct{})
	return f.lastDone
}

// callIDs 返回已调用曲目 ID 列表（保持调用顺序）。
func (f *fakeCache) callIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, t := range f.calls {
		out[i] = t.ID
	}
	return out
}

// callCount 返回 CacheAsync 调用次数。
func (f *fakeCache) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// closeLast 关闭最近一次调用返回的完成 channel（模拟下载彻底结束）；
// 无在途调用/已关闭时返回 false。
func (f *fakeCache) closeLast() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastDone == nil {
		return false
	}
	select {
	case <-f.lastDone:
		return false // 已关闭
	default:
		close(f.lastDone)
		return true
	}
}

// waitFor 轮询 cond（5s 超时 fatal）：worker 异步消费目标，调用何时发生
// 不可精确预测，测试只能轮询（同仓库既有测试模式）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("条件未在超时内满足")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- 基本行为 ----

func TestNewTargetNilAndSetTargetNilNoDownload(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	if s.Target() != nil {
		t.Errorf("New 后 Target() = %v, want nil", s.Target())
	}
	s.SetTarget(nil) // nil = 停止/清空目标：不应触发任何下载
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 0 {
		t.Errorf("SetTarget(nil) 后调用次数 = %d, want 0", got)
	}
	if s.Target() != nil {
		t.Errorf("SetTarget(nil) 后 Target() = %v, want nil", s.Target())
	}
}

// SetTarget(t1) → 恰好一次 CacheAsync(t1)：worker 启动下载后即使完成信号
// 关闭也不应重复调用（同一目标不重复预下载——真实 cache 对已缓存条目
// 返回 nil，重复调用是纯浪费）。
func TestSetTargetTriggersExactlyOnce(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	if got := fc.callIDs(); !reflect.DeepEqual(got, []string{"t1"}) {
		t.Fatalf("调用 = %v, want [t1]", got)
	}
	// 完成信号关闭（下载结束）后：目标未变 → 不重复调用
	if !fc.closeLast() {
		t.Fatal("前置：应存在在途完成信号")
	}
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 1 {
		t.Errorf("下载结束后调用次数 = %d, want 1（同一目标不重复调用）", got)
	}
	// 同 ID 新指针重设（无 SetTarget(nil) 间隔）：仍不重复调用——去重按 ID，
	// 换指针不改变“已处理”状态（与“失败静默、不自动重试”同一语义）。
	t1b := testTrack("t1")
	s.SetTarget(&t1b)
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 1 {
		t.Errorf("同 ID 重设（无 nil 间隔）后调用次数 = %d, want 1（不重试）", got)
	}
}

// SetTarget(nil) 重置去重状态：完成下载后清空目标、再重设同 ID 曲目
// （新指针），会重新触发下载——失败后可重试的语义（先清空再重设 = 显式重试）。
func TestSetTargetNilResetsDedupSameIDRetriggers(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	if !fc.closeLast() {
		t.Fatal("前置：应存在在途完成信号")
	}
	// 完成 → 清空 → 重设同 ID 新指针：应恰好再触发一次下载
	s.SetTarget(nil)
	t1b := testTrack("t1") // 同 ID、新指针（UI 层每次传 &next 的形态）
	s.SetTarget(&t1b)
	waitFor(t, func() bool { return fc.callCount() == 2 })
	if got := fc.callIDs(); !reflect.DeepEqual(got, []string{"t1", "t1"}) {
		t.Fatalf("调用 = %v, want [t1 t1]（清空后同 ID 应重新触发）", got)
	}
	// 第二次下载完成后：不再重复调用（恰好再调用一次）
	if !fc.closeLast() {
		t.Fatal("前置：第二次调用应存在在途完成信号")
	}
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 2 {
		t.Errorf("第二次下载结束后调用次数 = %d, want 2", got)
	}
}

// 在途（done 未关）时 SetTarget 同 ID 目标（换新指针）：worker 正串行等待中，
// 不重复调用——去重按 ID，指针换新不影响“在途已处理”的判定。
func TestSetTargetSameIDWhileInFlightNoDup(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	// done 未关闭（在途）时再次 SetTarget 同 ID 曲目（新指针，UI 层形态）
	t1b := testTrack("t1")
	s.SetTarget(&t1b)
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 1 {
		t.Errorf("在途重复 SetTarget 后调用次数 = %d, want 1", got)
	}
	fc.closeLast() // 收尾：让 worker 回到空闲
}

// 在途时 SetTarget(t2)：串行——t2 必须等 t1 的完成信号关闭后才被调用。
func TestSetTargetNewWhileInFlightSerial(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	t2 := testTrack("t2")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	// 换目标：只更新槽位，不打断在途下载
	s.SetTarget(&t2)
	time.Sleep(50 * time.Millisecond)
	if got := fc.callIDs(); !reflect.DeepEqual(got, []string{"t1"}) {
		t.Fatalf("在途期间调用 = %v, want [t1]（t2 必须串行等待）", got)
	}
	if s.Target() != &t2 {
		t.Errorf("Target() 应立即反映新目标")
	}
	// t1 完成后：自动处理最新目标 t2
	fc.closeLast()
	waitFor(t, func() bool { return fc.callCount() == 2 })
	if got := fc.callIDs(); !reflect.DeepEqual(got, []string{"t1", "t2"}) {
		t.Errorf("调用顺序 = %v, want [t1 t2]（串行单在途）", got)
	}
}

// 在途时 SetTarget(nil)：清空目标——t1 完成后不再调用任何下载。
func TestSetTargetNilWhileInFlightStopsAfterCurrent(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	s.SetTarget(nil) // 停止/清空目标
	fc.closeLast()   // t1 下载结束
	time.Sleep(50 * time.Millisecond)
	if got := fc.callCount(); got != 1 {
		t.Errorf("清空目标后调用次数 = %d, want 1（t1 完成后不再下载）", got)
	}
	if s.Target() != nil {
		t.Errorf("Target() = %v, want nil", s.Target())
	}
}

// CacheAsync 返回 nil（真实 cache 的 no-op：已缓存/禁用/同 ID 在途）：
// 调度器不等待（不会有完成信号），立即处理新目标，不死等。
func TestCacheAsyncNilReturnsProcessesNewTargetImmediately(t *testing.T) {
	fc := &fakeCache{returnNil: true}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	t2 := testTrack("t2")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	// 新目标一到立即处理（无需任何完成信号）
	s.SetTarget(&t2)
	waitFor(t, func() bool { return fc.callCount() == 2 })
	if got := fc.callIDs(); !reflect.DeepEqual(got, []string{"t1", "t2"}) {
		t.Errorf("调用 = %v, want [t1 t2]", got)
	}
}

// Stop：空闲（worker 阻塞等 wake）时快速退出。
func TestStopWhileIdle(t *testing.T) {
	s := New(&fakeCache{})
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("空闲时 Stop 未快速返回（worker 未退出）")
	}
	s.Stop() // 幂等：多次调用安全
}

// Stop：在途（下载未完成）时快速退出，不等待下载。
func TestStopWhileInFlight(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop() // 兜底：显式 Stop 前的任何失败（如 waitFor fatal）也能退出 worker
	t1 := testTrack("t1")
	s.SetTarget(&t1)
	waitFor(t, func() bool { return fc.callCount() == 1 })
	// done 未关闭 = 下载在途：Stop 不应等待它
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("在途时 Stop 未快速返回（不应等待下载完成）")
	}
	if got := fc.callCount(); got != 1 {
		t.Errorf("Stop 后调用次数 = %d, want 1", got)
	}
	fc.closeLast() // 模拟 cache 层下载自行结束：worker 已退出，无副作用
}

// Target() 反映最新目标（每次 SetTarget 后立即可见）。
func TestTargetReflectsLatest(t *testing.T) {
	fc := &fakeCache{}
	s := New(fc)
	defer s.Stop()
	t1 := testTrack("t1")
	t2 := testTrack("t2")
	s.SetTarget(&t1)
	if s.Target() != &t1 {
		t.Errorf("Target() 应指向 t1")
	}
	s.SetTarget(&t2)
	if s.Target() != &t2 {
		t.Errorf("Target() 应指向最新 t2")
	}
	s.SetTarget(nil)
	if s.Target() != nil {
		t.Errorf("Target() 应反映清空")
	}
}

// cache 为 nil（未配置缓存）：SetTarget/New/Stop 全部安全 no-op，不 panic。
func TestNilCacheClientSafe(t *testing.T) {
	s := New(nil)
	t1 := testTrack("t1")
	s.SetTarget(&t1) // 不应 panic，也不应设置目标（无 cache 可消费）
	if s.Target() != nil {
		t.Errorf("cache nil 时 SetTarget 应 no-op: Target() = %v, want nil", s.Target())
	}
	s.SetTarget(nil)
	done := make(chan struct{})
	go func() {
		s.Stop() // 不应 panic/死锁
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cache nil 时 Stop 未返回")
	}
}

// ---- 集成测试：真实 cache.Manager + 假 yt-dlp 脚本 ----
// 以下 helper 与 cache 包测试同款（那些 helper 未导出，此处自建一份）：
// 假脚本解析 -o 模板得到输出路径（%(ext)s → webm，与真实 yt-dlp 同语义），
// 产出合法音频（EBML 魔数 + 2048 字节，满足 MinAudioSize 内容校验），
// 下载→注册全链路真实走通。

func fakeYtDlpBody(extra string) string {
	return `
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$(printf '%s' "$a" | sed 's/%(ext)s/webm/')
  fi
  prev="$a"
done
[ -n "$out" ] || exit 9
` + extra
}

// fakeAudioOut 是假 yt-dlp 脚本的合法音频产物写法：EBML/WebM 魔数（八进制
// 转义，POSIX sh 兼容）+ 零填充到 2048 字节（≥ MinAudioSize）。
const fakeAudioOut = `printf '\032\105\337\243' > "$out"
head -c 2044 /dev/zero >> "$out"`

func writeFakeYtDlp(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func validAudioBytes() []byte {
	return append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 2044)...)
}

// 端到端：SetTarget → 调度器调 CacheAsync → 真实下载（假脚本落盘）→
// 注册进索引 → 完成信号关闭。轮询 Lookup 命中（超时 5s）后断言
// 文件落盘（正确文件名 + 合法音频内容）与索引注册。
func TestSchedulerIntegrationWithRealCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无法执行 POSIX sh 假 yt-dlp 脚本，集成下载链路不可测")
	}
	dir := t.TempDir()
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: dir},
		writeFakeYtDlp(t, fakeYtDlpBody(fakeAudioOut)), "", "", nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	s := New(cm)
	defer s.Stop()

	track := testTrack("preload-int-1")
	s.SetTarget(&track)

	deadline := time.Now().Add(5 * time.Second)
	var path string
	for {
		var ok bool
		if path, ok = cm.Lookup(track.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("预下载未在超时内完成（Lookup 未命中）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 断言文件落盘 + 索引注册：假脚本把 %(ext)s 替换为 webm
	if want := filepath.Join(dir, cache.SafeName(track.ID)+".webm"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读缓存文件: %v", err)
	}
	if string(data) != string(validAudioBytes()) {
		t.Errorf("缓存内容 = %q, want 合法音频（EBML 魔数 + 2048 字节）", data)
	}
	// 调度器空闲（目标已处理，不重复调用）：等待期间不产生额外下载
	if _, ok := cm.Lookup(track.ID); !ok {
		t.Error("Lookup 二次断言: 应仍命中")
	}
}

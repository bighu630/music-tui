# "添加到下一首"实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `a` 键"添加到"选择器支持把歌曲插入到当前曲之后（下一首播放）。

**Architecture:** queue 包新增 `InsertNext`（纯逻辑：currentIdx+1 处插入，无当前曲插队首）；"添加到"选择器首项新增"▶ 下一首播放"，第二项为现有"▶ 当前播放队列"（追加队尾）；root 新增 `trackInsertNextMsg` 路由。

**Tech Stack:** Go + bubbletea（list.Model 选择器）；TDD（先写失败测试再实现）。

**设计文档:** `docs/superpowers/specs/2026-08-15-insert-next-design.md`（用户已确认：方案 B、无当前曲插队首、随机模式不重洗牌、无成功 toast、提示文案不变）

---

## 文件结构

| 文件 | 责任 |
|---|---|
| `queue/queue.go` | 新增 `InsertNext` API |
| `queue/queue_test.go` | InsertNext 单测 |
| `ui/root.go` | `trackInsertNextMsg` + `emitTrackInsertNext` + 路由 |
| `ui/playlists.go` | picker 新增 `pickerQueueNextItem` 首项、Enter 分发 |
| `ui/playlists_ui_test.go` | picker 条目/顺序/Enter 分发断言更新 |
| `ui/queue_test.go` | `openPickerAndAppendQueue` helper、TestHistoryAppend 更新 |
| `ui/root_test.go` | `trackInsertNextMsg` 路由测试 |

**任务依赖：Task 2 依赖 Task 1 的 `InsertNext`（编译/运行），必须顺序执行。**

---

### Task 1: queue 包 InsertNext

**Files:**
- Modify: `queue/queue.go`（`Add` 之后新增）
- Test: `queue/queue_test.go`（`TestRemoveInvalidIndexIgnored` 之后追加测试组）

- [ ] **Step 1: 写失败测试**（`queue/queue_test.go` 末尾追加，复用现有 `testTrack`/`ids`/`eq` helper）

```go
// ---- InsertNext ----

func TestInsertNextAfterCurrent(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(1) // 当前 t2
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"t1", "t2", "tx", "t3"}) {
		t.Fatalf("Tracks = %v, want [t1 t2 tx t3]", got)
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "t2" {
		t.Errorf("Current = %s, want t2", cur.ID)
	}
	if next, ok := q.Next(); !ok || next.ID != "tx" {
		t.Errorf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}

func TestInsertNextWithoutCurrentInsertsAtHead(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	// currentIdx 仍为 -1（Add 不改变当前曲）
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"tx", "t1", "t2", "t3"}) {
		t.Fatalf("Tracks = %v, want [tx t1 t2 t3]", got)
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("插入不应改变当前曲目")
	}
}

func TestInsertNextEmptyQueue(t *testing.T) {
	q := New()
	q.InsertNext(testTrack("t1"))
	if q.Len() != 1 || q.CurrentIndex() != -1 {
		t.Errorf("Len/CurrentIndex = %d/%d, want 1/-1", q.Len(), q.CurrentIndex())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"t1"}) {
		t.Fatalf("Tracks = %v, want [t1]", got)
	}
	if _, ok := q.Current(); ok {
		t.Error("空队列插入不应有当前曲目")
	}
}

func TestInsertNextKeepsPositionInShuffle(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(0)
	q.SetMode(Shuffle) // 洗牌 t1 之后
	q.InsertNext(testTrack("tx"))
	// InsertNext 不重洗牌：插入位即实际下一首
	next, ok := q.Next()
	if !ok || next.ID != "tx" {
		t.Fatalf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}

func TestInsertNextAfterRemoveCurrent(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(0) // 当前 t1
	q.Remove(0) // 顺延：当前变 t2（index 0）
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"t2", "tx", "t3"}) {
		t.Fatalf("Tracks = %v, want [t2 tx t3]", got)
	}
	if next, ok := q.Next(); !ok || next.ID != "tx" {
		t.Errorf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./queue/ -run 'TestInsertNext' -v`
Expected: FAIL（`q.InsertNext undefined`）

- [ ] **Step 3: 实现**（`queue/queue.go`，放在 `Add` 之后）

```go
// InsertNext 插入到当前曲目之后（下一首播放）。不改变当前曲目、不自动开播；
// 无当前曲目（currentIdx=-1，如从未播放/清空后）时插入到队首（index 0）。
// 随机模式不重洗牌：插入位即实际下一首（"下一首播放"语义优先）。
func (q *Queue) InsertNext(t model.Track) {
	pos := 0
	if q.currentIdx >= 0 {
		pos = q.currentIdx + 1
	}
	q.tracks = append(q.tracks, model.Track{})
	copy(q.tracks[pos+1:], q.tracks[pos:])
	q.tracks[pos] = t
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./queue/ -run 'TestInsertNext' -v`
Expected: PASS（5 个用例）

- [ ] **Step 5: 全量验证 + 提交**

```bash
go build ./... && go vet ./... && go test ./... 2>&1 | tail -20
git add queue/queue.go queue/queue_test.go
git status   # 确认只有这两个文件
git commit -m "feat(queue): InsertNext 插入当前曲之后（无当前曲插队首）"
```

---

### Task 2: UI 层——选择器首项 + root 路由

**前置条件：Task 1 已完成（`queue.InsertNext` 已存在）。**

**Files:**
- Modify: `ui/root.go`（`trackAppendMsg` 定义附近 + `case trackAppendMsg` 附近 + `emitTrackAppend` 附近）
- Modify: `ui/playlists.go`（picker 区）
- Modify: `ui/playlists_ui_test.go`、`ui/queue_test.go`、`ui/root_test.go`

- [ ] **Step 1: 先写失败测试**

1a. `ui/root_test.go` 末尾追加（`ids` 内联实现；确认 `trackSelectedMsg` 与 `trackAppendMsg` 现有用法一致——参考已有测试 `update(m, trackSelectedMsg{track: testTrack("t1")})`）：

```go
// trackInsertNextMsg 插入到当前曲之后；无当前曲时插队首；不打断播放。
func TestTrackInsertNextMsg(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, trackSelectedMsg{track: testTrack("t1")}) // 播放 t1（替换语义）
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})   // 队尾
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})   // 队列 [t1 t2 t3]
	m, _ = update(m, trackInsertNextMsg{track: testTrack("tx")})
	got := m.queue.Tracks()
	if len(got) != 4 || got[0].ID != "t1" || got[1].ID != "tx" || got[2].ID != "t2" || got[3].ID != "t3" {
		t.Fatalf("Tracks = %+v, want [t1 tx t2 t3]", idsOf(got))
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 {
		t.Errorf("插入不应触发新播放, playCount = %d", fp.playCount())
	}
}

// idsOf 提取曲目 ID 列表（测试辅助，可放 root_test.go 顶部 helper 区）。
func idsOf(ts []model.Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
```

1b. `ui/playlists_ui_test.go` 更新 4 处现有断言 + 1 处新断言：

- **TestPlaylistDetailRemoveAndAppend**（约 L203）："a 弹选择器 → Enter 默认队列项（追加 t1）"段改为断言默认项 Enter 发 `trackInsertNextMsg`：

```go
	// a 弹选择器 → Enter 默认项"下一首播放"（插入 t1；空队列无当前曲 → 队首）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var tin trackInsertNextMsg
	for _, msg := range execCmds(cmd) {
		if im, ok := msg.(trackInsertNextMsg); ok {
			tin = im
		}
	}
	if tin.track.ID != "t1" {
		t.Fatalf("trackInsertNextMsg.track = %s, want t1", tin.track.ID)
	}
	m, _ = update(m, tin)
	if m.plPicker != nil {
		t.Fatal("插入后选择器应关闭")
	}
	if m.queue.Len() != 1 || fp.playCount() != 0 {
		t.Errorf("a 插入应只入队不播放: Len=%d playCount=%d", m.queue.Len(), fp.playCount())
	}
```

- **TestPickerAddToPlaylistFromSearch**（约 L377）：默认选中项断言改为 `pickerQueueNextItem`，且下移到"收藏"需要 **2 次** Down：

```go
	// 默认选中首项"下一首播放"；下移 2 次到列表"收藏"再 Enter
	if _, ok := m.plPicker.list.SelectedItem().(pickerQueueNextItem); !ok {
		t.Fatalf("默认选中项 = %+v, want pickerQueueNextItem", m.plPicker.list.SelectedItem())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
```

（原 1 次 Down 变 2 次；后续断言不变）

- **TestPickerCreateNewListFlow**（约 L453）：无既有列表时条目为 `下一首播放 / 当前播放队列 / ＋ 新建列表`，下移 **2 次** 到新建项：

```go
	// 无既有列表：首项"下一首播放" + "当前播放队列" + 末尾"＋ 新建列表"；下移 2 次选中新建项
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
```

- **TestPickerQueueItemAppendsToQueue**（约 L628）：改为同时断言两个队列项——默认选中 `pickerQueueNextItem`（Title `▶ 下一首播放`、Description `插入到当前曲之后`），Down 一次后选中 `pickerQueueItem`（Title `▶ 当前播放队列`、Description `追加到队尾`），Enter 发 `trackAppendMsg`：

```go
	// 首项"下一首播放"（默认选中）
	it0, ok := m.plPicker.list.SelectedItem().(pickerQueueNextItem)
	if !ok {
		t.Fatalf("默认选中项 = %+v, want pickerQueueNextItem", m.plPicker.list.SelectedItem())
	}
	if stripANSI(it0.Title()) != "▶ 下一首播放" {
		t.Errorf("下一首项 Title = %q, want ▶ 下一首播放", it0.Title())
	}
	if it0.Description() != "插入到当前曲之后" {
		t.Errorf("下一首项 Description = %q, want 插入到当前曲之后", it0.Description())
	}
	// 第二项"当前播放队列"（追加到队尾）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	it, ok := m.plPicker.list.SelectedItem().(pickerQueueItem)
	if !ok {
		t.Fatalf("Down 后选中项 = %+v, want pickerQueueItem", m.plPicker.list.SelectedItem())
	}
	if stripANSI(it.Title()) != "▶ 当前播放队列" {
		t.Errorf("队列项 Title = %q, want ▶ 当前播放队列", it.Title())
	}
	if it.Description() != "追加到队尾" {
		t.Errorf("队列项 Description = %q, want 追加到队尾", it.Description())
	}
```

（Enter 发 `trackAppendMsg` 的后续断言不变）

1c. `ui/queue_test.go` 更新 2 处：

- `openPickerAndAppendQueue` helper（约 L39）：默认项现在是"下一首播放"，追加流程需先 Down 一次再 Enter：

```go
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中第二项"当前播放队列"
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
```

（注释同步改为"Enter 确认第二项'当前播放队列'（追加到队尾）"）

- **TestHistoryAppend**（约 L450）：同上，Enter 前加一次 Down（保持"追加到队尾"语义），注释更新。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./ui/ -run 'TestTrackInsertNextMsg|TestPicker|TestPlaylistDetailRemoveAndAppend|TestHistoryAppend' 2>&1 | tail -30`
Expected: FAIL（`trackInsertNextMsg`/`pickerQueueNextItem` undefined + 断言不匹配）

- [ ] **Step 3: 实现 root 路由**（`ui/root.go`）

3a. `trackAppendMsg` 定义之后新增：

```go
// trackInsertNextMsg 选择器请求把曲目插入到当前曲之后（下一首播放）。
type trackInsertNextMsg struct {
	track model.Track
}
```

3b. `case trackAppendMsg:` 分支之后新增：

```go
	case trackInsertNextMsg:
		// 插入到当前曲之后（下一首播放）：不打断当前播放，也不自动开播
		m.queue.InsertNext(msg.track)
		return m.syncQueueViews(), nil
```

3c. `emitTrackAppend` 之后新增：

```go
func emitTrackInsertNext(track model.Track) tea.Cmd {
	return func() tea.Msg { return trackInsertNextMsg{track: track} }
}
```

- [ ] **Step 4: 实现 picker**（`ui/playlists.go`）

4a. `pickerQueueItem` 定义之前新增：

```go
// pickerQueueNextItem 选择器首项：插入到当前曲之后（下一首播放，固定第一项，默认选中）。
// 样式加粗与末尾粉色新建项区分。
type pickerQueueNextItem struct{}

func (pickerQueueNextItem) Title() string {
	return lipgloss.NewStyle().Bold(true).Render("▶ 下一首播放")
}
func (pickerQueueNextItem) Description() string { return "插入到当前曲之后" }
func (pickerQueueNextItem) FilterValue() string { return "下一首播放" }
```

4b. `pickerQueueItem` 注释更新为"选择器第二项：追加到当前播放队列"。

4c. `refreshItems` 首行追加：

```go
	items = append(items, pickerQueueNextItem{})
	items = append(items, pickerQueueItem{})
```

（替换原有单条 `pickerQueueItem{}` append；容量 `len(lists)+2` 改为 `len(lists)+3`）

4d. Enter 分发新增分支（`case pickerQueueItem:` 之前）：

```go
			case pickerQueueNextItem:
				// 插入到当前曲之后（下一首播放）：走全局 trackInsertNextMsg，
				// 不设 notice（插入无成功 toast，与追加一致）。
				p.closed = true
				return p, emitTrackInsertNext(p.track)
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./ui/ -run 'TestTrackInsertNextMsg|TestPicker|TestPlaylistDetailRemoveAndAppend|TestHistoryAppend' -v 2>&1 | tail -40`
Expected: PASS。然后 `go test ./ui/ 2>&1 | tail -10` 全绿（可能有其他依赖默认项顺序的断言，失败则按新顺序修正——先读测试再改，不要盲改）。

- [ ] **Step 6: 全量验证 + 提交**

```bash
go build ./... && go vet ./... && go test ./... 2>&1 | tail -20
git add ui/root.go ui/playlists.go ui/root_test.go ui/playlists_ui_test.go ui/queue_test.go
git status   # 确认只有这 5 个文件
git commit -m "feat(ui): a 选择器新增'下一首播放'首项（trackInsertNextMsg 路由）"
```

---

## 验证（两个任务都完成后）

- `go build ./...`、`go vet ./...`、`go test ./...` 全绿（含 `go test -race ./queue/ ./ui/`）
- git log 两个 feat commit，`git status` clean
- 与 master 差异仅限：queue/queue.go、queue/queue_test.go、ui/root.go、ui/playlists.go、ui/root_test.go、ui/playlists_ui_test.go、ui/queue_test.go

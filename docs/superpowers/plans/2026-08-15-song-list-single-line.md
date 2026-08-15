# 歌曲列表条目单行化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 music-tui 的 4 处歌曲列表（搜索/队列/历史/播放列表详情）条目从两行显示改为单行（`标题 - 作者 · 时长`），保留序号/▶ 标记/当前曲加粗，间距不变。

**Architecture:** 利用 charmbracelet `list.DefaultDelegate` 的 `ShowDescription=false` 单行模式（条目高度 1、自动 `ansi.Truncate` 省略号截断，从右截：先丢时长→作者→标题保底）。新增共享函数 `formatTrackLine(title, artist, meta)` 统一拼接逻辑，4 个条目类型（`trackItem`/`queueItem`/`historyItem`/`plTrackItem`）的 `Title()` 调用它，`Description()` 返回空串。仅歌曲列表的 delegate 改单行；播放列表概览/登录设置/添加到选择器保持双行。

**Tech Stack:** Go 1.25、charmbracelet bubbles v1.0.0 (list)、lipgloss v1.1.0、bubbletea。测试框架为 Go 标准 `testing`。

**设计文档:** `docs/superpowers/specs/2026-08-15-song-list-single-line-design.md`

**背景知识（worker 必读）:**
- 列表组件：`list.New(nil, delegate, 80, 24)`，条目实现 `list.Item` 接口（`Title()`/`Description()`/`FilterValue()`）。`DefaultDelegate` 的 `Render` 在 `ShowDescription=false` 时只渲染 Title 一行，`Height()` 返回 1。
- `testTrack(id)`（ui/root_test.go:196）生成 `Title: "测试歌曲 "+id`、`Artist: "测试歌手"`、`Duration: 200`（= "03:20"）。
- 工具函数：`formatDuration(sec float64) string`（ui/format.go，mm:ss/HH:MM:SS）、`formatPlayedAt(t, now)`（ui/format.go，"今天 15:04"/"昨天 15:04"/"2006-01-02 15:04"）。
- 测试命令：`export PATH=/home/ivhu/go-sdk/go/bin:$PATH` 后 `go test ./ui/ -run <TestName> -v`。
- **工作区隔离：执行前先用 superpowers:using-git-worktrees 技能创建 worktree；若仓库有并行会话在改文件，先 `git pull`/`git fetch` 再开工；commit 只 `git add` 自己负责的文件。**

---

### Task 1: formatTrackLine 共享函数（TDD）

**Files:**
- Modify: `ui/format.go`（追加函数）
- Test: `ui/format_test.go`（追加测试）

- [ ] **Step 1: 写失败测试**

在 `ui/format_test.go` 末尾追加：

```go
func TestFormatTrackLine(t *testing.T) {
	cases := []struct {
		name, title, artist, meta, want string
	}{
		{"正常", "晴天", "周杰伦", "03:45", "晴天 - 周杰伦 · 03:45"},
		{"空作者", "晴天", "", "03:45", "晴天 · 03:45"},
		{"空附加", "晴天", "周杰伦", "", "晴天 - 周杰伦"},
		{"空作者空附加", "晴天", "", "", "晴天"},
		{"标题含连字符", "A - B", "C", "01:00", "A - B - C · 01:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatTrackLine(c.title, c.artist, c.meta); got != c.want {
				t.Errorf("formatTrackLine(%q,%q,%q) = %q, want %q", c.title, c.artist, c.meta, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run TestFormatTrackLine -v`
Expected: FAIL（`formatTrackLine` undefined）

- [ ] **Step 3: 实现函数**

在 `ui/format.go` 末尾追加（注释风格与文件现有注释一致，中文）：

```go
// formatTrackLine 拼接单行歌曲条目："标题 - 作者 · 附加"。
// 作者为空时省略 " - " 分隔符；附加信息（时长/播放时间）为空时省略 " · " 段。
func formatTrackLine(title, artist, meta string) string {
	line := title
	if artist != "" {
		line += " - " + artist
	}
	if meta != "" {
		line += " · " + meta
	}
	return line
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run TestFormatTrackLine -v`
Expected: PASS（5 个子测试全绿）

- [ ] **Step 5: Commit**

```bash
cd /data/code/music-tui && git add ui/format.go ui/format_test.go && git commit -m "feat(ui): formatTrackLine 单行条目拼接函数（空作者/空附加兜底）+ 单测"
```

---

### Task 2: 搜索结果页单行化（trackItem）

**Files:**
- Modify: `ui/search.go`（trackItem 的 Title/Description、newSearchModel 的 delegate）

- [ ] **Step 1: 修改条目方法与 delegate**

在 `ui/search.go` 中：

1. `newSearchModel` 内 `delegate := list.NewDefaultDelegate()` 之后加一行：

```go
	delegate.ShowDescription = false // 单行条目（标题 - 作者 · 时长）
```

2. 替换 `trackItem` 的两个方法（当前为 `return i.track.Title` 与 `return i.track.Artist + " · " + formatDuration(i.track.Duration)`）：

```go
func (i trackItem) Title() string {
	return formatTrackLine(i.track.Title, i.track.Artist, formatDuration(i.track.Duration))
}
func (i trackItem) Description() string { return "" }
```

- [ ] **Step 2: 跑 ui 包测试**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run 'TestSearch' -v`
Expected: PASS（search 相关测试不依赖双行渲染）

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui && git add ui/search.go && git commit -m "feat(ui): 搜索结果条目单行化（标题 - 作者 · 时长）"
```

---

### Task 3: 队列页单行化（queueItem）

**Files:**
- Modify: `ui/queue.go`（queueItem 的 Title/Description、newQueueModel 的 delegate）

- [ ] **Step 1: 修改条目方法与 delegate**

在 `ui/queue.go` 中：

1. `newQueueModel` 内 `delegate := list.NewDefaultDelegate()` 之后加一行：

```go
	delegate.ShowDescription = false // 单行条目（▶ 序号. 标题 - 作者 · 时长）
```

2. 替换 `queueItem` 的 Title/Description（当前 Title 只拼 `%s%2d. %s` 且 Description 处理空 Artist）：

```go
func (i queueItem) Title() string {
	prefix := "  "
	if i.current {
		prefix = "▶ "
	}
	line := fmt.Sprintf("%s%2d. %s", prefix, i.idx+1,
		formatTrackLine(i.track.Title, i.track.Artist, formatDuration(i.track.Duration)))
	if i.current {
		line = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render(line)
	}
	return line
}

func (i queueItem) Description() string { return "" }
```

注意：`queueItem` 的空 Artist 处理由 `formatTrackLine` 承接（原 Description 中的 `if i.track.Artist == ""` 分支删除，逻辑等价：空作者时显示 `标题 · 03:20`）。

- [ ] **Step 2: 跑队列相关测试**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run 'TestQueue' -v`
Expected: PASS（TestQueueItemMarkers 的 Contains 断言 "▶"/"1." 依然成立）

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui && git add ui/queue.go && git commit -m "feat(ui): 队列条目单行化（保留 ▶ 标记/序号/当前曲加粗）"
```

---

### Task 4: 历史页单行化（historyItem）+ 测试更新

**Files:**
- Modify: `ui/history.go`（historyItem 的 Title/Description、newHistoryModel 的 delegate）
- Modify: `ui/history_test.go`（TestHistoryItemTitleAndDescription）

- [ ] **Step 1: 修改条目方法与 delegate（先改实现，测试随后更新）**

在 `ui/history.go` 中：

1. `newHistoryModel` 内 `delegate := list.NewDefaultDelegate()` 之后加一行：

```go
	delegate.ShowDescription = false // 单行条目（标题 - 作者 · 播放时间）
```

2. 替换 `historyItem` 的 Title/Description：

```go
func (i historyItem) Title() string {
	return formatTrackLine(i.entry.Track.Title, i.entry.Track.Artist, formatPlayedAt(i.entry.PlayedAt, time.Now()))
}
func (i historyItem) Description() string { return "" }
```

- [ ] **Step 2: 更新 TestHistoryItemTitleAndDescription**

在 `ui/history_test.go` 中，将 `TestHistoryItemTitleAndDescription`（约 line 92-103）整体替换为：

```go
func TestHistoryItemTitleAndDescription(t *testing.T) {
	tr := testTrack("t1")
	item := historyItem{entry: historyEntry(tr)}
	want := tr.Title + " - " + tr.Artist + " · "
	if !strings.HasPrefix(item.Title(), want) {
		t.Errorf("Title = %q, want 前缀 %q", item.Title(), want)
	}
	if item.FilterValue() != tr.Title+" "+tr.Artist {
		t.Errorf("FilterValue = %q", item.FilterValue())
	}
	if item.Description() != "" {
		t.Errorf("Description = %q, want 空（单行模式）", item.Description())
	}
}
```

（testTrack 的 Duration=200，`formatPlayedAt` 依赖 `time.Now()`，故只断言前缀。）

- [ ] **Step 3: 跑历史相关测试**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run 'TestHistory' -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /data/code/music-tui && git add ui/history.go ui/history_test.go && git commit -m "feat(ui): 历史条目单行化（标题 - 作者 · 播放时间）+ 测试更新"
```

---

### Task 5: 播放列表详情单行化（plTrackItem）+ delegate 拆分

**Files:**
- Modify: `ui/playlists.go`（newPlaylistModel 的 delegate 拆分、plTrackItem 的 Title/Description）

- [ ] **Step 1: 拆分 delegate 并修改条目方法**

在 `ui/playlists.go` 的 `newPlaylistModel` 中，当前 `ov`/`dt`/`st` 三个列表**共享**同一个 `delegate` 变量。拆成三个独立 delegate（概览/设置保持双行，仅详情单行）：

```go
func newPlaylistModel() playlistModel {
	ovDelegate := list.NewDefaultDelegate()
	dtDelegate := list.NewDefaultDelegate()
	dtDelegate.ShowDescription = false // 详情歌曲条目单行（标题 - 作者 · 时长）
	stDelegate := list.NewDefaultDelegate()
	ov := list.New(nil, ovDelegate, 80, 24)
	ov.Title = "播放列表"
	ov.SetShowHelp(false)
	ov.SetFilteringEnabled(false)
	ov.SetShowStatusBar(false)
	dt := list.New(nil, dtDelegate, 80, 24)
	dt.Title = ""
	dt.SetShowHelp(false)
	dt.SetFilteringEnabled(false)
	dt.SetShowStatusBar(false)
	st := list.New(nil, stDelegate, 80, 24)
	st.Title = ""
	st.SetShowHelp(false)
	st.SetFilteringEnabled(false)
	st.SetShowStatusBar(false)
	ti := textinput.New()
	ti.Placeholder = "输入列表名，Enter 确认"
	ti.CharLimit = 4096 // 共享输入框：URL 导入/粘贴 Cookie 需要长输入（列表名仅占小头）
	return playlistModel{overview: ov, detail: dt, setup: st, input: ti, ytSyncNames: map[string]bool{}}
}
```

注意：只改 `newPlaylistModel` 函数体开头（原函数其余部分不变，包括 `ti` 与 return 行）。原 `delegate := list.NewDefaultDelegate()` 一行删除。

替换 `plTrackItem` 的 Title/Description：

```go
func (i plTrackItem) Title() string {
	return fmt.Sprintf("%2d. %s", i.idx+1,
		formatTrackLine(i.track.Title, i.track.Artist, formatDuration(i.track.Duration)))
}
func (i plTrackItem) Description() string { return "" }
```

- [ ] **Step 2: 跑播放列表测试**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test ./ui/ -run 'TestPlaylist|TestPl' -v`
Expected: PASS（overviewItem 的 Description 测试不受影响，因概览 delegate 仍为双行；详情渲染断言用 Contains 不受单行影响）

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui && git add ui/playlists.go && git commit -m "feat(ui): 播放列表详情条目单行化（拆分管 overview/setup 的双行 delegate）"
```

---

### Task 6: 全量验证

**Files:** 无

- [ ] **Step 1: go build + go vet**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go build ./... && go vet ./...`
Expected: 无输出（成功）

- [ ] **Step 2: 全量测试（含 race）**

Run: `export PATH=/home/ivhu/go-sdk/go/bin:$PATH && cd /data/code/music-tui && go test -race ./...`
Expected: 全部 `ok`（ui 包约 82s，耐心等待）

- [ ] **Step 3: 若有失败，修复并重跑**

若出现意外失败（如某渲染断言依赖双行高度），定位断言并更新为单行语义（与 Task 4 Step 2 同风格：改断言为单行格式校验），重跑 Step 2 直到全绿。commit 修复。

- [ ] **Step 4: 汇报**

向 feature_lead 汇报：改动文件列表、测试结果（build/vet/test -race 全绿）、以及请用户终端确认的清单（4 个列表的单行效果、长标题截断、空作者条目）。

---

## 自审记录

- **Spec 覆盖**：范围（4 列表）→ Task 2-5；格式 `标题 - 作者 · 时长` → Task 1 函数 + 各 Task；序号/▶/加粗保留 → Task 3/5；间距不变 → 无 SetSpacing 调用；非歌曲列表不动 → Task 5 拆分 delegate 保 overview/setup 双行；测试 → Task 1/4 + Task 6 全量。
- **类型一致性**：`formatTrackLine(title, artist, meta string) string` 在 Task 1 定义，Task 2-5 调用签名一致；`formatDuration`/`formatPlayedAt` 为现有函数，签名未改。
- **占位符扫描**：无 TBD/TODO；每个代码步骤含完整代码；命令含期望输出。

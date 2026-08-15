# 队列/历史页 `/` 过滤功能 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 队列页、历史页按 `/` 打开过滤输入框，实时子串过滤（标题/歌手、大小写不敏感），Enter 确认、Esc 退出恢复完整列表，过滤态操作作用于过滤后列表项（原索引天然正确）。

**Architecture:** 复用搜索页 textinput 模式：每页新增 `filtering bool` + `filterInput textinput.Model`；过滤后条目**复用原 queueItem/historyItem 结构**（`idx`/`entry` 携带原始数据，操作键零改动）；共享纯函数 `filterMatches` 做匹配；`sync`/`setEntries` 数据刷新时重放过滤。选中按曲目 ID 保持、未命中 clamp 到可见末尾。

**Tech Stack:** Go 1.25、charmbracelet/bubbles v1.0.0（list + textinput）、bubbletea、lipgloss。TDD。

**执行环境:** worktree `/data/code/music-tui/.worktrees/slash-filter`（分支 feat/slash-filter）。Go 不在 PATH：`export PATH=/home/ivhu/go-sdk/go/bin:$PATH GOROOT=/home/ivhu/go-sdk/go GOPATH=/data/GO/GOPATH`。测试：`cd /data/code/music-tui/.worktrees/slash-filter && go test ./ui -run <Test名> -v`。

**已提交基线（lead 完成）：** `docs/superpowers/specs/2026-08-15-queue-history-slash-filter-design.md`、`ui/filter.go`（`filterMatches`）、`ui/filter_test.go`。任务 1/2 直接使用，勿重复实现。

---

### Task 1: 队列页过滤（worker A）

**Files:**
- Modify: `ui/queue.go`
- Test: `ui/queue_test.go`

**交互契约（与 spec 一致，勿改）：**

| 按键 | 聚焦中（编辑） | 已确认（失焦、过滤生效） |
|---|---|---|
| `/` | 输入字符 | 重新聚焦（保留关键词） |
| 字符键 | 进过滤词，实时过滤 | 页面操作（p/d/c/s） |
| ↑↓ | 移动列表选中 | 移动列表选中 |
| Enter | 确认（失焦，过滤保持） | 播放选中项 |
| Esc | 退出过滤：清词、恢复全量 | 同左 |

- [ ] **Step 1: 写失败测试**（追加到 `ui/queue_test.go`）

```go
// TestQueueSlashFilter 队列页 / 过滤全流程：打开→输入实时过滤→计数→
// Enter 确认→过滤态播放/删除（原始下标）→Esc 恢复。
func TestQueueSlashFilter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 队列页

	// / 打开过滤
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.queuePage.filtering || !m.queuePage.filterInput.Focused() {
		t.Fatalf("/ 后 filtering=%v focused=%v, want true/true", m.queuePage.filtering, m.queuePage.filterInput.Focused())
	}
	if got := m.queuePage.view(); !strings.Contains(got, "过滤:") {
		t.Errorf("过滤行未渲染: %q", got)
	}

	// 输入 "t2" 实时过滤 + 计数
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if got := m.queuePage.view(); !strings.Contains(got, "(1/3)") {
		t.Errorf("计数应显示 (1/3): %q", got)
	}
	if n := len(m.queuePage.list.VisibleItems()); n != 1 {
		t.Fatalf("过滤后可见 %d 项, want 1", n)
	}

	// Enter 确认：失焦、过滤保持
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.queuePage.filterInput.Focused() {
		t.Fatal("Enter 应确认过滤并失焦")
	}
	if m.queuePage.filtering != true {
		t.Fatal("确认后 filtering 应保持 true")
	}

	// 确认态 Enter 播放 → queuePlayMsg.index 为原始下标 1
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var play queuePlayMsg
	for _, msg := range execCmds(cmd) {
		if pm, ok := msg.(queuePlayMsg); ok {
			play = pm
		}
	}
	if play.index != 1 {
		t.Fatalf("过滤态播放 index = %d, want 1（原始队列下标）", play.index)
	}

	// 确认态 d 删除 → deleteEntryMsg 原始下标 1
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var del queueDeleteMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(queueDeleteMsg); ok {
			del = dm
		}
	}
	if del.index != 1 {
		t.Fatalf("过滤态删除 index = %d, want 1", del.index)
	}
	m, _ = update(m, del) // root 执行删除
	if len(m.queuePage.list.VisibleItems()) != 0 {
		t.Errorf("删除后过滤列表应为空（剩余 t1/t3 不含 t2）")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "(0/2)") {
		t.Errorf("删除后计数应 (0/2): %q", got)
	}

	// Esc 恢复完整列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queuePage.filtering {
		t.Fatal("Esc 应退出过滤")
	}
	if got := m.queuePage.view(); strings.Contains(got, "过滤:") {
		t.Errorf("退出后不应有过滤行: %q", got)
	}
	if n := len(m.queuePage.list.VisibleItems()); n != 2 {
		t.Fatalf("恢复后可见 %d 项, want 2", n)
	}
}

// TestQueueSlashFilterIndexMapping 多命中时选中项与原始下标映射。
func TestQueueSlashFilterIndexMapping(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // 命中全部 3 项
	if n := len(m.queuePage.list.VisibleItems()); n != 3 {
		t.Fatalf("过滤后可见 %d 项, want 3", n)
	}
	// 聚焦态 ↑↓ 仍可移动列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 确认（选中第 2 项）
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var play queuePlayMsg
	for _, msg := range execCmds(cmd) {
		if pm, ok := msg.(queuePlayMsg); ok {
			play = pm
		}
	}
	if play.index != 1 {
		t.Fatalf("播放 index = %d, want 1", play.index)
	}
	// 聚焦态再按 / 是输入字符
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if m.queuePage.filterInput.Value() != "/" {
		t.Fatalf("聚焦态 / 应输入字符, got %q", m.queuePage.filterInput.Value())
	}
}

// TestQueueSlashFilterSyncReapplies 过滤态下 sync 重放过滤（删除后计数一致）。
func TestQueueSlashFilterSyncReapplies(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	// 外部变化（如搜索页追加 t4）→ sync 后过滤仍生效
	m.queue.Add(testTrack("t4"))
	m = m.syncQueueViews()
	if n := len(m.queuePage.list.VisibleItems()); n != 3 {
		t.Fatalf("sync 后过滤列表 %d 项, want 3（t4 不含关键词）", n)
	}
	if got := m.queuePage.view(); !strings.Contains(got, "(3/4)") {
		t.Errorf("sync 后计数应 (3/4): %q", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./ui -run 'TestQueueSlashFilter' -v` → 编译失败（`queueModel.filtering` 未定义）。预期失败即进入 Step 3。
- [ ] **Step 3: 实现 `ui/queue.go`**（结构如下，保持既有函数与注释风格；勿动 `queueItem`/`modeLabel`/`modeName`/消息类型）：

```go
// 新增 import：strings、github.com/charmbracelet/bubbles/textinput

type queueModel struct {
	list    list.Model
	items   []model.Track
	current int
	mode    queue.Mode
	aiTitle, aiArtist string

	filtering   bool
	filterInput textinput.Model

	width, height int
}

func newQueueModel() queueModel {
	// ...既有 list 初始化不变...
	ti := textinput.New()
	ti.CharLimit = 120
	return queueModel{list: l, current: -1, filterInput: ti}
}

// typing 返回过滤输入框是否聚焦（root 让字符类全局键空格/a/q 让位；与搜索页同名方法一致）。
func (q queueModel) typing() bool { return q.filtering && q.filterInput.Focused() }

func (q queueModel) Update(msg tea.Msg) (queueModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "/":
			// 打开或重聚焦过滤输入框；已聚焦时 "/" 作为普通字符输入
			if !q.filtering {
				q.filtering = true
				q.filterInput.Focus()
				return q, nil
			}
			if !q.filterInput.Focused() {
				q.filterInput.Focus()
				return q, nil
			}
		case "esc":
			// 任何过滤态 Esc：退出过滤，清空关键词恢复完整列表
			if q.filtering {
				q.filtering = false
				q.filterInput.Blur()
				q.filterInput.SetValue("")
				return q.applyFilter(), nil
			}
		}
		if q.filtering && q.filterInput.Focused() {
			switch msg.String() {
			case "enter":
				// 确认过滤：失焦、过滤保持生效
				q.filterInput.Blur()
				return q, nil
			case "up", "down":
				// 聚焦时方向键仍操作列表（textinput 不消费方向键）
				var cmd tea.Cmd
				q.list, cmd = q.list.Update(msg)
				return q, cmd
			default:
				var cmd tea.Cmd
				q.filterInput, cmd = q.filterInput.Update(msg)
				return q.applyFilter(), cmd
			}
		}
		switch msg.String() {
		case "enter", "p":
			if item, ok := q.list.SelectedItem().(queueItem); ok {
				return q, emitQueuePlay(item.idx)
			}
			return q, nil
		case "d":
			if item, ok := q.list.SelectedItem().(queueItem); ok {
				return q, emitQueueDelete(item.idx)
			}
			return q, nil
		case "c":
			return q, emitQueueClear()
		case "s":
			return q, emitQueueMode()
		}
	}
	var cmd tea.Cmd
	q.list, cmd = q.list.Update(msg)
	return q, cmd
}

// itemAt 构造第 i 项：当前曲目应用 AI 清洗标题（若有）。
func (q queueModel) itemAt(i int) queueItem {
	tr := q.items[i]
	if i == q.current && q.aiTitle != "" {
		tr.Title, tr.Artist = q.aiTitle, q.aiArtist
	}
	return queueItem{track: tr, idx: i, current: i == q.current}
}

// applyFilter 按当前过滤词重算列表展示（过滤中只显示命中条目，queueItem.idx
// 保留原始队列下标，播放/删除直接可用；未过滤时显示全量）。选中项尽量按
// 曲目 ID 保持，被过滤掉/已删除则 clamp 到可见末尾（回归：TestQueueDeleteKeepsSelectionValid）。
func (q queueModel) applyFilter() queueModel {
	keep := ""
	if it, ok := q.list.SelectedItem().(queueItem); ok {
		keep = it.track.ID
	}
	visible := make([]list.Item, 0, len(q.items))
	kw := q.filterInput.Value()
	for i := range q.items {
		it := q.itemAt(i)
		if q.filtering && !filterMatches(kw, it.FilterValue()) {
			continue
		}
		visible = append(visible, it)
	}
	q.list.SetItems(visible)
	if keep != "" {
		for i, it := range visible {
			if it.(queueItem).track.ID == keep {
				q.list.Select(i)
				return q
			}
		}
	}
	if len(visible) > 0 {
		if idx := q.list.Index(); idx >= len(visible) {
			q.list.Select(len(visible) - 1)
		}
	}
	return q
}

// sync 用队列最新状态刷新页面（root 在队列变化后调用）。数据刷新后重放过滤。
func (q queueModel) sync(qu *queue.Queue) queueModel {
	q.items = qu.Tracks()
	q.current = qu.CurrentIndex()
	q.mode = qu.Mode()
	q.list.Title = fmt.Sprintf("播放队列 (%d)", qu.Len())
	return q.applyFilter()
}

func (q queueModel) setSize(width, height int) queueModel {
	q.width, q.height = width, height
	q.list.SetSize(width, height-3)
	q.filterInput.Width = width - 14
	if q.filterInput.Width < 10 {
		q.filterInput.Width = 10
	}
	return q
}

// view 渲染队列页：过滤开启时顶部为过滤行（"过滤: [输入框] (n/m)"），
// 提示行随过滤状态切换；空队列且未过滤时显示空态提示。
func (q queueModel) view() string {
	if q.filtering {
		hint := "列表循环 · 输入过滤 · Enter 确认 · Esc 取消"
		if !q.filterInput.Focused() {
			hint = "列表循环 · Enter/p 跳转播放 · d 删除 · c 清空 · s 切换模式 · Esc 退出过滤"
		}
		count := fmt.Sprintf("(%d/%d)", len(q.list.VisibleItems()), len(q.items))
		filterLine := "过滤: " + q.filterInput.View() + " " + lipgloss.NewStyle().Faint(true).Render(count)
		return bottomHint(q.height, filterLine+"\n"+q.list.View(), hint)
	}
	hint := q.modeLabel() + " · Enter/p 跳转播放 · d 删除 · c 清空 · s 切换模式"
	if len(q.items) == 0 {
		content := lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("队列为空\n\n搜索页选中结果后按 a 添加到队列，Enter 立即播放")
		return bottomHint(q.height, content, hint)
	}
	return bottomHint(q.height, q.list.View(), hint)
}
```

> 注：`sync` 的既有选中保持逻辑（keep-by-ID + clamp）整体并入 `applyFilter`，行为等价——选中项在 SetItems 前后由 `list.Index()` 保持，越界时 clamp。原 sync 中的 `keepIdx` 逻辑删除。

- [ ] **Step 4: 运行确认通过**：`go test ./ui -run 'TestQueueSlashFilter' -v` → 3 个测试 PASS。
- [ ] **Step 5: 回归**：`go test ./ui` 全绿（重点 TestQueueDeleteKeepsSelectionValid、TestQueueTabShowsEmptyView、TestQueueHintOnLastLine、TestSearchEnterReplacesQueue 等队列相关）。
- [ ] **Step 6: Commit**：`git add ui/queue.go ui/queue_test.go && git commit -m "feat(ui): 队列页 / 过滤（实时过滤、Enter 确认、Esc 恢复、过滤态操作原索引）"`（只 add 自己负责的文件）。

---

### Task 2: 历史页过滤（worker B）

**Files:**
- Modify: `ui/history.go`
- Test: `ui/history_test.go`

**交互契约：与 Task 1 完全一致**（/ 打开、字符实时过滤、↑↓ 移动、Enter 确认、Esc 退出恢复、确认态操作键作用于过滤后条目）。

- [ ] **Step 1: 写失败测试**（追加到 `ui/history_test.go`）

```go
// TestHistorySlashFilter 历史页 / 过滤全流程：打开→输入→计数→Enter 确认→
// 过滤态重播/删除（原始记录）→Esc 恢复。
func TestHistorySlashFilter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := m.history.Add(testTrack(id)); err != nil {
			t.Fatal(err)
		}
	}
	m = m.refreshHistory()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 历史页

	// / 打开过滤
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.historyPage.filtering || !m.historyPage.filterInput.Focused() {
		t.Fatalf("/ 后 filtering=%v focused=%v, want true/true", m.historyPage.filtering, m.historyPage.filterInput.Focused())
	}
	if got := m.historyPage.view(); !strings.Contains(got, "过滤:") {
		t.Errorf("过滤行未渲染: %q", got)
	}

	// 输入 "t2" 实时过滤 + 计数（历史最新在前，t2 在第 2 位）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if got := m.historyPage.view(); !strings.Contains(got, "(1/3)") {
		t.Errorf("计数应显示 (1/3): %q", got)
	}
	if n := len(m.historyPage.list.VisibleItems()); n != 1 {
		t.Fatalf("过滤后可见 %d 项, want 1", n)
	}

	// Enter 确认
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.historyPage.filterInput.Focused() {
		t.Fatal("Enter 应确认过滤并失焦")
	}

	// 确认态 Enter 重播 → trackSelectedMsg 应为 t2
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var sel trackSelectedMsg
	for _, msg := range execCmds(cmd) {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t2" {
		t.Fatalf("过滤态重播 = %s, want t2", sel.track.ID)
	}

	// 确认态 d 删除 → deleteEntryMsg 应为 t2
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var del deleteEntryMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(deleteEntryMsg); ok {
			del = dm
		}
	}
	if del.id != "t2" {
		t.Fatalf("过滤态删除 id = %s, want t2", del.id)
	}
	m, _ = update(m, del) // root 执行删除
	if n := len(m.historyPage.list.VisibleItems()); n != 0 {
		t.Errorf("删除后过滤列表应为空, got %d", n)
	}
	if got := m.historyPage.view(); !strings.Contains(got, "(0/2)") {
		t.Errorf("删除后计数应 (0/2): %q", got)
	}

	// Esc 恢复完整列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.historyPage.filtering {
		t.Fatal("Esc 应退出过滤")
	}
	if got := m.historyPage.view(); strings.Contains(got, "过滤:") {
		t.Errorf("退出后不应有过滤行: %q", got)
	}
	if n := len(m.historyPage.list.VisibleItems()); n != 2 {
		t.Fatalf("恢复后可见 %d 项, want 2", n)
	}
}

// TestHistorySlashFilterMapping 多命中 + 聚焦态方向键 + 确认态 a 添加到选择器（全局键）。
func TestHistorySlashFilterMapping(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := m.history.Add(testTrack(id)); err != nil {
			t.Fatal(err)
		}
	}
	m = m.refreshHistory()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // 命中全部
	if n := len(m.historyPage.list.VisibleItems()); n != 3 {
		t.Fatalf("过滤后可见 %d 项, want 3", n)
	}
	// 聚焦态 ↑↓ 移动（历史最新在前：t3, t2, t1；down 一次到 t2）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 确认
	// 确认态 a 打开"添加到"选择器（root 全局键，作用于过滤后选中项）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("确认态按 a 应打开选择器")
	}
	if m.plPicker.track.ID != "t2" {
		t.Fatalf("选择器 track = %s, want t2", m.plPicker.track.ID)
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./ui -run 'TestHistorySlashFilter' -v` → 编译失败（`historyModel.filtering` 未定义）。预期失败即进入 Step 3。
- [ ] **Step 3: 实现 `ui/history.go`**（结构与 Task 1 对称；勿动 `historyItem`/消息类型）：

```go
// 新增 import：strings、github.com/charmbracelet/bubbles/textinput

type historyModel struct {
	list    list.Model
	entries []history.Entry

	filtering   bool
	filterInput textinput.Model

	width, height int
}

func newHistoryModel() historyModel {
	// ...既有 list 初始化不变...
	ti := textinput.New()
	ti.CharLimit = 120
	return historyModel{list: l, filterInput: ti}
}

// typing 返回过滤输入框是否聚焦（root 让字符类全局键空格/a/q 让位）。
func (h historyModel) typing() bool { return h.filtering && h.filterInput.Focused() }

func (h historyModel) Update(msg tea.Msg) (historyModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "/":
			if !h.filtering {
				h.filtering = true
				h.filterInput.Focus()
				return h, nil
			}
			if !h.filterInput.Focused() {
				h.filterInput.Focus()
				return h, nil
			}
		case "esc":
			if h.filtering {
				h.filtering = false
				h.filterInput.Blur()
				h.filterInput.SetValue("")
				return h.applyFilter(), nil
			}
		}
		if h.filtering && h.filterInput.Focused() {
			switch msg.String() {
			case "enter":
				h.filterInput.Blur()
				return h, nil
			case "up", "down":
				var cmd tea.Cmd
				h.list, cmd = h.list.Update(msg)
				return h, cmd
			default:
				var cmd tea.Cmd
				h.filterInput, cmd = h.filterInput.Update(msg)
				return h.applyFilter(), cmd
			}
		}
		switch msg.String() {
		case "enter", "p":
			if item, ok := h.list.SelectedItem().(historyItem); ok {
				return h, emitTrackSelected(item.entry.Track)
			}
			return h, nil
		case "d":
			if item, ok := h.list.SelectedItem().(historyItem); ok {
				return h, emitDeleteEntry(item.entry.Track.ID, item.entry.Track.Source)
			}
			return h, nil
		case "c":
			return h, emitClearHistory()
		}
	}
	var cmd tea.Cmd
	h.list, cmd = h.list.Update(msg)
	return h, cmd
}

// applyFilter 按当前过滤词重算列表展示（过滤中只显示命中条目，historyItem
// 携带完整 entry，重播/删除直接可用；未过滤时显示全量）。选中项尽量按
// 曲目 ID 保持，未命中 clamp 到可见末尾。
func (h historyModel) applyFilter() historyModel {
	keep := ""
	if it, ok := h.list.SelectedItem().(historyItem); ok {
		keep = it.entry.Track.ID
	}
	visible := make([]list.Item, 0, len(h.entries))
	kw := h.filterInput.Value()
	for _, e := range h.entries {
		it := historyItem{entry: e}
		if h.filtering && !filterMatches(kw, it.FilterValue()) {
			continue
		}
		visible = append(visible, it)
	}
	h.list.SetItems(visible)
	if keep != "" {
		for i, it := range visible {
			if it.(historyItem).entry.Track.ID == keep {
				h.list.Select(i)
				return h
			}
		}
	}
	if len(visible) > 0 {
		if idx := h.list.Index(); idx >= len(visible) {
			h.list.Select(len(visible) - 1)
		}
	}
	return h
}

// setEntries 更新列表数据（root 在加载/删除/清空后调用）。数据刷新后重放过滤。
func (h historyModel) setEntries(entries []history.Entry) historyModel {
	h.entries = entries
	h.list.Title = fmt.Sprintf("最近播放 (%d/%d)", len(entries), history.MaxEntries)
	return h.applyFilter()
}

func (h historyModel) setSize(width, height int) historyModel {
	h.width, h.height = width, height
	h.list.SetSize(width, height-3)
	h.filterInput.Width = width - 14
	if h.filterInput.Width < 10 {
		h.filterInput.Width = 10
	}
	return h
}

// view 渲染历史页：过滤开启时顶部为过滤行，提示行随过滤状态切换；
// 空历史且未过滤时显示空态提示。
func (h historyModel) view() string {
	if h.filtering {
		hint := "输入过滤 · Enter 确认 · Esc 取消"
		if !h.filterInput.Focused() {
			hint = "Enter/p 重播 · d 删除 · c 清空 · a 添加到… · Esc 退出过滤"
		}
		count := fmt.Sprintf("(%d/%d)", len(h.list.VisibleItems()), len(h.entries))
		filterLine := "过滤: " + h.filterInput.View() + " " + lipgloss.NewStyle().Faint(true).Render(count)
		return bottomHint(h.height, filterLine+"\n"+h.list.View(), hint)
	}
	hint := "Enter/p 重播 · d 删除 · c 清空 · a 添加到…"
	if len(h.entries) == 0 {
		content := lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("暂无播放历史\n\n去搜索页播放一首歌吧")
		return bottomHint(h.height, content, hint)
	}
	return bottomHint(h.height, h.list.View(), hint)
}
```

- [ ] **Step 4: 运行确认通过**：`go test ./ui -run 'TestHistorySlashFilter' -v` → 2 个测试 PASS。
- [ ] **Step 5: 回归**：`go test ./ui` 全绿（重点 TestHistoryReplayDeleteClear、TestHistoryEmptyView、TestHistoryHintOnLastLine、TestHistoryEmptyHintOnLastLine）。
- [ ] **Step 6: Commit**：`git add ui/history.go ui/history_test.go && git commit -m "feat(ui): 历史页 / 过滤（实时过滤、Enter 确认、Esc 恢复、过滤态操作原始记录）"`（只 add 自己负责的文件）。

---

### Task 3: root 集成 + 全局回归（lead，两个 worker 完成后执行）

**Files:**
- Modify: `ui/root.go`（`typingText`）、`ui/root_test.go`

- [ ] `typingText` 增加两页分支（两页 `typing()` 已由 Task 1/2 提供）：

```go
// typingText 返回是否有输入框处于聚焦（搜索关键词/播放列表命名/队列历史过滤）：
// 聚焦时字符类全局键（空格/a/q）让位给输入框。
func (m Model) typingText() bool {
	switch m.current {
	case pageSearch:
		return m.searchPage.typing()
	case pagePlaylists:
		return m.plPage.typing()
	case pageQueue:
		return m.queuePage.typing()
	case pageHistory:
		return m.historyPage.typing()
	}
	return false
}
```

- [ ] `ui/root_test.go` 追加集成测试（断言全局键让位 + 数字键仍切页 + 过滤态跨页保持）：

```go
// TestGlobalKeysYieldToFilter 队列/历史过滤聚焦时：空格/a/q 让位给过滤输入框，
// 数字键仍切页，过滤态跨页切换保持。
func TestGlobalKeysYieldToFilter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.queue.Add(testTrack("t1"))
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})

	// 空格 → 过滤词而非播放/暂停
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.queuePage.filterInput.Value() != " " {
		t.Errorf("空格应输入过滤词, got %q", m.queuePage.filterInput.Value())
	}
	// a → 过滤词而非选择器
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker != nil {
		t.Fatal("过滤聚焦时 a 不应打开选择器")
	}
	// q → 过滤词而非退出
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatal("过滤聚焦时 q 不应退出")
		}
	}
	// 数字键仍切页（历史页），过滤态跨页保持
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	if m.current != pageHistory {
		t.Fatalf("数字 5 应切到历史页, got %v", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if !m.queuePage.filtering || !m.queuePage.filterInput.Focused() {
		t.Fatal("返回队列页后过滤态应保持")
	}
}

// TestQueueFilterHintOnLastLine 过滤态提示行应在内容区最后一行（两种状态）。
func TestQueueFilterHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.queue.Add(testTrack("t1"))
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	assertHintOnLastLine(t, m, "Enter 确认")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 确认
	assertHintOnLastLine(t, m, "Esc 退出过滤")
}
```

- [ ] `go test ./ui` 全绿 + `go test ./...` 全绿。
- [ ] Commit：`git add ui/root.go ui/root_test.go && git commit -m "feat(ui): 队列/历史过滤聚焦时全局键让位（空格/a/q），数字键仍切页"`

---

### Task 4: 全量验证（lead）

- [ ] `go build ./... && go vet ./... && go test ./... -race` 全绿
- [ ] 邀请 reviewer 审查（reviewer 会话）
- [ ] 修复 reviewer 发现的问题后，通知同一 reviewer 复审，直至通过
- [ ] 向用户汇报，请用户终端确认（过滤输入、结果数、Esc 恢复、过滤态操作）

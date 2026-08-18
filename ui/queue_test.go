package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// searchAndPick 走完 搜索 → 结果回灌 流程，返回就绪（列表聚焦）的 model。
// 用数字键 4 直达搜索页（Tab 需 ×3，数字键更稳）。
func searchAndPick(t *testing.T, m Model, fa *fakeSearchAdapter) Model {
	t.Helper()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}) // 直达搜索页
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var res searchResultsMsg
	for _, msg := range execSearchCmds(cmd) {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	if res.tracks == nil {
		t.Fatal("未收到搜索结果")
	}
	m, _ = update(m, res)
	return m
}

// openPickerAndAppendQueue 对当前列表选中项按 a 弹出"添加到"选择器 →
// Down 选中第二项"当前播放队列"（追加到队尾）→ Enter 回灌 trackAppendMsg。
// 可复用：搜索/历史/播放列表详情页选中歌曲后的追加流程。
func openPickerAndAppendQueue(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中第二项"当前播放队列"
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var ta trackAppendMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(trackAppendMsg); ok {
			ta = am
		}
	}
	if ta.track.ID == "" {
		t.Fatal("选择器队列项 Enter 未产生 trackAppendMsg")
	}
	m, _ = update(m, ta)
	return m
}

// TestQueueTabShowsEmptyView 第 2 个 Tab：空队列显示空态提示。
func TestQueueTabShowsEmptyView(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageQueue {
		t.Fatalf("Tab 后 current = %v, want pageQueue", m.current)
	}
	if got := m.queuePage.view(); !strings.Contains(got, "队列为空") {
		t.Errorf("空队列 view 应提示队列为空, got %q", got)
	}
	if m.queue.Len() != 0 {
		t.Errorf("初始队列 Len = %d, want 0", m.queue.Len())
	}
}

// TestSearchAppendBuildsQueue 搜索页 a 追加：不播放、不打断、无当前曲目。
func TestSearchAppendBuildsQueue(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa, nil)
	m = searchAndPick(t, m, fa)

	m = openPickerAndAppendQueue(t, m)              // 追加 t1
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 t2
	m = openPickerAndAppendQueue(t, m)
	if m.queue.Len() != 2 {
		t.Fatalf("queue.Len = %d, want 2", m.queue.Len())
	}
	if m.queue.CurrentIndex() != -1 {
		t.Errorf("仅追加不应有当前曲目, CurrentIndex = %d", m.queue.CurrentIndex())
	}
	if fp.playCount() != 0 {
		t.Errorf("追加不应触发播放, playCount = %d", fp.playCount())
	}
	if m.state.Track != nil {
		t.Errorf("追加不应改变播放状态, state.Track = %+v", m.state.Track)
	}
	// 队列页展示两条
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if len(m.queuePage.items) != 2 || m.queuePage.current != -1 {
		t.Errorf("队列页未同步: items=%d current=%d", len(m.queuePage.items), m.queuePage.current)
	}
}

// TestSearchEnterReplacesQueue 搜索页 Enter 替换语义：清空队列 → 入队 → 播放。
func TestSearchEnterReplacesQueue(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa, nil)
	m = searchAndPick(t, m, fa)

	// 先 a 追加 t1、t2 建立队列
	m = openPickerAndAppendQueue(t, m)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m = openPickerAndAppendQueue(t, m)
	if m.queue.Len() != 2 {
		t.Fatalf("前置追加失败: Len = %d", m.queue.Len())
	}

	// Enter 播放选中项（t2）→ 替换语义
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var sel trackSelectedMsg
	for _, msg := range execCmds(cmd) {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t2" {
		t.Fatalf("selected = %s, want t2", sel.track.ID)
	}
	m, _ = update(m, sel)
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != 0 {
		t.Fatalf("替换后 Len/CurrentIndex = %d/%d, want 1/0", m.queue.Len(), m.queue.CurrentIndex())
	}
	if cur, _ := m.queue.Current(); cur.ID != "t2" {
		t.Errorf("替换后当前曲 = %s, want t2", cur.ID)
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("play = %d %q, want 1 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
}

// TestAppendDoesNotInterruptPlayback a 追加不打断当前播放，首页显示队列位置。
func TestAppendDoesNotInterruptPlayback(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}

	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	if fp.playCount() != 1 {
		t.Errorf("追加不应触发播放, playCount = %d", fp.playCount())
	}
	if m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Errorf("追加不应改变当前曲, state.Track = %+v", m.state.Track)
	}
	if m.queue.Len() != 2 || m.queue.CurrentIndex() != 0 {
		t.Errorf("队列 = %d 条 current=%d, want 2 条 current=0", m.queue.Len(), m.queue.CurrentIndex())
	}
	if got := m.home.view(); !strings.Contains(got, "顺序  1/2") {
		t.Errorf("首页应显示 顺序  1/2, got %q", got)
	}
}

// TestTrackEndedAutoAdvances TrackEnded 自动连播：依次播放下一首，末首播完回绕队首；
// 空队列播完仍停止。
func TestTrackEndedAutoAdvances(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg) // 回灌 BatchMsg（历史写入等）
	}
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// t1 结束 → 自动连播 t2
	// 预推一个事件：TrackEnded 返回的 batch 含 waitForPlayerEvents（事件链），
	// 测试执行该 batch 时需有事件可消费（与 execRetryBatch 同款模式）。
	fp.events <- player.ProgressEvent{Position: 0, Duration: 200}
	m, cmd = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	msgs := execCmds(cmd)
	for _, msg := range msgs {
		m, _ = update(m, msg)
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("连播失败: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" || !m.state.Playing {
		t.Fatalf("连播后 state = %+v", m.state)
	}
	if m.ended {
		t.Error("连播后 ended 应为 false")
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("连播后 CurrentIndex = %d, want 1", m.queue.CurrentIndex())
	}
	if got := m.home.view(); !strings.Contains(got, "顺序  2/3") {
		t.Errorf("首页应显示 顺序  2/3, got %q", got)
	}
	// 自动连播写入历史
	if entries := m.history.Entries(); len(entries) != 2 {
		t.Errorf("连播曲目应入历史, entries = %d", len(entries))
	}

	// t2 结束 → t3
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 3 || fp.lastPlayed() != testTrack("t3").URL {
		t.Fatalf("第二次连播失败: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}

	// t3 结束 → 末尾回绕 → 自动连播队首 t1（列表循环语义）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 4 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("末首播完应回绕队首: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("回绕后 CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}
	if m.state.Track == nil || m.state.Track.ID != "t1" || !m.state.Playing || m.ended {
		t.Errorf("回绕后 state = %+v ended=%v, want t1 播放中 ended=false", m.state, m.ended)
	}
	if got := m.home.view(); !strings.Contains(got, "顺序  1/3") {
		t.Errorf("首页应显示 顺序  1/3, got %q", got)
	}

	// 队列清空后播完：无下一首 → 停止（ended 置位，不再播放）
	m, _ = update(m, queueClearMsg{})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 4 {
		t.Errorf("空队列播完不应再触发播放, playCount = %d", fp.playCount())
	}
	if m.state.Playing || !m.ended {
		t.Errorf("空队列播完后 Playing=%v ended=%v, want false/true", m.state.Playing, m.ended)
	}
}

// TestTrackEndedKeepsCurrentPage 自动连播不应把用户从当前页面拽走。
func TestTrackEndedKeepsCurrentPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.current != pageQueue {
		t.Fatalf("current = %v, want pageQueue", m.current)
	}
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.current != pageQueue {
		t.Errorf("自动连播不应切页, current = %v", m.current)
	}
	if m.queuePage.current != 1 {
		t.Errorf("队列页高亮应移到 t2, current = %d", m.queuePage.current)
	}
	if fp.playCount() != 2 {
		t.Errorf("playCount = %d, want 2", fp.playCount())
	}
}

// TestQueuePageJumpPlay 队列页 Enter 跳转语义：保留队列，播放所选曲目。
func TestQueuePageJumpPlay(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // t2
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // t3
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var qp queuePlayMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queuePlayMsg); ok {
			qp = qm
		}
	}
	if qp.index != 2 {
		t.Fatalf("queuePlayMsg.index = %d, want 2", qp.index)
	}
	m, _ = update(m, qp)
	if m.queue.Len() != 3 {
		t.Errorf("跳转不应清空队列, Len = %d, want 3", m.queue.Len())
	}
	if m.queue.CurrentIndex() != 2 {
		t.Errorf("跳转后 CurrentIndex = %d, want 2", m.queue.CurrentIndex())
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t3").URL {
		t.Errorf("play = %d %q, want 2 次 t3", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("跳转播放后应回首页, current = %v", m.current)
	}
	if m.state.Track == nil || m.state.Track.ID != "t3" || !m.state.Playing {
		t.Errorf("state = %+v, want t3 播放中", m.state)
	}
}

// TestQueuePageDeleteAndClear 队列页 d 删除（删当前曲顺延下一首）、c 清空。
func TestQueuePageDeleteAndClear(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	// 默认选中第一项（t1 = 当前曲）→ d 删除
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var qd queueDeleteMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queueDeleteMsg); ok {
			qd = qm
		}
	}
	if qd.index != 0 {
		t.Fatalf("queueDeleteMsg.index = %d, want 0", qd.index)
	}
	m, _ = update(m, qd)
	if m.queue.Len() != 2 {
		t.Fatalf("删除后 Len = %d, want 2", m.queue.Len())
	}
	if cur, _ := m.queue.Current(); cur.ID != "t2" {
		t.Errorf("删除当前曲后应顺延 t2, Current = %s", cur.ID)
	}
	if fp.playCount() != 1 {
		t.Errorf("删除不应触发播放, playCount = %d", fp.playCount())
	}
	if len(m.queuePage.items) != 2 || m.queuePage.current != 0 {
		t.Errorf("队列页未同步: items=%d current=%d", len(m.queuePage.items), m.queuePage.current)
	}

	// c 清空
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	var qc queueClearMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queueClearMsg); ok {
			qc = qm
		}
	}
	m, _ = update(m, qc)
	if m.queue.Len() != 0 || m.queue.CurrentIndex() != -1 {
		t.Errorf("清空后 Len/CurrentIndex = %d/%d, want 0/-1", m.queue.Len(), m.queue.CurrentIndex())
	}
	if got := m.queuePage.view(); !strings.Contains(got, "队列为空") {
		t.Errorf("清空后应显示空态, got %q", got)
	}
	if fp.playCount() != 1 {
		t.Errorf("清空不应影响当前播放, playCount = %d", fp.playCount())
	}
}

// TestQueueModeToggle 队列页 s 三态循环切换模式，首页/队列页文案同步显示。
func TestQueueModeToggle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	var qm queueModeMsg
	for _, msg := range execCmds(cmd) {
		if mm, ok := msg.(queueModeMsg); ok {
			qm = mm
		}
	}
	m, _ = update(m, qm)
	if m.queue.Mode() != queue.Shuffle {
		t.Fatalf("Mode = %v, want Shuffle", m.queue.Mode())
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("切模式不应移动当前指针, CurrentIndex = %d", m.queue.CurrentIndex())
	}
	if got := m.home.view(); !strings.Contains(got, "随机") {
		t.Errorf("首页应显示随机模式, got %q", got)
	}
	if !strings.Contains(m.queuePage.view(), "随机播放") {
		t.Errorf("队列页应显示随机播放, got %q", m.queuePage.view())
	}

	// 再按 s：Shuffle → RepeatOne
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	for _, msg := range execCmds(cmd) {
		if mm, ok := msg.(queueModeMsg); ok {
			m, _ = update(m, mm)
		}
	}
	if m.queue.Mode() != queue.RepeatOne {
		t.Errorf("Mode = %v, want RepeatOne", m.queue.Mode())
	}
	if got := m.home.view(); !strings.Contains(got, "单曲循环") {
		t.Errorf("首页应显示单曲循环, got %q", got)
	}
	if !strings.Contains(m.queuePage.view(), "单曲循环") {
		t.Errorf("队列页应显示单曲循环, got %q", m.queuePage.view())
	}

	// 再按 s：RepeatOne → Sequential
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	for _, msg := range execCmds(cmd) {
		if mm, ok := msg.(queueModeMsg); ok {
			m, _ = update(m, mm)
		}
	}
	if m.queue.Mode() != queue.Sequential {
		t.Errorf("Mode = %v, want Sequential", m.queue.Mode())
	}
	if got := m.home.view(); !strings.Contains(got, "顺序") {
		t.Errorf("首页应显示顺序模式, got %q", got)
	}
	if !strings.Contains(m.queuePage.view(), "列表循环") {
		t.Errorf("队列页应显示列表循环, got %q", m.queuePage.view())
	}
}

// TestModeLabels 三态文案：队列页完整名 + 首页短名。
func TestModeLabels(t *testing.T) {
	q := queueModel{mode: queue.Sequential}
	if got := q.modeLabel(); got != "列表循环" {
		t.Errorf("Sequential modeLabel = %q, want 列表循环", got)
	}
	q.mode = queue.Shuffle
	if got := q.modeLabel(); got != "随机播放" {
		t.Errorf("Shuffle modeLabel = %q, want 随机播放", got)
	}
	q.mode = queue.RepeatOne
	if got := q.modeLabel(); got != "单曲循环" {
		t.Errorf("RepeatOne modeLabel = %q, want 单曲循环", got)
	}
	if got := modeName(queue.Sequential); got != "顺序" {
		t.Errorf("Sequential modeName = %q, want 顺序", got)
	}
	if got := modeName(queue.Shuffle); got != "随机" {
		t.Errorf("Shuffle modeName = %q, want 随机", got)
	}
	if got := modeName(queue.RepeatOne); got != "单曲循环" {
		t.Errorf("RepeatOne modeName = %q, want 单曲循环", got)
	}
}

// TestHistoryAppend 历史页 a：弹出选择器 → 默认队列项 → 追加到队尾。
func TestHistoryAppend(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if err := m.history.Add(testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker == nil {
		t.Fatal("历史页按 a 应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中第二项"当前播放队列"（追加到队尾）
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var ta trackAppendMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(trackAppendMsg); ok {
			ta = am
		}
	}
	if ta.track.ID != "t1" {
		t.Fatalf("trackAppendMsg.track = %s, want t1", ta.track.ID)
	}
	m, _ = update(m, ta)
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != -1 {
		t.Errorf("追加后 Len/CurrentIndex = %d/%d, want 1/-1", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 0 {
		t.Errorf("历史页追加不应播放, playCount = %d", fp.playCount())
	}
}

// TestDeleteCurrentThenTrackEndedPlaysSlidTrack 回归：删除正在播放的当前曲后，
// 播完应播放顺延曲目（不跳过）：队列 [t1▶,t2,t3] 删 t1 → t1 播完 → 播 t2 → 播 t3。
func TestDeleteCurrentThenTrackEndedPlaysSlidTrack(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// 队列页删除当前曲 t1 → 顺延 t2 为当前（mpv 仍在播 t1，不打断）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var qd queueDeleteMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queueDeleteMsg); ok {
			qd = qm
		}
	}
	m, _ = update(m, qd)
	if cur, _ := m.queue.Current(); cur.ID != "t2" {
		t.Fatalf("删除当前曲后应顺延 t2, Current = %s", cur.ID)
	}

	// t1 播完 → 应播放顺延的 t2（而非跳过它直接播 t3）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("播完应接顺延曲目: playCount=%d lastPlayed=%q, want 2 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("顺延曲目播放后 CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" || !m.state.Playing {
		t.Errorf("state = %+v, want t2 播放中", m.state)
	}

	// t2 播完 → 正常推进 t3
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 3 || fp.lastPlayed() != testTrack("t3").URL {
		t.Errorf("顺延后应正常连播: playCount=%d lastPlayed=%q, want 3 次 t3", fp.playCount(), fp.lastPlayed())
	}
}

// TestDeleteLastCurrentThenTrackEndedPlaysFromHead 回归：删除末位当前曲（无顺延）后，
// 播完从头继续；首页显示 0/N（无当前曲）。
func TestDeleteLastCurrentThenTrackEndedPlaysFromHead(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// 推进到 t3（末位当前）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.queue.CurrentIndex() != 2 {
		t.Fatalf("CurrentIndex = %d, want 2", m.queue.CurrentIndex())
	}

	// 删除末位当前曲 t3 → 无当前曲目，首页显示 0/2
	m, _ = update(m, queueDeleteMsg{index: 2})
	if m.queue.CurrentIndex() != -1 {
		t.Fatalf("删除末位当前后 CurrentIndex = %d, want -1", m.queue.CurrentIndex())
	}
	if got := m.home.view(); !strings.Contains(got, "顺序  0/2") {
		t.Errorf("首页应显示 顺序  0/2, got %q", got)
	}

	// t3 播完 → 从头播 t1
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("无当前曲目时播完应从头: lastPlayed = %q, want t1", fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("从头后 CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}
}

// TestAutoAdvancePlayFailure 回归：自动连播 Play 失败 → 状态重置 + 错误横幅，不写历史。
func TestAutoAdvancePlayFailure(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg) // 回灌 BatchMsg（t1 入历史）
	}
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})

	fp.playErr = true
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 1 {
		t.Errorf("失败播放不应计入 playCount, got %d", fp.playCount())
	}
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("toast = %q, want 含播放失败", activeToastText(m))
	}
	if m.state.Track != nil || m.state.Playing {
		t.Errorf("失败后状态应重置: %+v", m.state)
	}
	if entries := m.history.Entries(); len(entries) != 1 {
		t.Errorf("失败连播不应写历史, entries = %d, want 1（仅 t1）", len(entries))
	}
}

// TestSpaceReplayKeepsQueue 回归（用户报告）：结束后空格重播 = 仅重载当前曲，
// 不得改动队列。此前 restartSameTrack 误走 startPlay 替换语义（清空队列 + 单曲
// 入队），网络失败 → 暂停 → 再次播放后整个队列被抹成只剩重播曲一首。
// 列表循环下仅空队列播完才置 ended，故先清空队列再触发播完。
func TestSpaceReplayKeepsQueue(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})

	// 清空队列后播完 → 无下一首 → 停止（ended）
	m, _ = update(m, queueClearMsg{})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if !m.ended || m.state.Playing {
		t.Fatalf("播完后 ended=%v Playing=%v, want true/false", m.ended, m.state.Playing)
	}

	// 追加 t3，空格重播 t1 → 仅重载当前曲，队列原样保留（[t3]、指针 -1 不动）
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("空格应重播同曲: playCount=%d lastPlayed=%q, want 2 次 t1", fp.playCount(), fp.lastPlayed())
	}
	// 重播不得改动队列：仍为追加的 [t3]，指针保持 -1（不重建队列）
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != -1 {
		t.Errorf("重播后队列 = %d 条 current=%d, want 保持 [t3] current=-1（重播不改队列）", m.queue.Len(), m.queue.CurrentIndex())
	}
	if snap := m.queue.Snapshot(); len(snap.Tracks) == 1 && snap.Tracks[0].ID != "t3" {
		t.Errorf("重播后队列内容 = %s, want t3（未被替换成重播曲）", snap.Tracks[0].ID)
	}
}

// TestQueueDeleteKeepsSelectionValid 回归：删除的正是选中项时，选择应
// clamp 到邻近项（不越界），Enter/d 不静默失效。
func TestQueueDeleteKeepsSelectionValid(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 t3（下标 2）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var qd queueDeleteMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queueDeleteMsg); ok {
			qd = qm
		}
	}
	m, _ = update(m, qd)

	if m.queuePage.list.SelectedItem() == nil {
		t.Fatal("删除选中项后光标越界，SelectedItem 为 nil")
	}
	if item, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || item.idx != 1 {
		t.Errorf("选择应 clamp 到下标 1（t2）, got %+v", item)
	}
	// Enter 仍能正常发出跳转消息
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var qp queuePlayMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queuePlayMsg); ok {
			qp = qm
		}
	}
	if qp.index != 1 {
		t.Errorf("删除后 Enter 应跳转到下标 1, got %d", qp.index)
	}
}

// TestQueueItemMarkers 队列项渲染：当前曲带 ▶ 标记与序号。
func TestQueueItemMarkers(t *testing.T) {
	item := queueItem{track: testTrack("t1"), idx: 0, current: true}
	if !strings.Contains(item.Title(), "▶") || !strings.Contains(item.Title(), "1.") {
		t.Errorf("当前曲 Title = %q, want 含 ▶ 与序号", item.Title())
	}
	item2 := queueItem{track: testTrack("t2"), idx: 1, current: false}
	if strings.Contains(item2.Title(), "▶") || !strings.Contains(item2.Title(), "2.") {
		t.Errorf("非当前曲 Title = %q, want 无 ▶ 但有序号", item2.Title())
	}
}

// ---- 工具 ----

func idsOf(ts []model.Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func sameTrackSet(a, b []model.Track) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, t := range a {
		seen[t.ID] = true
	}
	for _, t := range b {
		if !seen[t.ID] {
			return false
		}
	}
	return true
}

// sameIDs 顺序敏感比较曲目 ID 切片（移动模式断言队列顺序用）。
func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// moveMsgOf 从 cmd 结果中提取 queueMoveMsg（未产生则返回零值）。
func moveMsgOf(t *testing.T, cmd tea.Cmd) queueMoveMsg {
	t.Helper()
	var mv queueMoveMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queueMoveMsg); ok {
			mv = qm
		}
	}
	return mv
}

// TestQueueHintOnLastLine 队列页（非空）快捷键提示行渲染在页面内容区
// 最后一行（窗口最底行），hint 含 Enter 跳转播放。
func TestQueueHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	m.queue.Add(testTrack("t1"))
	m.queue.Add(testTrack("t2"))
	m.queuePage = m.queuePage.sync(m.queue)
	assertHintOnLastLine(t, m, "Enter/p 跳转播放")
}

// TestQueueHintOnLastLineManyItems 队列项多到触发列表分页行时提示行仍贴底。
func TestQueueHintOnLastLineManyItems(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	for i := 0; i < 10; i++ {
		m.queue.Add(testTrack(fmt.Sprintf("t%d", i)))
	}
	m.queuePage = m.queuePage.sync(m.queue)
	assertHintOnLastLine(t, m, "Enter/p 跳转播放")
}

// TestQueueEmptyHintOnLastLine 队列页空态也渲染提示行，且在最后一行。
func TestQueueEmptyHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	assertHintOnLastLine(t, m, "Enter/p 跳转播放")
}

// TestQueueCurrentItemShowsAITitle AI 识别结果到达后，队列页当前项
// （▶ 标记）显示清洗后标题；其他项保持原始；切歌后回落。
func TestQueueCurrentItemShowsAITitle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	// 直接构造两曲队列并播放第一首（startPlay 等价：Replace + beginPlay）
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.queue.Add(testTrack("t2"))
	m.queuePage = m.queuePage.sync(m.queue)

	got := m.queuePage.view()
	if !strings.Contains(got, "1. 测试歌曲 t1") {
		t.Fatalf("AI 到达前应显示原始标题, got %q", got)
	}
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly, title: "晴天", artist: "周杰伦"})
	// 不手动 sync：AI 结果 handler 已即时重建队列视图（生产路径）

	got = m.queuePage.view()
	if !strings.Contains(got, "1. 晴天") {
		t.Errorf("队列当前项应显示 AI 清洗标题（handler 即时刷新）, got %q", got)
	}
	if !strings.Contains(got, "2. 测试歌曲 t2") {
		t.Errorf("非当前项应保持原始标题, got %q", got)
	}
	if strings.Contains(got, "1. 测试歌曲 t1") {
		t.Errorf("当前项不应再显示原始标题: %q", got)
	}
}

// TestQueueSlashFilter 队列页 / 过滤全流程：打开→输入实时过滤→计数→
// Enter 确认→过滤态播放/删除（原始下标）→Esc 恢复。
// 注：数字 1-5 被 root 拦截用于切页（设计约束：过滤词无法含 1-5），
// 故用无数字关键词 "tb" 验证单选命中。
func TestQueueSlashFilter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"ta", "tb", "tc"} {
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

	// 输入 "tb" 实时过滤 + 计数（数字 1-5 不可入过滤词，用字母区分）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
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
		t.Errorf("删除后过滤列表应为空（剩余 ta/tc 不含 tb）")
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
	for _, id := range []string{"ta", "tb", "tc"} {
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
	// 已确认态再按 /：重新聚焦并保留关键词（本次按键不消费为字符）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.queuePage.filterInput.Focused() {
		t.Fatal("已确认态 / 应重新聚焦过滤输入框")
	}
	if m.queuePage.filterInput.Value() != "t" {
		t.Fatalf("重聚焦应保留关键词, got %q", m.queuePage.filterInput.Value())
	}
	// 聚焦态再按 / 才是输入字符
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if m.queuePage.filterInput.Value() != "t/" {
		t.Fatalf("聚焦态 / 应输入字符, got %q", m.queuePage.filterInput.Value())
	}
}

// TestQueueSlashFilterSyncReapplies 过滤态下 sync 重放过滤（删除后计数一致）。
func TestQueueSlashFilterSyncReapplies(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"ta", "tb", "tc"} {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	// 外部变化（如搜索页追加 u4）→ sync 后过滤仍生效（u4 不含关键词 t）
	m.queue.Add(testTrack("u4"))
	m = m.syncQueueViews()
	if n := len(m.queuePage.list.VisibleItems()); n != 3 {
		t.Fatalf("sync 后过滤列表 %d 项, want 3（u4 不含关键词）", n)
	}
	if got := m.queuePage.view(); !strings.Contains(got, "(3/4)") {
		t.Errorf("sync 后计数应 (3/4): %q", got)
	}
}

// TestQueueSlashFilterEscKeepsSelection Esc 退出过滤后选中项按曲目 ID 恢复
// （keep-found 路径：过滤前后选中项均可见）。
func TestQueueSlashFilterEscKeepsSelection(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"ta", "tb", "tc"} {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 tb（下标 1）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tb")}) // 仅 tb 可见
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "tb" {
		t.Fatalf("过滤后选中应保持 tb")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // 退出过滤
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "tb" {
		t.Fatalf("Esc 后选中应按 ID 恢复 tb")
	}
	if idx := m.queuePage.list.Index(); idx != 1 {
		t.Fatalf("Esc 后选中下标 = %d, want 1", idx)
	}
}

// TestQueueSlashFilterZeroHitsSafe 0 命中时操作键安全（不产生任何消息），Esc 恢复。
func TestQueueSlashFilterZeroHitsSafe(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.queue.Add(testTrack("t1"))
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}) // 0 命中
	if got := m.queuePage.view(); !strings.Contains(got, "(0/1)") {
		t.Fatalf("计数应 (0/1): %q", got)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 确认
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(execCmds(cmd)) != 0 {
		t.Fatal("0 命中时 Enter 不应产生消息")
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if len(execCmds(cmd)) != 0 {
		t.Fatal("0 命中时 d 不应产生消息")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if n := len(m.queuePage.list.VisibleItems()); n != 1 {
		t.Fatalf("Esc 后可见 %d 项, want 1", n)
	}
}

// TestQueueSlashFilterEmptyQueue 空队列 / 打开显示 (0/0)，Esc 正常退出。
func TestQueueSlashFilterEmptyQueue(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if got := m.queuePage.view(); !strings.Contains(got, "(0/0)") {
		t.Fatalf("空队列计数应 (0/0): %q", got)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queuePage.filtering {
		t.Fatal("Esc 应退出过滤")
	}
}

// ---- 移动模式 ----

// buildQueuePageModel 构造队列页模型（无当前曲目）：入队指定曲目并直达队列页。
func buildQueuePageModel(t *testing.T, ids ...string) Model {
	t.Helper()
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	for _, id := range ids {
		m.queue.Add(testTrack(id))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	return m
}

// TestQueueMoveEnterExit m 进入移动模式（view 切换移动 hint）；Enter/Esc 等效
// 结束且 Enter 不产生 queuePlayMsg；退出后 hint 恢复含 "m 移动"。
func TestQueueMoveEnterExit(t *testing.T) {
	m := buildQueuePageModel(t, "t1", "t2", "t3")

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !m.queuePage.moving {
		t.Fatal("按 m 应进入移动模式")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "↑↓←→/hjkl 移动") {
		t.Errorf("移动模式 view 应含移动 hint, got %q", got)
	}
	if got := m.queuePage.view(); !strings.Contains(got, "Enter/Esc 结束") {
		t.Errorf("移动模式 view 应含结束提示, got %q", got)
	}
	if got := m.queuePage.view(); !strings.Contains(got, m.queuePage.modeLabel()) {
		t.Errorf("移动模式 hint 应含模式名前缀, got %q", got)
	}

	// Enter 结束：等效 Esc，且不触发跳转播放
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.queuePage.moving {
		t.Fatal("Enter 应结束移动模式")
	}
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(queuePlayMsg); ok {
			t.Fatal("移动模式 Enter 不应触发跳转播放")
		}
	}
	if got := m.queuePage.view(); !strings.Contains(got, "m 移动") {
		t.Errorf("Enter 退出后 hint 应恢复含 m 移动, got %q", got)
	}

	// Esc 结束
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !m.queuePage.moving {
		t.Fatal("再次按 m 应进入移动模式")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queuePage.moving {
		t.Fatal("Esc 应结束移动模式")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "m 移动") {
		t.Errorf("Esc 退出后 hint 应恢复含 m 移动, got %q", got)
	}
}

// TestQueueMoveNotEnteredEmptyOrSingle 空队列/单曲队列按 m 不进入移动模式。
func TestQueueMoveNotEnteredEmptyOrSingle(t *testing.T) {
	m := buildQueuePageModel(t) // 空队列
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.queuePage.moving {
		t.Error("空队列按 m 不应进入移动模式")
	}

	m = buildQueuePageModel(t, "t1") // 单曲队列
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.queuePage.moving {
		t.Error("单曲队列按 m 不应进入移动模式")
	}
}

// TestQueueMoveKeys 移动键位全验证：↓/j 下移、↑/k 上移、←/h 队首、→/l 队尾，
// 每次回灌后队列顺序更新且选中项跟随被移动曲目（从新位置继续）。
func TestQueueMoveKeys(t *testing.T) {
	m := buildQueuePageModel(t, "t1", "t2", "t3")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})

	// ↓：t1(0) → 1，回灌后 [t2,t1,t3]，选中仍 t1
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyDown})
	mv := moveMsgOf(t, cmd)
	if mv.from != 0 || mv.to != 1 {
		t.Fatalf("↓ from/to = %d/%d, want 0/1", mv.from, mv.to)
	}
	m, _ = update(m, mv)
	if got := idsOf(m.queue.Tracks()); !sameIDs(got, []string{"t2", "t1", "t3"}) {
		t.Fatalf("↓ 后队列 = %v, want [t2 t1 t3]", got)
	}
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "t1" {
		t.Fatalf("↓ 后选中应跟随 t1, got %+v", it)
	}

	// ↓：t1(1) → 2（从新位置继续），回灌后 [t2,t3,t1]
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyDown})
	mv = moveMsgOf(t, cmd)
	if mv.from != 1 || mv.to != 2 {
		t.Fatalf("再次 ↓ from/to = %d/%d, want 1/2", mv.from, mv.to)
	}
	m, _ = update(m, mv)
	if got := idsOf(m.queue.Tracks()); !sameIDs(got, []string{"t2", "t3", "t1"}) {
		t.Fatalf("再次 ↓ 后队列 = %v, want [t2 t3 t1]", got)
	}

	// ↑：t1(2) → 1
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyUp})
	mv = moveMsgOf(t, cmd)
	if mv.from != 2 || mv.to != 1 {
		t.Fatalf("↑ from/to = %d/%d, want 2/1", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t2,t1,t3]

	// k：t1(1) → 0
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	mv = moveMsgOf(t, cmd)
	if mv.from != 1 || mv.to != 0 {
		t.Fatalf("k from/to = %d/%d, want 1/0", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t1,t2,t3]

	// j：t1(0) → 1
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	mv = moveMsgOf(t, cmd)
	if mv.from != 0 || mv.to != 1 {
		t.Fatalf("j from/to = %d/%d, want 0/1", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t2,t1,t3]

	// h：t1(1) → 队首 0
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	mv = moveMsgOf(t, cmd)
	if mv.from != 1 || mv.to != 0 {
		t.Fatalf("h from/to = %d/%d, want 1/0（队首）", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t1,t2,t3]

	// l：t1(0) → 队尾 len-1 = 2
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	mv = moveMsgOf(t, cmd)
	if mv.from != 0 || mv.to != 2 {
		t.Fatalf("l from/to = %d/%d, want 0/2（队尾）", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t2,t3,t1]
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "t1" || it.idx != 2 {
		t.Errorf("l 后选中应跟随 t1 到队尾, got %+v", it)
	}
}

// TestQueueMoveBoundaries 边界：队首 ↑/k/← 无消息；队尾 ↓/j/→ 无消息；
// 队列与 moving 状态不变。
func TestQueueMoveBoundaries(t *testing.T) {
	m := buildQueuePageModel(t, "t1", "t2", "t3")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})

	// 队首（t1 idx 0）：上移/队首均为边界
	for _, key := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyUp},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")},
		tea.KeyMsg{Type: tea.KeyLeft},
	} {
		_, cmd := update(m, key)
		if msgs := execCmds(cmd); len(msgs) != 0 {
			t.Errorf("队首 %v 不应产生消息, got %v", key, msgs)
		}
		if !m.queuePage.moving {
			t.Fatalf("队首 %v 不应退出移动模式", key)
		}
	}

	// l 移到队尾（t1 → idx 2）后测队尾边界
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRight})
	mv := moveMsgOf(t, cmd)
	if mv.from != 0 || mv.to != 2 {
		t.Fatalf("l from/to = %d/%d, want 0/2", mv.from, mv.to)
	}
	m, _ = update(m, mv) // [t2,t3,t1]

	for _, key := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRight},
	} {
		_, cmd := update(m, key)
		if msgs := execCmds(cmd); len(msgs) != 0 {
			t.Errorf("队尾 %v 不应产生消息, got %v", key, msgs)
		}
	}
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "t1" {
		t.Fatalf("边界按键后选中应仍为 t1, got %+v", it)
	}
	if got := idsOf(m.queue.Tracks()); !sameIDs(got, []string{"t2", "t3", "t1"}) {
		t.Fatalf("边界按键后队列应不变, got %v", got)
	}
}

// TestQueueMoveModal 移动模式模态：d/c/s/p// 一律忽略（无消息、moving 保持、
// 队列不变），移动键仍有效。
func TestQueueMoveModal(t *testing.T) {
	m := buildQueuePageModel(t, "t1", "t2", "t3")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})

	for _, key := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")},
	} {
		_, cmd := update(m, key)
		if msgs := execCmds(cmd); len(msgs) != 0 {
			t.Fatalf("移动模式 %v 不应产生消息, got %v", key, msgs)
		}
		if !m.queuePage.moving {
			t.Fatalf("移动模式 %v 不应退出, moving=%v", key, m.queuePage.moving)
		}
	}
	if got := idsOf(m.queue.Tracks()); !sameIDs(got, []string{"t1", "t2", "t3"}) {
		t.Fatalf("移动模式按键后队列应不变, got %v", got)
	}

	// 移动键仍有效
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyDown})
	mv := moveMsgOf(t, cmd)
	if mv.from != 0 || mv.to != 1 {
		t.Fatalf("模态后 ↓ from/to = %d/%d, want 0/1", mv.from, mv.to)
	}
}

// TestQueueMoveFilterInteraction 过滤交互：确认态按 m → 先退出过滤再进入移动
// 模式；输入框聚焦时按 m → m 是普通过滤字符（moving 仍 false）。
func TestQueueMoveFilterInteraction(t *testing.T) {
	m := buildQueuePageModel(t, "t1", "t2", "t3")
	// / 打开 → 输入 t → Enter 确认失焦（过滤确认态）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.queuePage.filtering || m.queuePage.filterInput.Focused() {
		t.Fatalf("前置过滤确认态失败: filtering=%v focused=%v", m.queuePage.filtering, m.queuePage.filterInput.Focused())
	}

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.queuePage.filtering {
		t.Error("确认态按 m 应先退出过滤")
	}
	if !m.queuePage.moving {
		t.Error("确认态按 m 应进入移动模式")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "↑↓←→/hjkl 移动") {
		t.Errorf("进入后应显示移动 hint, got %q", got)
	}

	// 聚焦态：m 是普通过滤字符
	m = buildQueuePageModel(t, "t1", "t2", "t3")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.queuePage.moving {
		t.Error("输入框聚焦时按 m 不应进入移动模式")
	}
	if !m.queuePage.filterInput.Focused() || m.queuePage.filterInput.Value() != "m" {
		t.Errorf("聚焦时 m 应输入过滤词, value=%q focused=%v", m.queuePage.filterInput.Value(), m.queuePage.filterInput.Focused())
	}
}

// TestQueueMoveGlobalKeysNotIntercepted 需求 5：移动模式不拦截 root 全局键。
// 全链路（startPlay 建当前曲 + trackAppendMsg 建队列 ≥2 首）进入移动模式后：
// 空格暂停/继续照常（mpv Pause/Resume 命令发出，StateEvent 回灌后
// m.state.Playing 翻转）；数字键 1→2 切页往返 moving 保持 true 且移动 hint
// 仍在；q 照常产生 tea.QuitMsg（先例：TestQuitOnQ/TestSpaceReplayKeepsQueue
// 均处理 Quit 消息）；Esc 正常退出。
func TestQueueMoveGlobalKeysNotIntercepted(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !m.queuePage.moving {
		t.Fatal("按 m 应进入移动模式")
	}

	// 空格：不退出移动模式，暂停命令照常发出（全局语义不被拦截）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if !m.queuePage.moving {
		t.Fatal("移动模式按空格不应退出移动模式")
	}
	if msgs := execCmds(cmd); fp.pauseCount() != 1 {
		t.Errorf("空格应触发暂停（Pause 命令已发出）, pauseCount = %d, msgs=%v", fp.pauseCount(), msgs)
	}
	m, _ = update(m, playerEventMsg{ev: player.StateEvent{Playing: false}}) // mpv 暂停状态回灌
	if m.state.Playing {
		t.Error("暂停事件回灌后 Playing 应为 false")
	}
	// 再按空格：继续播放，同样不退出移动模式
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if !m.queuePage.moving {
		t.Fatal("移动模式再按空格不应退出移动模式")
	}
	if msgs := execCmds(cmd); fp.resumeCount() != 1 {
		t.Errorf("再按空格应触发继续（Resume 命令已发出）, resumeCount = %d, msgs=%v", fp.resumeCount(), msgs)
	}
	m, _ = update(m, playerEventMsg{ev: player.StateEvent{Playing: true}})
	if !m.state.Playing {
		t.Error("继续事件回灌后 Playing 应为 true")
	}

	// 数字键切页：moving 状态随页面保留（1 首页 → 2 队列页）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.current != pageHome {
		t.Fatalf("按 1 应切到首页, current = %v", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.current != pageQueue {
		t.Fatalf("按 2 应切回队列页, current = %v", m.current)
	}
	if !m.queuePage.moving {
		t.Fatal("切页往返后 moving 应保持 true")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "↑↓←→/hjkl 移动") {
		t.Errorf("切页往返后仍应显示移动模式 hint, got %q", got)
	}

	// q：照常触发退出（先例：resume_test 对 Quit 消息有断言）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	var quit bool
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Error("移动模式按 q 应产生 Quit 消息")
	}
	if !m.queuePage.moving {
		t.Fatal("q 只触发退出消息，不应改变 moving 状态")
	}

	// Esc 正常退出移动模式
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.queuePage.moving {
		t.Fatal("Esc 应退出移动模式")
	}
	if got := m.queuePage.view(); !strings.Contains(got, "m 移动") {
		t.Errorf("退出后 hint 应恢复含 m 移动, got %q", got)
	}
}

// TestQueueMoveHintOnLastLine 移动模式/过滤确认态的提示行贴底回归：
// 队列 10 首（触发列表分页）进入移动模式后，移动 hint 替换普通 hint 且
// 恒在内容区最后一行（状态栏上方）；Esc 退出后过滤确认态 hint 含 m 移动
// 且同样贴底。
func TestQueueMoveHintOnLastLine(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 直达队列页
	for i := 0; i < 10; i++ {
		m.queue.Add(testTrack(fmt.Sprintf("t%d", i)))
	}
	m = m.syncQueueViews()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !m.queuePage.moving {
		t.Fatal("按 m 应进入移动模式")
	}
	assertHintOnLastLine(t, m, "↑↓←→/hjkl 移动")
	if got := m.queuePage.view(); strings.Contains(got, "跳转播放") {
		t.Errorf("移动模式 hint 应替换普通 hint（不应含跳转播放）, got %q", got)
	}

	// Esc 退出 → 过滤确认态（/ → 输入词 → Enter 失焦）：hint 含 m 移动且贴底
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.queuePage.filterInput.Focused() || !m.queuePage.filtering {
		t.Fatalf("过滤确认态前置失败: filtering=%v focused=%v", m.queuePage.filtering, m.queuePage.filterInput.Focused())
	}
	assertHintOnLastLine(t, m, "m 移动")
}

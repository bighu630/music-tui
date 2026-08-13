package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// searchAndPick 走完 搜索 → 结果回灌 流程，返回就绪（列表聚焦）的 model。
func searchAndPick(t *testing.T, m Model, fa *fakeSearchAdapter) Model {
	t.Helper()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // 切到搜索页
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var res searchResultsMsg
	for _, msg := range execCmds(cmd) {
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

// appendSelected 对当前列表选中项按 a（追加到队尾），并回灌 trackAppendMsg。
func appendSelected(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	var ta trackAppendMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(trackAppendMsg); ok {
			ta = am
		}
	}
	if ta.track.ID == "" {
		t.Fatal("按 a 未产生 trackAppendMsg")
	}
	m, _ = update(m, ta)
	return m
}

// TestQueueTabShowsEmptyView 第 4 个 Tab：空队列显示空态提示。
func TestQueueTabShowsEmptyView(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	}
	if m.current != pageQueue {
		t.Fatalf("Tab×3 后 current = %v, want pageQueue", m.current)
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
	m := newTestModel(t, fp, fa)
	m = searchAndPick(t, m, fa)

	m = appendSelected(t, m) // 追加 t1
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 t2
	m = appendSelected(t, m)
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if len(m.queuePage.items) != 2 || m.queuePage.current != -1 {
		t.Errorf("队列页未同步: items=%d current=%d", len(m.queuePage.items), m.queuePage.current)
	}
}

// TestSearchEnterReplacesQueue 搜索页 Enter 替换语义：清空队列 → 入队 → 播放。
func TestSearchEnterReplacesQueue(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa)
	m = searchAndPick(t, m, fa)

	// 先 a 追加 t1、t2 建立队列
	m = appendSelected(t, m)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m = appendSelected(t, m)
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	if got := m.home.view(); !strings.Contains(got, "1/2 · 顺序") {
		t.Errorf("首页应显示 1/2 · 顺序, got %q", got)
	}
}

// TestTrackEndedAutoAdvances TrackEnded 自动连播：依次播放下一首，播完停止。
func TestTrackEndedAutoAdvances(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg) // 回灌 BatchMsg（历史写入等）
	}
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// t1 结束 → 自动连播 t2
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
	if got := m.home.view(); !strings.Contains(got, "2/3 · 顺序") {
		t.Errorf("首页应显示 2/3 · 顺序, got %q", got)
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

	// t3 结束 → 无下一首 → 停止（不循环）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 3 {
		t.Errorf("播完不应再触发播放, playCount = %d", fp.playCount())
	}
	if m.state.Playing || !m.ended {
		t.Errorf("播完后 Playing=%v ended=%v, want false/true", m.state.Playing, m.ended)
	}
	if m.queue.CurrentIndex() != 2 {
		t.Errorf("播完停在末位, CurrentIndex = %d", m.queue.CurrentIndex())
	}
}

// TestTrackEndedKeepsCurrentPage 自动连播不应把用户从当前页面拽走。
func TestTrackEndedKeepsCurrentPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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

// TestQueueModeToggle 队列页 s 切换顺序/随机，首页同步显示模式。
func TestQueueModeToggle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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
		t.Errorf("切随机不应移动当前指针, CurrentIndex = %d", m.queue.CurrentIndex())
	}
	// 洗牌后显示顺序 = 播放顺序（集合不变）
	if !sameTrackSet(m.queue.Tracks(), []model.Track{testTrack("t1"), testTrack("t2"), testTrack("t3")}) {
		t.Errorf("洗牌后曲目集合变了: %+v", idsOf(m.queue.Tracks()))
	}
	if got := m.home.view(); !strings.Contains(got, "随机") {
		t.Errorf("首页应显示随机模式, got %q", got)
	}
	if !strings.Contains(m.queuePage.view(), "随机播放") {
		t.Errorf("队列页应显示随机播放, got %q", m.queuePage.view())
	}

	// 再按 s 切回顺序
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
}

// TestHistoryAppend 历史页 a 追加到队尾。
func TestHistoryAppend(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	if err := m.history.Add(testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// 队列页删除当前曲 t1 → 顺延 t2 为当前（mpv 仍在播 t1，不打断）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	if got := m.home.view(); !strings.Contains(got, "0/2 · 顺序") {
		t.Errorf("首页应显示 0/2 · 顺序, got %q", got)
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	if !strings.Contains(m.lastError, "播放失败") {
		t.Errorf("lastError = %q, want 含播放失败", m.lastError)
	}
	if m.state.Track != nil || m.state.Playing {
		t.Errorf("失败后状态应重置: %+v", m.state)
	}
	if entries := m.history.Entries(); len(entries) != 1 {
		t.Errorf("失败连播不应写历史, entries = %d, want 1（仅 t1）", len(entries))
	}
}

// TestSpaceReplayReplacesQueue 锁定既定行为：结束后空格重播走替换语义（清空队列）。
func TestSpaceReplayReplacesQueue(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})

	// t1 播完 → 连播 t2；t2 播完 → 无下一首 → 停止（ended）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if !m.ended || m.state.Playing {
		t.Fatalf("播完后 ended=%v Playing=%v, want true/false", m.ended, m.state.Playing)
	}

	// 追加 t3，空格重播 t2 → 替换语义清空队列
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if fp.playCount() != 3 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("空格应重播同曲: playCount=%d lastPlayed=%q, want 3 次 t2", fp.playCount(), fp.lastPlayed())
	}
	// 设计已确认：手动播放统一替换语义 → 队列只剩重播曲目
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != 0 {
		t.Errorf("重播后队列 = %d 条 current=%d, want 1 条 current=0（替换语义）", m.queue.Len(), m.queue.CurrentIndex())
	}
}

// TestQueueDeleteKeepsSelectionValid 回归：删除的正是选中项时，选择应
// clamp 到邻近项（不越界），Enter/d 不静默失效。
func TestQueueDeleteKeepsSelectionValid(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
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

package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
	"music-tui/session"
)

// ---- fakes ----

type fakePlayer struct {
	mu           sync.Mutex
	plays        []string  // Play 调用记录
	paused       []string  // PlayPaused 调用记录（续播恢复）
	pausedStarts []float64 // PlayPaused 的 start 参数记录（恢复起点）
	pauses       int
	resumes      int
	seeks        []float64
	loops        []bool // SetLoop 调用记录
	playErr      bool   // 为 true 时 Play 返回错误（测试注入）
	loopErr      bool   // 为 true 时 SetLoop 返回错误（测试注入）
	events       chan player.Event
}

func newFakePlayer() *fakePlayer {
	return &fakePlayer{events: make(chan player.Event, 64)}
}

func (f *fakePlayer) Play(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.playErr {
		return context.DeadlineExceeded
	}
	f.plays = append(f.plays, url)
	return nil
}

func (f *fakePlayer) PlayPaused(url string, start float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.playErr {
		return context.DeadlineExceeded
	}
	f.paused = append(f.paused, url)
	f.pausedStarts = append(f.pausedStarts, start)
	return nil
}

func (f *fakePlayer) Pause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses++
	return nil
}

func (f *fakePlayer) Resume() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	return nil
}

func (f *fakePlayer) Seek(seconds float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeks = append(f.seeks, seconds)
	return nil
}

func (f *fakePlayer) SetLoop(loop bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loopErr {
		return errors.New("loop 设置失败")
	}
	f.loops = append(f.loops, loop)
	return nil
}

func (f *fakePlayer) Events() <-chan player.Event { return f.events }

func (f *fakePlayer) pauseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauses
}

func (f *fakePlayer) resumeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resumes
}

// playCount 返回累计播放次数（计划代码补充：TestPlayFlow 依赖）。
func (f *fakePlayer) playCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plays)
}

// pausedCount 返回 PlayPaused 调用次数（续播恢复测试）。
func (f *fakePlayer) pausedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paused)
}

// pausedStart 返回最近一次 PlayPaused 的 start 参数（无调用返回 0）。
func (f *fakePlayer) pausedStart() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pausedStarts) == 0 {
		return 0
	}
	return f.pausedStarts[len(f.pausedStarts)-1]
}

func (f *fakePlayer) lastPaused() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paused) == 0 {
		return ""
	}
	return f.paused[len(f.paused)-1]
}

func (f *fakePlayer) lastPlayed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.plays) == 0 {
		return ""
	}
	return f.plays[len(f.plays)-1]
}

// lastLoop 返回最近一次 SetLoop 的参数及是否有调用（无调用返回 false, false）。
func (f *fakePlayer) lastLoop() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.loops) == 0 {
		return false, false
	}
	return f.loops[len(f.loops)-1], true
}

type fakeSearchAdapter struct {
	mu     sync.Mutex
	calls  int
	tracks []model.Track
	err    error
}

func (f *fakeSearchAdapter) Search(ctx context.Context, query string) ([]model.Track, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.tracks, nil
}

// ---- 测试工具 ----

func testTrack(id string) model.Track {
	return model.Track{
		ID:       id,
		Title:    "测试歌曲 " + id,
		Artist:   "测试歌手",
		Duration: 200,
		URL:      "https://www.youtube.com/watch?v=" + id,
		Source:   "youtube",
		CoverURL: "",
	}
}

// newTestModel 组装真实服务（历史/封面用临时目录，歌词指向 404 的假服务器）。
// onTrack 透传给 NewModel（MPRIS 曲目回调；测试可传 nil 或自定收集）。
func newTestModel(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, onTrack func(*model.Track)) Model {
	t.Helper()
	lyricServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(lyricServer.Close)

	hist, err := history.NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	cf, err := cover.NewFetcher(filepath.Join(t.TempDir(), "covers"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	return NewModel(fp, fa,
		lyrics.NewClientWithBaseURL(lyricServer.URL, "music-tui test (https://example.com)"),
		cf, hist, sess, onTrack)
}

// execCmds 同步执行 tea.Cmd 并收集返回的非 nil 消息（测试用）。
func execCmds(cmds ...tea.Cmd) []tea.Msg {
	var msgs []tea.Msg
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if msg := c(); msg != nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// update 调用 root Update 并把返回的 tea.Model 断言回 Model
// （Model.Update 的签名必须是 (tea.Model, tea.Cmd)，测试需要类型断言）。
func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	tm, cmd := m.Update(msg)
	return tm.(Model), cmd
}

// runProgram 运行一个真实 tea.Program：先启动 Run 再顺序 Send 消息
// （Send 会阻塞直到事件循环消费，天然同步），最后 Send(tea.Quit)，
// 返回最终 Model。bubbletea 对输入 EOF 不会自动退出，可安全使用。
func runProgram(t *testing.T, m Model, sends ...tea.Msg) Model {
	t.Helper()
	p := tea.NewProgram(m, tea.WithInput(strings.NewReader("")), tea.WithoutRenderer())
	type result struct {
		model tea.Model
		err   error
	}
	resCh := make(chan result, 1)
	go func() {
		tm, err := p.Run()
		resCh <- result{tm, err}
	}()
	for _, s := range sends {
		p.Send(s)
	}
	p.Send(tea.Quit()) // tea.Quit 是 Cmd（func() Msg），Send 需传 QuitMsg（v1.3.10）
	res := <-resCh
	if res.err != nil {
		t.Fatalf("program: %v", res.err)
	}
	return res.model.(Model)
}

// waitFor 轮询等待条件成立，超时报错（用于异步 cmd 的副作用断言）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within", timeout)
}

// ---- Program 集成测试 ----

func TestTabSwitchesPages(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)

	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageSearch {
		t.Errorf("Tab 后 current = %v, want pageSearch", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.current != pageHistory {
		t.Errorf("按 3 后 current = %v, want pageHistory", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if m.current != pageQueue {
		t.Errorf("按 4 后 current = %v, want pageQueue", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.current != pageHome {
		t.Errorf("按 1 后 current = %v, want pageHome", m.current)
	}
}

func TestSpaceTogglesPlayback(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	tr := testTrack("t1")
	m.state = model.PlaybackState{Track: &tr, Playing: true}
	m.home = m.home.syncState(m.state)

	runProgram(t, m, tea.KeyMsg{Type: tea.KeySpace})
	waitFor(t, 2*time.Second, func() bool { return fp.pauseCount() == 1 })

	m.state.Playing = false
	m.home = m.home.syncState(m.state)
	runProgram(t, m, tea.KeyMsg{Type: tea.KeySpace})
	waitFor(t, 2*time.Second, func() bool { return fp.resumeCount() == 1 })
}

func TestSpaceTypesWhenSearchInputFocused(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m = runProgram(t, m,
		tea.KeyMsg{Type: tea.KeyTab}, // 切到搜索页，输入框聚焦
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")},
		tea.KeyMsg{Type: tea.KeySpace},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("杰倫")},
	)
	if got := m.searchPage.input.Value(); got != "晴天 杰倫" {
		t.Errorf("input = %q, want %q", got, "晴天 杰倫")
	}
	if fp.pauseCount() != 0 {
		t.Errorf("输入框中空格不应触发暂停，pauseCount = %d", fp.pauseCount())
	}
}

func TestQuitOnQ(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	p := tea.NewProgram(m, tea.WithInput(strings.NewReader("")), tea.WithoutRenderer())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Run(); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	select {
	case <-done:
		// 按 q 后程序正常退出
	case <-time.After(3 * time.Second):
		t.Fatal("按 q 后程序未退出")
	}
}

// ---- Update 驱动流程测试 ----

func TestPlayFlow(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa, nil)

	// 搜索页输入关键词并 Enter
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageSearch {
		t.Fatal("Tab 后应在搜索页")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.searchPage.state != searchLoading {
		t.Fatalf("state = %v, want searchLoading", m.searchPage.state)
	}
	msgs := execCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone || len(m.searchPage.results) != 2 {
		t.Fatalf("搜索后 state = %v, results = %d", m.searchPage.state, len(m.searchPage.results))
	}

	// Enter 播放第一项
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs = execCmds(cmd)
	var sel trackSelectedMsg
	for _, msg := range msgs {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	m, cmd = update(m, sel)
	// startPlay 触发四个并行 cmd：播放 / 歌词 / 封面 / 历史
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("play 调用错误: %d %q", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Fatalf("current = %v, want pageHome", m.current)
	}
	if m.state.Track == nil || m.state.Track.ID != "t1" || !m.state.Playing {
		t.Fatalf("state = %+v", m.state)
	}
	if m.home.lyricsState != lyricsLoading {
		t.Fatalf("歌词初始态 = %v, want lyricsLoading", m.home.lyricsState)
	}
	// 回灌四个结果消息（歌词/封面均失败 → 不阻塞播放）
	msgs = execCmds(cmd)
	for _, msg := range msgs {
		m, _ = update(m, msg)
	}
	if entries := m.history.Entries(); len(entries) != 1 || entries[0].Track.ID != "t1" {
		t.Fatalf("历史 = %+v", entries)
	}
	if m.home.lyricsState != lyricsNone {
		t.Fatalf("歌词失败后 state = %v, want lyricsNone", m.home.lyricsState)
	}
	if !m.home.coverFallback {
		t.Error("封面失败后应显示占位框")
	}

	// 同步歌词到达 → 高亮随进度推进
	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	if m.home.lyricsState != lyricsSynced {
		t.Fatalf("state = %v, want lyricsSynced", m.home.lyricsState)
	}
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 25, Duration: 200}})
	if m.home.currentLine != 1 {
		t.Fatalf("currentLine = %d, want 1", m.home.currentLine)
	}

	// 播放结束 → 队列回绕自动连播同曲（startPlay 替换语义下队列仅 t1 一首，
	// 列表循环播完回绕重播自身）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if !m.state.Playing || m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Fatalf("TrackEndedEvent 后应回绕重播 t1, state = %+v", m.state)
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("回绕连播失败: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}

	// 重播同一首 → 历史去重仍 1 条
	m, cmd = m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if entries := m.history.Entries(); len(entries) != 1 {
		t.Fatalf("重播去重失败: %+v", entries)
	}
}

func TestStaleAsyncResultsIgnored(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 过期 trackID 的歌词/封面结果应被丢弃
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]x"))
	m, _ = update(m, lyricsResultMsg{trackID: "stale", lyrics: ly})
	if m.home.lyricsState == lyricsSynced {
		t.Error("过期歌词结果不应被应用")
	}
	m, _ = update(m, coverResultMsg{trackID: "stale", path: "/tmp/x.jpg"})
	if m.home.coverWidget != nil {
		t.Error("过期封面结果不应被应用")
	}
}

func TestPlayFailureShowsError(t *testing.T) {
	fp := newFakePlayer()
	fp.playErr = true
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	if cmd != nil {
		t.Error("播放失败后不应再发异步 cmd（歌词/封面/历史）")
	}
	if !strings.Contains(m.lastError, "播放失败") {
		t.Errorf("lastError = %q, want 含“播放失败”", m.lastError)
	}
	if m.state.Playing {
		t.Error("播放失败后 Playing 应为 false")
	}
	if m.state.Track != nil {
		t.Error("播放失败后 state.Track 应为 nil（回到未播放空态）")
	}
	if m.home.state.Track != nil {
		t.Error("播放失败后 home 应回到未在播放空态")
	}
	if entries := m.history.Entries(); len(entries) != 0 {
		t.Errorf("播放失败不应写入历史，entries = %d 条", len(entries))
	}
	if got := m.home.view(); !strings.Contains(got, "未在播放") {
		t.Errorf("home.view 应显示未在播放，got %q", got)
	}
	// 失败后空格应被忽略（无 Track 可重播，也不走暂停/继续）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil || fp.playCount() != 0 {
		t.Error("播放失败后空格应被忽略且不触发任何播放操作")
	}
}

// TrackStartedEvent 携带 Duration=0（observe 与 Get 兜底均失败，如直播/
// 特殊流）时不应覆盖搜索元数据提供的真实时长，避免进度条被抹零。
func TestTrackStartedEventZeroDurationKeepsMetadata(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1")) // 搜索元数据 Duration=200
	_ = execCmds(cmd)
	if m.state.Duration != 200 {
		t.Fatalf("startPlay 后 Duration = %v, want 200", m.state.Duration)
	}

	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 0}})
	if m.state.Duration != 200 {
		t.Errorf("Duration=0 的 TrackStartedEvent 不应覆盖已有时长: got %v, want 200", m.state.Duration)
	}

	// 非零时长仍正常覆盖
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 217}})
	if m.state.Duration != 217 {
		t.Errorf("Duration=217 的 TrackStartedEvent 应覆盖: got %v", m.state.Duration)
	}
}

func TestErrorEventSetsEndedAndSpaceReplays(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}

	m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: errors.New("mpv 崩溃")}})
	if !strings.Contains(m.lastError, "mpv 崩溃") {
		t.Errorf("lastError = %q, want 含 mpv 崩溃", m.lastError)
	}
	if m.state.Playing {
		t.Error("ErrorEvent 后 Playing 应为 false")
	}
	if !m.ended {
		t.Error("ErrorEvent 后 ended 应为 true")
	}

	// 出错后空格 = 重播同曲（而非 Resume）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("空格应重播同曲: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if fp.resumeCount() != 0 {
		t.Errorf("出错后空格不应走 Resume，resumeCount=%d", fp.resumeCount())
	}
	if m.ended || !m.state.Playing {
		t.Errorf("重播后 ended=%v Playing=%v, want false/true", m.ended, m.state.Playing)
	}
}

func TestSpaceAfterTrackEndedReplaysSameTrack(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}

	// 队列清空后播完：无下一首 → 停止（ended 置位；列表循环下仅空队列停止）
	m, _ = update(m, queueClearMsg{})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.state.Playing || !m.ended {
		t.Fatalf("TrackEnded 后 Playing=%v ended=%v, want false/true", m.state.Playing, m.ended)
	}
	if fp.playCount() != 1 {
		t.Fatalf("空队列播完不应再触发播放, playCount=%d", fp.playCount())
	}

	// 结束态空格 → 重播同曲（Track 仍在，走 startPlay 而非 Resume）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("空格应重播同曲: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if fp.resumeCount() != 0 {
		t.Errorf("结束后空格不应走 Resume，resumeCount=%d", fp.resumeCount())
	}
	if !m.state.Playing || m.ended {
		t.Errorf("重播后 state=%+v ended=%v, want Playing=true ended=false", m.state, m.ended)
	}
}

// 首页上一首/下一首消息：queue.Prev/Next + beginPlay；手动操作重置重试预算
// 并解除删除解耦标记；空队列忽略（不 panic、无命令）。
func TestPrevNextTrackMessages(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// nextTrackMsg：t1 → t2
	m, _ = update(m, nextTrackMsg{})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("下一首: playCount=%d lastPlayed=%q, want 2 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("下一首后 CurrentIndex = %d, want 1", m.queue.CurrentIndex())
	}
	if !m.state.Playing || m.ended {
		t.Errorf("下一首后 state=%+v ended=%v, want Playing=true ended=false", m.state, m.ended)
	}

	// nextTrackMsg 末位回绕：t3 → t1
	m, _ = update(m, nextTrackMsg{})
	m, _ = update(m, nextTrackMsg{})
	if fp.playCount() != 4 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("下一首回绕: playCount=%d lastPlayed=%q, want 4 次 t1", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("回绕后 CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}

	// prevTrackMsg：队首回绕到末尾 t3
	m, _ = update(m, prevTrackMsg{})
	if fp.playCount() != 5 || fp.lastPlayed() != testTrack("t3").URL {
		t.Fatalf("上一首回绕: playCount=%d lastPlayed=%q, want 5 次 t3", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 2 {
		t.Errorf("上一首后 CurrentIndex = %d, want 2", m.queue.CurrentIndex())
	}

	// prevTrackMsg 正常回退：t3 → t2
	m, _ = update(m, prevTrackMsg{})
	if fp.playCount() != 6 || fp.lastPlayed() != testTrack("t2").URL || m.queue.CurrentIndex() != 1 {
		t.Errorf("上一首回退: playCount=%d lastPlayed=%q CurrentIndex=%d, want 6 次 t2/1", fp.playCount(), fp.lastPlayed(), m.queue.CurrentIndex())
	}

	// 手动上一首/下一首重置重试预算并解除解耦标记
	m.retryCount = 1
	m.queueSkip = true
	m, _ = update(m, nextTrackMsg{})
	if m.retryCount != 0 || m.queueSkip {
		t.Errorf("手动切歌应重置 retryCount=%d queueSkip=%v", m.retryCount, m.queueSkip)
	}
	if fp.lastPlayed() != testTrack("t3").URL {
		t.Errorf("重置检查的下一首应到 t3: %q", fp.lastPlayed())
	}

	// 空队列：prev/next 均忽略
	m2 := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, cmd = update(m2, prevTrackMsg{})
	if cmd != nil {
		t.Errorf("空队列 prev 应无命令: %v", cmd)
	}
	m2, cmd = update(m2, nextTrackMsg{})
	if cmd != nil {
		t.Errorf("空队列 next 应无命令: %v", cmd)
	}
}

// 模式按钮消息 toggleModeMsg 三态循环：Sequential→Shuffle→RepeatOne→Sequential；
// 切入 RepeatOne 时 SetLoop(true)，切出时 SetLoop(false)。
func TestToggleModeCycles(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// Sequential → Shuffle：SetLoop(false)
	m, _ = update(m, toggleModeMsg{})
	if m.queue.Mode() != queue.Shuffle {
		t.Fatalf("Mode = %v, want Shuffle", m.queue.Mode())
	}
	if lp, ok := fp.lastLoop(); !ok || lp {
		t.Errorf("Shuffle 下 SetLoop = %v/%v, want false", lp, ok)
	}

	// Shuffle → RepeatOne：SetLoop(true)
	m, _ = update(m, toggleModeMsg{})
	if m.queue.Mode() != queue.RepeatOne {
		t.Fatalf("Mode = %v, want RepeatOne", m.queue.Mode())
	}
	if lp, ok := fp.lastLoop(); !ok || !lp {
		t.Errorf("RepeatOne 下 SetLoop = %v/%v, want true", lp, ok)
	}

	// RepeatOne → Sequential：SetLoop(false)
	m, _ = update(m, toggleModeMsg{})
	if m.queue.Mode() != queue.Sequential {
		t.Fatalf("Mode = %v, want Sequential", m.queue.Mode())
	}
	if lp, ok := fp.lastLoop(); !ok || lp {
		t.Errorf("切出 RepeatOne 后 SetLoop = %v/%v, want false", lp, ok)
	}

	// 再按一次：Sequential → Shuffle（循环回到第二位）
	m, _ = update(m, toggleModeMsg{})
	if m.queue.Mode() != queue.Shuffle {
		t.Errorf("Mode = %v, want Shuffle（四连按回到 Shuffle）", m.queue.Mode())
	}
	// 模式切换不打断当前播放
	if !m.state.Playing || m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Errorf("模式切换不应打断播放: %+v", m.state)
	}
	if fp.playCount() != 1 {
		t.Errorf("模式切换不应触发播放: playCount = %d", fp.playCount())
	}
}

// beginPlay 按模式设置 SetLoop：RepeatOne → true，Sequential/Shuffle → false；
// RepeatOne 下手动下一首新曲仍按模式循环。
func TestBeginPlaySetsLoopPerMode(t *testing.T) {
	fp := newFakePlayer()

	// Sequential：SetLoop(false)
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if lp, ok := fp.lastLoop(); !ok || lp {
		t.Errorf("Sequential beginPlay 后 SetLoop = %v/%v, want false", lp, ok)
	}

	// RepeatOne：SetLoop(true)（Replace 保留模式）
	m.queue.SetMode(queue.RepeatOne)
	m, cmd = m.startPlay(testTrack("t2"))
	_ = execCmds(cmd)
	if lp, ok := fp.lastLoop(); !ok || !lp {
		t.Errorf("RepeatOne beginPlay 后 SetLoop = %v/%v, want true", lp, ok)
	}

	// RepeatOne 下手动下一首：新曲仍按模式 SetLoop(true)
	m, _ = update(m, nextTrackMsg{})
	if lp, ok := fp.lastLoop(); !ok || !lp {
		t.Errorf("RepeatOne 切歌后 SetLoop = %v/%v, want true", lp, ok)
	}

	// Shuffle：SetLoop(false)
	m.queue.SetMode(queue.Shuffle)
	m, cmd = m.startPlay(testTrack("t3"))
	_ = execCmds(cmd)
	if lp, ok := fp.lastLoop(); !ok || lp {
		t.Errorf("Shuffle beginPlay 后 SetLoop = %v/%v, want false", lp, ok)
	}
}

// SetLoop 失败仅记 lastError，不阻断播放（异步 cmd 照常、状态照常）。
func TestSetLoopFailureDoesNotBlockPlayback(t *testing.T) {
	fp := newFakePlayer()
	fp.loopErr = true
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.queue.SetMode(queue.RepeatOne)
	m, cmd := m.startPlay(testTrack("t1"))
	if cmd == nil {
		t.Fatal("SetLoop 失败不应阻断播放：应仍有异步 cmd（歌词/封面/历史）")
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("SetLoop 失败不应影响播放: playCount=%d", fp.playCount())
	}
	if !strings.Contains(m.lastError, "循环") {
		t.Errorf("lastError = %q, want 含循环失败信息", m.lastError)
	}
	if !m.state.Playing || m.state.Track == nil {
		t.Errorf("SetLoop 失败后应保持播放态: %+v", m.state)
	}
}

// ---- onTrack 回调（MPRIS 曲目桥） ----

// startPlay 成功时应在 Play 返回后同步通知新曲目（回调收到 track 指针拷贝）。
func TestStartPlayNotifiesTrack(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	var got []*model.Track
	m := newTestModel(t, fp, fa, func(track *model.Track) {
		cp := *track
		got = append(got, &cp)
	})

	tr := model.Track{ID: "v1", Title: "T1", Artist: "A", URL: "http://x/1"}
	m, cmd := m.startPlay(tr)
	_ = cmd // 回调在 startPlay 内同步触发，无需执行异步 cmd
	_ = m
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("startPlay 应通知新曲目: %#v", got)
	}
}

// startPlay 失败时（Play 返回错误）应通知 nil（当前无曲目）。
func TestStartPlayFailureClearsTrack(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	var got []*model.Track
	m := newTestModel(t, fp, fa, func(track *model.Track) {
		got = append(got, track) // 失败路径传 nil
	})
	fp.playErr = true

	tr := model.Track{ID: "v1", Title: "T1", URL: "http://x/1"}
	m, cmd := m.startPlay(tr)
	_ = cmd // 失败路径无异步 cmd
	_ = m
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("播放失败应通知 nil: %#v", got)
	}
}

func TestTabWrapsAround(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if m.current != pageHome {
		t.Fatalf("初始 current = %v, want pageHome", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // home → search
	if m.current != pageSearch {
		t.Fatalf("current = %v, want pageSearch", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // search → history
	if m.current != pageHistory {
		t.Fatalf("current = %v, want pageHistory", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // history → queue
	if m.current != pageQueue {
		t.Fatalf("current = %v, want pageQueue", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // queue → home（循环 wrap）
	if m.current != pageHome {
		t.Errorf("tab 循环后 current = %v, want pageHome", m.current)
	}
}

// ---------------------------------------------------------------------------
// 取流失败自动重试（YouTube 403 风控等瞬态错误：重试=重新 loadfile=重新取流）
// ---------------------------------------------------------------------------

// 执行重试 batch：cmd 是 tea.Batch(waitForPlayerEvents, retryPlayCmd)。
// waitForPlayerEvents 阻塞在播放器事件通道，测试需先预推一个普通事件让它
// 立即返回，随后 retryPlayCmd 在 retryBackoff 后发出 retryPlayMsg，
// 由 update 的 tea.BatchMsg 分支回灌（与现有测试驱动方式一致）。
func execRetryBatch(m Model, cmd tea.Cmd, fp *fakePlayer) Model {
	fp.events <- player.ProgressEvent{Position: 0, Duration: 200}
	m2, _ := update(m, cmd().(tea.BatchMsg))
	return m2
}

// 取流失败在重试预算内：自动重试重新 loadfile，成功（TrackStartedEvent）
// 后恢复播放状态并重置预算。
func TestLoadFailRetriesThenSucceeds(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("初始 playCount = %d, want 1", fp.playCount())
	}

	// 第 1 次取流失败：调度自动重试，等待期间暂停态 + 横幅提示
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	if cmd == nil {
		t.Fatal("取流失败后应调度重试 cmd")
	}
	if m.state.Playing {
		t.Error("重试等待期间 Playing 应为 false")
	}
	if !strings.Contains(m.lastError, "正在自动重试（1/2）") {
		t.Errorf("lastError = %q, want 含“正在自动重试（1/2）”", m.lastError)
	}

	// 重试触发：重新 loadfile（playCount=2），恢复播放态
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 2 {
		t.Fatalf("重试后 playCount = %d, want 2", fp.playCount())
	}
	if fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("重试应重载同一曲目: %q", fp.lastPlayed())
	}
	if !m.state.Playing {
		t.Error("重试 loadfile 后 Playing 应为 true")
	}

	// 重试成功（file-loaded）：重试预算重置
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.retryCount != 0 {
		t.Errorf("加载成功后 retryCount = %d, want 0", m.retryCount)
	}
}

// 队列连播时某曲重试耗尽：跳过失败曲目，继续播放下一首；
// 横幅保留告知用户哪首失败（不中断整个连播）。
func TestLoadFailRetriesExhaustedSkipsInQueue(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.queue.Add(testTrack("t2")) // 队列：[t1, t2]

	loadErr := player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
	// 前两次失败各触发一次自动重试（重载第 1 首）
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	if fp.playCount() != 3 {
		t.Fatalf("两次重试后 playCount = %d, want 3", fp.playCount())
	}
	if fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("重试应仍重载第 1 首: %q", fp.lastPlayed())
	}

	// 第 3 次失败：重试耗尽 → 跳过第 1 首，播放第 2 首
	m, _ = update(m, playerEventMsg{ev: loadErr})
	if fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("重试耗尽应播放第 2 首: got %q, want %q", fp.lastPlayed(), testTrack("t2").URL)
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" {
		t.Errorf("state.Track = %+v, want t2", m.state.Track)
	}
	if !m.state.Playing {
		t.Error("跳过并播放下一首后 Playing 应为 true")
	}
	if !strings.Contains(m.lastError, "跳过") || !strings.Contains(m.lastError, "测试歌曲 t1") {
		t.Errorf("lastError = %q, want 含“跳过”与第 1 首标题", m.lastError)
	}
}

// 单曲（无下一首）重试耗尽：停止播放，不再重试，横幅提示手动重试。
func TestLoadFailRetriesExhaustedStopsSingle(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	loadErr := player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	if fp.playCount() != 3 {
		t.Fatalf("两次重试后 playCount = %d, want 3", fp.playCount())
	}

	// 第 3 次失败：重试耗尽且队列无下一首 → 停止，不再调度任何播放
	m, cmd = update(m, playerEventMsg{ev: loadErr})
	if fp.playCount() != 3 {
		t.Errorf("重试耗尽后不应再 Play: playCount = %d, want 3", fp.playCount())
	}
	if m.state.Playing {
		t.Error("重试耗尽停止后 Playing 应为 false")
	}
	if !m.ended {
		t.Error("重试耗尽停止后 ended 应为 true")
	}
	if !strings.Contains(m.lastError, "已重试 2 次") || !strings.Contains(m.lastError, "请稍后重试或更换歌曲") {
		t.Errorf("lastError = %q, want 含“已重试 2 次”与“请稍后重试或更换歌曲”", m.lastError)
	}
}

// 重试等待期间用户手动换曲：代际不匹配，过期重试必须丢弃
// （不得对旧曲重复播放）。
func TestStaleRetryDroppedOnNewPlay(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 第 1 首失败：调度重试（携带当前代际）
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	if cmd == nil {
		t.Fatal("应调度重试 cmd")
	}

	// 重试触发前用户手动播放另一首（代际递增）
	m, _ = m.startPlay(testTrack("t2"))
	if fp.playCount() != 2 {
		t.Fatalf("换歌后 playCount = %d, want 2", fp.playCount())
	}

	// 执行旧 batch（含过期重试）：必须被丢弃，不得再播放旧曲
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 2 {
		t.Errorf("过期重试不应触发播放: playCount = %d, want 2", fp.playCount())
	}
	if fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("播放的应是新歌 t2: %q", fp.lastPlayed())
	}
	if !m.state.Playing {
		t.Error("换歌后 Playing 应为 true")
	}
}

// 回归（P1）：重试等待期间删除当前曲（queueSkip=true、指针顺延）→ 重试触发时
// 必须清除 queueSkip（重试与队列当前状态重新对齐），否则残留标记会让
// TrackEnded 重复播放顺延曲目：队列 [t1,t2,t3]，t1 失败重试挂起 → 删 t1
// → 重试播放 t2 → t2 播完 TrackEnded 若走 queueSkip 分支会重播 t2（Current），
// 正确行为是正常推进 t3。
func TestRetryPlayClearsQueueSkip(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")}) // 队列 [t1, t2, t3]

	// t1 取流失败：调度自动重试（代际匹配）
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	if cmd == nil {
		t.Fatal("应调度重试 cmd")
	}

	// 重试等待期间删除当前曲 t1 → queueSkip=true，指针顺延到 t2
	m, _ = update(m, queueDeleteMsg{index: m.queue.CurrentIndex()})
	if !m.queueSkip {
		t.Fatal("删除当前曲后应置 queueSkip")
	}
	if cur, _ := m.queue.Current(); cur.ID != "t2" {
		t.Fatalf("删除后当前曲应为 t2, got %s", cur.ID)
	}

	// 重试触发：播放顺延曲目 t2，并清除 queueSkip（与队列当前状态重新对齐）
	m = execRetryBatch(m, cmd, fp)
	if m.queueSkip {
		t.Error("重试播放后应清除 queueSkip")
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("重试应播放顺延曲目 t2: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}

	// t2 播完：queueSkip 已清除 → 正常推进 t3；若残留会走解耦分支重播 t2
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 3 || fp.lastPlayed() != testTrack("t3").URL {
		t.Errorf("t2 播完应推进 t3: playCount=%d lastPlayed=%q, want 3 次 t3", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("推进后 CurrentIndex = %d, want 1", m.queue.CurrentIndex())
	}
}

// 回归（P1 分支镜像）：重试耗尽跳过时若存在删除解耦标记（queueSkip=true、
// 指针已顺延），应播放顺延曲目（Current 兜底）而非 Next()——不跳过头
// （镜像 TrackEnded 的解耦逻辑）。场景：t1 两次重试均失败后删除 t1
// （最后一次重试加载期间），第 3 次失败耗尽 → 应接 t2 而非 t3。
func TestLoadFailExhaustedSkipRespectsQueueSkip(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	loadErr := player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
	// 前两次失败各触发一次自动重试（重载 t1），重试均正常执行
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	if fp.playCount() != 3 {
		t.Fatalf("两次重试后 playCount = %d, want 3", fp.playCount())
	}

	// 最后一次重试加载期间删除当前曲 t1 → queueSkip=true，指针顺延 t2
	m, _ = update(m, queueDeleteMsg{index: m.queue.CurrentIndex()})
	if !m.queueSkip {
		t.Fatal("删除当前曲后应置 queueSkip")
	}

	// 第 3 次失败：重试耗尽且存在解耦标记 → 播放顺延曲目 t2（Current 兜底）
	// 而非 Next() 的 t3——不跳过头
	m, _ = update(m, playerEventMsg{ev: loadErr})
	if fp.playCount() != 4 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("耗尽跳过应播放顺延曲目 t2: playCount=%d lastPlayed=%q, want 4 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.queueSkip {
		t.Error("耗尽跳过后应清除 queueSkip")
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" || !m.state.Playing {
		t.Errorf("state = %+v, want t2 播放中", m.state)
	}
	if !strings.Contains(m.lastError, "跳过") || !strings.Contains(m.lastError, "测试歌曲 t1") {
		t.Errorf("lastError = %q, want 含“跳过”与 t1 标题", m.lastError)
	}
}

// 回归（P3）：重试等待期间队列被清空 → 重试触发时无曲可播，不得 panic，
// 停止重试并给出合理横幅（不能悬挂“正在自动重试”）。
func TestRetryOnClearedQueueStops(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// t1 取流失败 → 重试等待期间队列被清空
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	m, _ = update(m, queueClearMsg{})
	if m.queue.Len() != 0 {
		t.Fatalf("清空后队列 Len = %d, want 0", m.queue.Len())
	}

	// 重试触发：队列已空 → 停止重试，不 panic，横幅合理
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 1 {
		t.Errorf("队列清空后重试不应播放: playCount = %d, want 1", fp.playCount())
	}
	if !strings.Contains(m.lastError, "队列已清空") {
		t.Errorf("lastError = %q, want 含队列已清空", m.lastError)
	}
	if m.state.Playing {
		t.Error("停止后 Playing 应为 false")
	}
}

// 回归（P2）：续播恢复（PlayPaused 静默加载 + Seek 定位）的 IPC 成功只代表
// 命令被接受，mpv 异步取流失败（end-file error → LoadFailedError）随后才到。
// 此时不得自动重试（重试会走 beginPlay→Play()：发声、从 0:00、非暂停，静默
// 丢弃恢复语义），应保留“恢复播放失败”语义（清空内存队列 + 横幅带 hint 诊断，
// 磁盘会话保留下次启动重试）；恢复上下文作废后取流失败恢复正常自动重试。
func TestResumeLoadFailNoAutoRetry(t *testing.T) {
	m, fp := newResumeTestModel(t, sessionState(66.6, false), nil)
	if !m.resuming {
		t.Fatal("恢复场景应置 resuming 标记")
	}

	// PlayPaused（含 start= 原子定位，不再单独 Seek）IPC 成功 → resumeResultMsg
	// 成功（resuming 保持，等待 mpv 异步加载结果：TrackStartedEvent 或 end-file error）
	msgs := execCmds(resumeCmd(m))
	m, cmd := update(m, msgs[0])
	_ = execCmds(cmd) // 歌词/封面加载结果与本测试无关
	if fp.pausedCount() != 1 || len(fp.seeks) != 0 || fp.pausedStart() != 66.6 {
		t.Fatalf("恢复应 PlayPaused(url, 66.6): paused=%d seeks=%v start=%v", fp.pausedCount(), fp.seeks, fp.pausedStart())
	}

	// 实际取流失败（end-file error）：不得自动重试，保留恢复失败语义
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "403 Forbidden"}}})
	if fp.playCount() != 0 {
		t.Errorf("恢复加载失败不应调用 Play: %d", fp.playCount())
	}
	// cmd 非 nil = waitForPlayerEvents 监听链存活（回归：resuming 分支曾返回 nil，
	// 事件监听永久失聪、播放状态机冻结）。该 cmd 阻塞在播放器事件通道，测试不执行。
	if cmd == nil {
		t.Fatal("恢复加载失败后事件监听链应存活（cmd 应为 waitForPlayerEvents，非 nil）")
	}
	if !strings.Contains(m.lastError, "恢复播放失败") || !strings.Contains(m.lastError, "风控") {
		t.Errorf("lastError = %q, want 含“恢复播放失败”与 hint 诊断（风控）", m.lastError)
	}
	if m.queue.Len() != 3 {
		t.Errorf("失败后队列应保留展示: Len = %d, want 3", m.queue.Len())
	}
	if m.state.Track != nil || m.state.Playing {
		t.Errorf("失败后状态应重置: %+v", m.state)
	}
	if m.resuming {
		t.Error("失败处理后 resuming 应复位")
	}

	// 恢复上下文已作废：手动播放新曲后，取流失败恢复正常自动重试
	m, cmd = m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("手动播放后 playCount = %d, want 1", fp.playCount())
	}
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	if cmd == nil {
		t.Fatal("正常播放取流失败应调度自动重试 cmd")
	}
	if !strings.Contains(m.lastError, "正在自动重试（1/2）") {
		t.Errorf("lastError = %q, want 含“正在自动重试（1/2）”", m.lastError)
	}
}

// loadFailureHint 把 mpv file_error 诊断文本映射为可操作的中文提示。
func TestLoadFailHint(t *testing.T) {
	cases := []struct {
		fileErr string
		want    string
	}{
		{"no audio or video data played", "YouTube 未返回可播放音轨"},
		{"403 Forbidden", "YouTube 拒绝访问"},
		{"Couldn't resolve host name", "网络解析失败"},
		{"This video is unavailable", "视频不可用"},
		{"", "mpv 无法播放该地址"},
		{"some weird error", "播放出错：some weird error"},
	}
	for _, tc := range cases {
		if got := loadFailureHint(tc.fileErr); !strings.Contains(got, tc.want) {
			t.Errorf("loadFailureHint(%q) = %q, want 含 %q", tc.fileErr, got, tc.want)
		}
	}
}

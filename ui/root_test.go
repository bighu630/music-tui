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
)

// ---- fakes ----

type fakePlayer struct {
	mu      sync.Mutex
	plays   []string
	pauses  int
	resumes int
	seeks   []float64
	playErr bool // 为 true 时 Play 返回错误（测试注入）
	events  chan player.Event
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

func (f *fakePlayer) lastPlayed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.plays) == 0 {
		return ""
	}
	return f.plays[len(f.plays)-1]
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
func newTestModel(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter) Model {
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
	return NewModel(fp, fa,
		lyrics.NewClientWithBaseURL(lyricServer.URL, "music-tui test (https://example.com)"),
		cf, hist)
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
	m := newTestModel(t, fp, fa)

	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageSearch {
		t.Errorf("Tab 后 current = %v, want pageSearch", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.current != pageHistory {
		t.Errorf("按 3 后 current = %v, want pageHistory", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.current != pageHome {
		t.Errorf("按 1 后 current = %v, want pageHome", m.current)
	}
}

func TestSpaceTogglesPlayback(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, fa)

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

	// 播放结束 → Playing 复位
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.state.Playing {
		t.Fatal("TrackEndedEvent 后 Playing 应为 false")
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m := newTestModel(t, fp, &fakeSearchAdapter{})
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}

	// 播放结束：Playing 复位、ended 置位
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if m.state.Playing || !m.ended {
		t.Fatalf("TrackEnded 后 Playing=%v ended=%v, want false/true", m.state.Playing, m.ended)
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

func TestTabWrapsAround(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{})
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // history → home（循环 wrap）
	if m.current != pageHome {
		t.Errorf("tab 循环后 current = %v, want pageHome", m.current)
	}
}

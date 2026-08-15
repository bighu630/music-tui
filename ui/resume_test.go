package ui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/cache"
	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/queue"
	"music-tui/session"
	"music-tui/ytm"
)

// newResumeTestModel 组装带指定会话状态的测试 model（会话文件已写入 st）。
func newResumeTestModel(t *testing.T, st *session.State, onTrack func(*model.Track)) (Model, *fakePlayer) {
	t.Helper()
	fp := newFakePlayer()
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
	if st != nil {
		if err := sess.Save(*st); err != nil {
			t.Fatal(err)
		}
	}
	pls, err := playlists.NewStore(filepath.Join(t.TempDir(), "playlists.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 缓存指向不存在的 yt-dlp：恢复路径不会触发 CacheAsync，即使触发
	// 后台下载也立即失败退出（无网络无泄漏）。
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, "/nonexistent/yt-dlp")
	ytStore, err := ytm.NewStore(filepath.Join(t.TempDir(), "ytm.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(fp, &fakeSearchAdapter{},
		lyrics.NewClientWithBaseURL(lyricServer.URL, "music-tui test (https://example.com)"),
		cf, hist, sess, pls, cm, ytm.NewClient(ytStore, &fakeYTFetcher{}), onTrack)
	return m, fp
}

// sessionState 构造含 3 首曲目、当前第 2 首（b）的会话状态。
func sessionState(pos float64, ended bool) *session.State {
	q := queue.New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前（1）
	q.SetMode(queue.Shuffle)
	st := session.State{Queue: q.Snapshot(), Position: pos, Ended: ended}
	return &st
}

// TestResumeRestoresQueueAndState 重启恢复：队列/模式/当前曲/进度恢复为暂停态，
// Init 触发 PlayPaused 静默加载并定位（随 loadfile start= 原子完成，不再单独
// Seek），成功后加载歌词/封面。
func TestResumeRestoresQueueAndState(t *testing.T) {
	m, fp := newResumeTestModel(t, sessionState(66.6, false), nil)

	// NewModel 同步恢复的状态
	if m.queue.Len() != 3 || m.queue.CurrentIndex() != 1 || m.queue.Mode() != queue.Shuffle {
		t.Fatalf("队列恢复失败: Len=%d current=%d mode=%v", m.queue.Len(), m.queue.CurrentIndex(), m.queue.Mode())
	}
	if m.state.Track == nil || m.state.Track.ID != "b" {
		t.Fatalf("state.Track = %+v, want b", m.state.Track)
	}
	if m.state.Position != 66.6 || m.state.Playing {
		t.Errorf("state = %+v, want position=66.6 playing=false（暂停态）", m.state)
	}
	if m.home.state.Position != 66.6 {
		t.Errorf("home 进度未恢复: %v", m.home.state.Position)
	}
	if m.home.lyricsState != lyricsLoading {
		t.Errorf("恢复后歌词应为加载中, got %v", m.home.lyricsState)
	}
	if m.resume == nil || m.resume.track.ID != "b" || m.resume.pos != 66.6 {
		t.Errorf("resume 信息 = %+v", m.resume)
	}

	// Init → resumeCmd：PlayPaused（含定位）（waitForPlayerEvents 阻塞通道，单独执行 resumeCmd）
	msgs := execCmds(resumeCmd(m))
	if fp.pausedCount() != 1 || fp.lastPaused() != testTrack("b").URL {
		t.Errorf("PlayPaused 调用 = %d %q, want 1 次 b", fp.pausedCount(), fp.lastPaused())
	}
	if len(fp.seeks) != 0 {
		t.Errorf("恢复不应再调用 Seek（定位随 loadfile start= 原子完成）: %v", fp.seeks)
	}
	if fp.pausedStart() != 66.6 {
		t.Errorf("PlayPaused start = %v, want 66.6", fp.pausedStart())
	}

	// resumeResultMsg 回灌 → 触发歌词/封面加载
	var resumed bool
	for _, msg := range msgs {
		if _, ok := msg.(resumeResultMsg); ok {
			resumed = true
		}
	}
	if !resumed {
		t.Fatal("未收到 resumeResultMsg")
	}
	m, cmd := update(m, msgs[0])
	if fp.playCount() != 0 {
		t.Errorf("恢复不应调用 Play（应 PlayPaused）: %d", fp.playCount())
	}
	// 歌词/封面 cmd 触发
	sub := execCmds(cmd)
	if len(sub) == 0 {
		t.Error("恢复成功应触发歌词/封面加载 cmd")
	}
	// 回灌歌词/封面结果不崩溃
	for _, msg := range sub {
		m, _ = update(m, msg)
	}
	if m.home.lyricsState != lyricsNone {
		t.Errorf("歌词 404 后应为 lyricsNone, got %v", m.home.lyricsState)
	}
}

// TestResumeEndedAdvancesToNext 退出时已播完且有下一首 → 恢复从下一首开头（暂停）。
func TestResumeEndedAdvancesToNext(t *testing.T) {
	m, fp := newResumeTestModel(t, sessionState(180, true), nil)
	if m.state.Track == nil || m.state.Track.ID != "c" {
		t.Fatalf("ended 恢复应跳到下一首 c, got %+v", m.state.Track)
	}
	if m.state.Position != 0 || m.state.Playing {
		t.Errorf("state = %+v, want position=0 playing=false", m.state)
	}
	if m.queue.CurrentIndex() != 2 {
		t.Errorf("CurrentIndex = %d, want 2", m.queue.CurrentIndex())
	}
	msgs := execCmds(resumeCmd(m))
	if fp.lastPaused() != testTrack("c").URL {
		t.Errorf("PlayPaused = %q, want c", fp.lastPaused())
	}
	if len(fp.seeks) != 0 {
		t.Errorf("恢复不应再调用 Seek（定位随 loadfile start= 原子完成）: %v", fp.seeks)
	}
	if fp.pausedStart() != 0 {
		t.Errorf("PlayPaused start = %v, want 0（从头加载）", fp.pausedStart())
	}
	m, _ = update(m, msgs[0])
	if m.state.Track == nil || m.state.Track.ID != "c" {
		t.Errorf("恢复结果后 state = %+v", m.state)
	}
}

// TestResumeEndedSingleTrackRestartsCurrent 退出时已播完且无下一首 → 当前曲从头。
func TestResumeEndedSingleTrackRestartsCurrent(t *testing.T) {
	q := queue.New()
	q.Replace(testTrack("a"))
	st := session.State{Queue: q.Snapshot(), Position: 180, Ended: true}
	m, fp := newResumeTestModel(t, &st, nil)
	if m.state.Track == nil || m.state.Track.ID != "a" || m.state.Position != 0 {
		t.Fatalf("单曲 ended 应从头恢复: %+v pos=%v", m.state.Track, m.state.Position)
	}
	msgs := execCmds(resumeCmd(m))
	if fp.lastPaused() != testTrack("a").URL {
		t.Errorf("PlayPaused = %q, want a", fp.lastPaused())
	}
	if len(fp.seeks) != 0 {
		t.Errorf("恢复不应再调用 Seek（定位随 loadfile start= 原子完成）: %v", fp.seeks)
	}
	if fp.pausedStart() != 0 {
		t.Errorf("PlayPaused start = %v, want 0（从头加载）", fp.pausedStart())
	}
	m, _ = update(m, msgs[0])
	if m.state.Playing {
		t.Error("恢复后应保持暂停")
	}
}

// TestResumeSuccessSetsLoopPerMode 续播恢复成功后按当前模式补 SetLoop
// （审查 Minor 4）：beginPlay 路径有显式 SetLoop，但恢复路径（PlayPaused
// 静默加载）此前漏设——单曲循环模式下恢复会丢失 mpv loop-file 语义。
// fakePlayer.lastLoop 可断言恢复成功后 SetLoop 的值与模式一致。
func TestResumeSuccessSetsLoopPerMode(t *testing.T) {
	cases := []struct {
		name string
		mode queue.Mode
		want bool
	}{
		{"sequential", queue.Sequential, false},
		{"shuffle", queue.Shuffle, false},
		{"repeatone", queue.RepeatOne, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := newResumeTestModel(t, sessionState(10, false), nil)
			m.queue.SetMode(tc.mode)
			msgs := execCmds(resumeCmd(m))
			if len(msgs) == 0 {
				t.Fatal("resumeCmd 未返回 resumeResultMsg")
			}
			m, _ = update(m, msgs[0])
			loop, ok := fp.lastLoop()
			if !ok {
				t.Fatal("恢复成功后应调用 SetLoop")
			}
			if loop != tc.want {
				t.Errorf("恢复成功后 lastLoop = %v, want %v（模式 %v）", loop, tc.want, tc.mode)
			}
		})
	}
}

// TestResumeFailureResets 恢复加载失败 → 状态重置 + 队列清空 + 错误横幅。
func TestResumeFailureResets(t *testing.T) {
	m, fp := newResumeTestModel(t, sessionState(66.6, false), nil)
	fp.playErr = true
	msgs := execCmds(resumeCmd(m))
	m, _ = update(m, msgs[0])
	if !strings.Contains(activeToastText(m), "恢复播放失败") {
		t.Errorf("toast = %q, want 含恢复播放失败", activeToastText(m))
	}
	if m.state.Track != nil || m.state.Playing {
		t.Errorf("失败后状态应重置: %+v", m.state)
	}
	if m.queue.Len() != 3 {
		t.Errorf("失败后队列应保留展示（不播但可见）: Len = %d, want 3", m.queue.Len())
	}
	if got := m.home.view(); !strings.Contains(got, "未在播放") {
		t.Errorf("home 应回到未播放空态: %q", got)
	}
}

// TestResumeNotifiesMPRIS 恢复的曲目应同步给外部消费者（onTrack 回调）。
func TestResumeNotifiesMPRIS(t *testing.T) {
	var got *model.Track
	m, _ := newResumeTestModel(t, sessionState(10, false), func(track *model.Track) {
		got = track
	})
	if got == nil || got.ID != "b" {
		t.Errorf("onTrack 应收到恢复曲目 b, got %+v", got)
	}
	_ = m
}

// TestResumeFirstProgressEventDoesNotOverwriteDisk 回归：恢复启动后 loadfile 会
// 触发 time-pos=0 的 ProgressEvent（先于 Seek 定位到达），不应触发节流保存
// 覆盖磁盘上的恢复进度（否则崩溃恢复退化为从 0 开始）。
func TestResumeFirstProgressEventDoesNotOverwriteDisk(t *testing.T) {
	m, _ := newResumeTestModel(t, sessionState(66.6, false), nil)
	if m.lastSave.IsZero() {
		t.Fatal("恢复时应预置节流基准（lastSave），否则首个进度事件会立即保存")
	}

	// 模拟 loadfile 后的 time-pos=0 事件（先于 Seek 的 66.6）
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 0, Duration: 200}})
	if st := m.session.State(); st.Position != 66.6 {
		t.Errorf("恢复后首个进度事件不应覆盖磁盘会话: Position = %v, want 66.6", st.Position)
	}
}

// TestSaveOnQuitWritesSession 播放中按 q 退出 → 会话写入（队列 + 进度 + ended）。
func TestSaveOnQuitWritesSession(t *testing.T) {
	m, _ := newResumeTestModel(t, nil, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 42, Duration: 200}})

	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	// 退出 cmd 应包含 Quit
	var quit bool
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Error("q 应产生 Quit 消息")
	}
	// 会话文件已写入（内存态即磁盘态，session 包单测已验证写盘 roundtrip）
	st := m.session.State()
	if st == nil {
		t.Fatal("退出后会话应为非空")
	}
	if st.Position != 42 || st.Ended {
		t.Errorf("会话 = %+v, want position=42 ended=false", st)
	}
	if len(st.Queue.Tracks) != 2 || st.Queue.CurrentIdx != 0 {
		t.Errorf("会话队列 = %+v, want 2 条 current=0", st.Queue)
	}
}

// TestSaveThrottledOnProgress 播放中自动保存按 saveInterval 节流。
func TestSaveThrottledOnProgress(t *testing.T) {
	m, _ := newResumeTestModel(t, nil, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 第一次进度事件（lastSave 零值）→ 立即保存 position=10
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 10, Duration: 200}})
	st := m.session.State()
	if st == nil || st.Position != 10 {
		t.Fatalf("首事件应保存: %+v", st)
	}

	// 节流窗口内 → 不保存
	m.lastSave = time.Now()
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 11, Duration: 200}})
	if st := m.session.State(); st.Position != 10 {
		t.Errorf("节流窗口内不应保存: %+v", st)
	}

	// 超过节流窗口 → 保存
	m.lastSave = time.Now().Add(-saveInterval - time.Second)
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12, Duration: 200}})
	if st := m.session.State(); st.Position != 12 {
		t.Errorf("超窗口应保存: %+v", st)
	}
}

// TestQuitWithoutTrackClearsSession 无播放中曲目时退出 → 清除会话文件。
func TestQuitWithoutTrackClearsSession(t *testing.T) {
	m, _ := newResumeTestModel(t, nil, nil)
	// 预置一个遗留会话（模拟上次退出残留）
	if err := m.session.Save(*sessionState(10, false)); err != nil {
		t.Fatal(err)
	}
	if m.state.Track != nil {
		t.Fatal("前置状态错误：应无播放中曲目")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if st := m.session.State(); st != nil {
		t.Error("无播放退出后会话应为空")
	}
}

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"music-tui/cache"
	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/queue"
	"music-tui/search"
	"music-tui/session"
	"music-tui/ytm"
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

// FetchPlaylist 是最小实现（接口扩展兼容）：返回空歌单，不参与 UI 测试逻辑。
func (f *fakeSearchAdapter) FetchPlaylist(ctx context.Context, playlistURL string, cookies search.CookieArgs) (model.Playlist, error) {
	return model.Playlist{}, nil
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

// newTestModel 组装真实服务（历史/封面用临时目录，歌词指向 404 的假服务器，
// 缓存指向临时目录 + 不存在的 yt-dlp（CacheAsync 后台下载立即失败退出，
// 无网络无泄漏））。附带未登录的 yt 客户端（临时 store + fake fetcher + 离线 browse 响应），
// 保证全程无网络副作用。onTrack 透传给 NewModel（MPRIS 曲目回调；测试可传 nil 或自定收集）。
func newTestModel(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, onTrack func(*model.Track)) Model {
	return newYTTestModel(t, fp, fa, onTrack).m
}

// ytTestEnv 是 YT Music 同步 UI 测试环境：store/client/fetcher 均可直接注入。
// browse 请求由离线 RoundTripper 固定响应（默认 logged_in=1 含两个歌单），
// 需要失败场景时用 env.client.SetHTTPClient 替换传输。
type ytTestEnv struct {
	store   *ytm.Store
	client  *ytm.Client
	fetcher *fakeYTFetcher
	m       Model
}

// newYTTestModel 构造带完整 yt 测试环境的模型（供 YT 同步流程测试）。
func newYTTestModel(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, onTrack func(*model.Track)) *ytTestEnv {
	t.Helper()
	env := &ytTestEnv{}
	store, err := ytm.NewStore(filepath.Join(t.TempDir(), "ytm.json"))
	if err != nil {
		t.Fatal(err)
	}
	env.store = store
	env.fetcher = &fakeYTFetcher{playlists: map[string]model.Playlist{}}
	env.client = ytm.NewClient(store, env.fetcher)
	env.client.SetHTTPClient(&http.Client{Transport: ytRoundTripper{code: 200, body: ytBrowseOK}})
	env.m = newTestModelBase(t, fp, fa, env.client, onTrack)
	return env
}

// newTestModelBase 组装除 yt 外的全部服务与模型（newTestModel/newYTTestModel 共用）。
// 缓存指向临时目录 + 不存在的 yt-dlp（CacheAsync 后台下载立即失败退出，无网络无泄漏）。
func newTestModelBase(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, yt *ytm.Client, onTrack func(*model.Track)) Model {
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, "/nonexistent/yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	return newTestModelBaseWithCache(t, fp, fa, yt, onTrack, cm)
}

// newTestModelBaseWithCache 同 newTestModelBase，但缓存管理器由调用方注入
// （缓存预热时序测试用：注入假 yt-dlp 脚本观测下载调用）。
func newTestModelBaseWithCache(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, yt *ytm.Client, onTrack func(*model.Track), cm *cache.Manager) Model {
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
	pls, err := playlists.NewStore(filepath.Join(t.TempDir(), "playlists.json"))
	if err != nil {
		t.Fatal(err)
	}
	return NewModel(fp, fa,
		lyrics.NewClientWithBaseURL(lyricServer.URL, "music-tui test (https://example.com)"),
		cf, hist, sess, pls, cm, yt, onTrack)
}

// refreshYTStatus 把 store 的登录状态同步进模型与页面（直接 seed store 后调用）。
func (env *ytTestEnv) refreshYTStatus() {
	env.m.ytLogin = env.client.Login()
	env.m.plPage = env.m.plPage.setYTSyncStatus(env.m.ytLogin, env.m.ytSyncing, env.m.ytInvalid)
}

// fakeYTFetcher 按 URL 返回预置歌单（ytm.Fetcher 的 UI 测试实现）。
type fakeYTFetcher struct {
	mu        sync.Mutex
	playlists map[string]model.Playlist
	err       error
	urls      []string
}

func (f *fakeYTFetcher) FetchPlaylist(ctx context.Context, playlistURL string, cookies search.CookieArgs) (model.Playlist, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, playlistURL)
	if f.err != nil {
		return model.Playlist{}, f.err
	}
	if p, ok := f.playlists[playlistURL]; ok {
		return p, nil
	}
	return model.Playlist{}, errors.New("未预置歌单 " + playlistURL)
}

// ytRoundTripper 固定返回 browse 响应的离线传输（VerifyLogin/同步不触网）。
type ytRoundTripper struct {
	code int
	body string
}

func (rt ytRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: rt.code,
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// ytBrowseOK 是 browse 有效登录响应（logged_in=1，两个歌单：PLAAA/PLBBB）。
const ytBrowseOK = `{
  "contents": {
    "singleColumnBrowseResultsRenderer": {
      "tabs": [{
        "tabRenderer": {
          "selected": true,
          "content": {
            "sectionListRenderer": {
              "contents": [{
                "itemSectionRenderer": {
                  "contents": [{
                    "gridRenderer": {
                      "items": [
                        {"musicTwoRowItemRenderer": {
                          "title": {"runs": [{"text": "我的最爱"}]},
                          "subtitle": {"runs": [{"text": "5 首"}]},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLAAA"}}
                        }},
                        {"musicTwoRowItemRenderer": {
                          "title": {"runs": [{"text": "通勤歌单"}]},
                          "subtitle": {"runs": [{"text": "12 首"}]},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLBBB"}}
                        }}
                      ]
                    }
                  }]
                }
              }]
            }
          }
        }
      }]
    }
  },
  "serviceTrackingParams": [{"service": "GFEEDBACK", "params": [{"key": "logged_in", "value": "1"}]}]
}`

// ytBrowseLoggedOut 是 browse 未登录响应（logged_in=0，无条目）。
const ytBrowseLoggedOut = `{
  "contents": {"singleColumnBrowseResultsRenderer": {"tabs": []}},
  "serviceTrackingParams": [{"service": "GFEEDBACK", "params": [{"key": "logged_in", "value": "0"}]}]
}`

// ytTrackURL 生成与 RemotePlaylist.URL() 一致的歌单 URL。
func ytTrackURL(id string) string {
	return "https://music.youtube.com/playlist?list=" + id
}

// m2：SyncAll 动态超时预算 = 30s 枚举余量 + 30s×歌单数，上限 10min。
func TestSyncAllBudget(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{9, 5 * time.Minute},   // 原固定 5min 恰好覆盖 9 个歌单
		{19, 10 * time.Minute}, // 30s + 30s×19 = 600s = 上限
		{20, 10 * time.Minute}, // 超过上限截断
		{100, 10 * time.Minute},
	}
	for _, tc := range cases {
		if got := syncAllBudget(tc.n); got != tc.want {
			t.Errorf("syncAllBudget(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
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

// activeToastText 返回当前活跃 toast 的文本（无 toast 时返回空串）。测试断言用。
// （注意与 Model 方法 toastText(t toast) 区分：后者渲染样式文本。）
func activeToastText(m Model) string {
	if m.toast == nil {
		return ""
	}
	return m.toast.text
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
	if m.current != pageQueue {
		t.Errorf("Tab 后 current = %v, want pageQueue", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.current != pagePlaylists {
		t.Errorf("按 3 后 current = %v, want pagePlaylists", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if m.current != pageSearch {
		t.Errorf("按 4 后 current = %v, want pageSearch", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	if m.current != pageHistory {
		t.Errorf("按 5 后 current = %v, want pageHistory", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.current != pageHome {
		t.Errorf("按 1 后 current = %v, want pageHome", m.current)
	}
}

func TestCtrlArrowsSwitchPages(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)

	// Ctrl+Right：正向循环 首页→队列→播放列表→搜索→历史→首页
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pageQueue {
		t.Errorf("Ctrl+Right 后 current = %v, want pageQueue", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pagePlaylists {
		t.Errorf("Ctrl+Right 后 current = %v, want pagePlaylists", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pageSearch {
		t.Errorf("Ctrl+Right 后 current = %v, want pageSearch", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pageHistory {
		t.Errorf("Ctrl+Right 后 current = %v, want pageHistory", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pageHome {
		t.Errorf("Ctrl+Right 循环后 current = %v, want pageHome", m.current)
	}

	// Ctrl+Left：反向一步 首页→历史
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if m.current != pageHistory {
		t.Errorf("Ctrl+Left 后 current = %v, want pageHistory", m.current)
	}
}

func TestShiftTabSwitchesPagesReverse(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)

	// Shift+Tab：反向循环 首页→历史→搜索→播放列表→队列→首页
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pageHistory {
		t.Errorf("Shift+Tab 后 current = %v, want pageHistory", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pageSearch {
		t.Errorf("Shift+Tab 后 current = %v, want pageSearch", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pagePlaylists {
		t.Errorf("Shift+Tab 后 current = %v, want pagePlaylists", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pageQueue {
		t.Errorf("Shift+Tab 后 current = %v, want pageQueue", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pageHome {
		t.Errorf("Shift+Tab 循环后 current = %v, want pageHome", m.current)
	}

	// Tab 与 Shift+Tab 互逆：Tab 一步后 Shift+Tab 回到原页
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyTab}) // home → queue
	if m.current != pageQueue {
		t.Fatalf("Tab 后 current = %v, want pageQueue", m.current)
	}
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.current != pageHome {
		t.Errorf("Tab 后 Shift+Tab 应回到原页: current = %v, want pageHome", m.current)
	}
}

// 搜索输入框聚焦时 Ctrl+←/→ 仍应全局切页（root 在 delegate 前消费按键，
// textinput 的 ctrl+←/→ 词跳转绑定收不到；代价是输入框内失去 ctrl+←/→
// 词跳转，alt+←/→ 仍可用）。
func TestCtrlArrowsSwitchPagesWhenSearchInputFocused(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m = runProgram(t, m,
		tea.KeyMsg{Type: tea.KeyTab}, // → 队列
		tea.KeyMsg{Type: tea.KeyTab}, // → 播放列表
		tea.KeyMsg{Type: tea.KeyTab}, // → 搜索页，输入框聚焦
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")},
		tea.KeyMsg{Type: tea.KeyCtrlLeft},
	)
	// 搜索页（index 3）反向一步 = 播放列表；聚焦不阻碍切页
	if m.current != pagePlaylists {
		t.Errorf("搜索输入框聚焦时 Ctrl+Left 后 current = %v, want pagePlaylists", m.current)
	}
	if got := m.searchPage.input.Value(); got != "晴天" {
		t.Errorf("切页后输入框内容应保留: input = %q, want %q", got, "晴天")
	}
	// 播放列表正向一步 = 搜索页（切走后焦点输入框内容仍在）
	m = runProgram(t, m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	if m.current != pageSearch {
		t.Errorf("Ctrl+Right 后 current = %v, want pageSearch", m.current)
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
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyTab},
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}) // 数字键直达搜索页
	if m.current != pageSearch {
		t.Fatal("按 4 后应在搜索页")
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
	if m.home.coverRenderCache != "" {
		t.Error("过期封面结果不应被应用")
	}
}

func TestPlayFailureShowsError(t *testing.T) {
	toastErrorDuration = time.Millisecond // 快进 toast 定时器（失败提示 cmd 是 tea.Tick）
	defer func() { toastErrorDuration = 5 * time.Second }()
	fp := newFakePlayer()
	fp.playErr = true
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	// 失败路径仅返回 toast 消失定时器 cmd：执行后不得产生歌词/封面/历史结果
	for _, msg := range execCmds(cmd) {
		switch msg.(type) {
		case lyricsResultMsg, coverResultMsg, historyResultMsg:
			t.Errorf("播放失败后不应再发歌词/封面/历史异步 cmd: %#v", msg)
		}
	}
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("toast = %q, want 含“播放失败”", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "mpv 崩溃") {
		t.Errorf("toast = %q, want 含 mpv 崩溃", activeToastText(m))
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

// SetLoop 失败仅记 toast，不阻断播放（异步 cmd 照常、状态照常）。
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
	if !strings.Contains(activeToastText(m), "循环") {
		t.Errorf("toast = %q, want 含循环失败信息", activeToastText(m))
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // home → queue
	if m.current != pageQueue {
		t.Fatalf("current = %v, want pageQueue", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // queue → playlists
	if m.current != pagePlaylists {
		t.Fatalf("current = %v, want pagePlaylists", m.current)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // playlists → search
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

// 加载超时（LoadTimeoutError，看门狗主动报错）应走现有自动重试链路：
// 预算内重试 = 重新 loadfile = 重新取流（拿新签名 URL），把取流悬挂转成
// 可恢复的重试而非无限期静默卡住（回归：连播未缓存下一首卡住）。
func TestLoadTimeoutErrorAutoRetriesThenSucceeds(t *testing.T) {
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

	// 看门狗超时错误：进入自动重试（暂停态 + toast），不落入“停止等手动”分支
	m, cmd = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadTimeoutError{Timeout: 30 * time.Second}}})
	if !strings.Contains(activeToastText(m), "自动重试") {
		t.Errorf("toast = %q, want 含自动重试", activeToastText(m))
	}
	if m.retryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", m.retryCount)
	}
	if m.state.Playing {
		t.Error("重试等待期间 Playing 应为 false")
	}
	if m.ended {
		t.Error("超时重试期间不应 ended（不是停止态）")
	}

	// 重试到期：重新 loadfile（新取流），加载中重新计时
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 2 {
		t.Fatalf("重试后 playCount = %d, want 2", fp.playCount())
	}
	if m.loadingSince.IsZero() {
		t.Error("重试 beginPlay 后应重新设置 loadingSince（新一轮加载计时）")
	}

	// 重试成功：TrackStarted 到达 → 加载中结束、预算重置
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if !m.loadingSince.IsZero() {
		t.Error("TrackStarted 后 loadingSince 应清零")
	}
	if m.retryCount != 0 {
		t.Errorf("加载成功后 retryCount = %d, want 0（预算重置）", m.retryCount)
	}
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
	if !strings.Contains(activeToastText(m), "正在自动重试（1/2）") {
		t.Errorf("toast = %q, want 含“正在自动重试（1/2）”", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "跳过") || !strings.Contains(activeToastText(m), "测试歌曲 t1") {
		t.Errorf("toast = %q, want 含“跳过”与第 1 首标题", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "已重试 2 次") || !strings.Contains(activeToastText(m), "请稍后重试或更换歌曲") {
		t.Errorf("toast = %q, want 含“已重试 2 次”与“请稍后重试或更换歌曲”", activeToastText(m))
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

// 回归（审查 Blocker 1）：队列多曲全部取流失败 → 无限交替重播死循环。
// 队列 [t1,t2] 两首都失败：t1 重试耗尽 → 跳 t2（记录 t1 本轮失败）→ t2 耗尽
// → Next() 回绕返回 t1（ID 不同，同 ID 防护不拦）→ 若继续交替，playCount
// 无限增长、ended 永不置位。修复：本轮失败 ID 集合——跳过前检查目标曲目
// 已在失败集合中则不跳，走停止路径（ended=true + 横幅 + Playing=false）。
func TestLoadFailAllTracksFailStopsLoop(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m.queue.Add(testTrack("t2")) // 队列 [t1, t2]

	loadErr := player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
	// t1 两次重试均失败
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	// 第 3 次失败：t1 耗尽 → 跳过到 t2（本轮失败集合记入 t1）
	m, _ = update(m, playerEventMsg{ev: loadErr})
	if fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("t1 耗尽应跳到 t2: %q", fp.lastPlayed())
	}
	// t2 两次重试均失败
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	// 第 3 次失败：t2 耗尽，Next() 回绕返回 t1——t1 已在失败集合，必须停止
	//（而非跳回 t1 继续交替重播），playCount 有界。
	m, _ = update(m, playerEventMsg{ev: loadErr})
	if fp.playCount() != 6 {
		t.Errorf("全部取流失败后 playCount = %d, want 6（有界，不再增长）", fp.playCount())
	}
	if !m.ended {
		t.Error("全部取流失败后 ended 应为 true")
	}
	if m.state.Playing {
		t.Error("全部取流失败后 Playing 应为 false")
	}
	if !strings.Contains(activeToastText(m), "已重试 2 次") {
		t.Errorf("toast = %q, want 含“已重试 2 次”", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "跳过") || !strings.Contains(activeToastText(m), "测试歌曲 t1") {
		t.Errorf("toast = %q, want 含“跳过”与 t1 标题", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "队列已清空") {
		t.Errorf("toast = %q, want 含队列已清空", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "恢复播放失败") || !strings.Contains(activeToastText(m), "风控") {
		t.Errorf("toast = %q, want 含“恢复播放失败”与 hint 诊断（风控）", activeToastText(m))
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
	if !strings.Contains(activeToastText(m), "正在自动重试（1/2）") {
		t.Errorf("toast = %q, want 含“正在自动重试（1/2）”", activeToastText(m))
	}
}

// 回归（审查 P2-1）：恢复路径（resumeCmd → resumeResultMsg 成功）加载中提示。
// loadingSince 此前只在 beginPlay 设置：恢复未缓存曲目取流悬挂时首页无
// "⏳ 加载中…"提示（30s 后看门狗兜底报错，但 UX 目标"可感知"在恢复路径未覆盖）。
// resumeResultMsg 成功即置 loadingSince（恢复加载进行中）；TrackStartedEvent
// 到达时统一清除（现有分支已清，无需额外处理）。
func TestResumeLoadingIndicatorShownUntilTrackStarted(t *testing.T) {
	m, _ := newResumeTestModel(t, sessionState(66.6, false), nil)

	msgs := execCmds(resumeCmd(m))
	var resumed bool
	for _, msg := range msgs {
		if _, ok := msg.(resumeResultMsg); ok {
			resumed = true
		}
	}
	if !resumed {
		t.Fatal("未收到 resumeResultMsg")
	}
	m, _ = update(m, msgs[0])
	if m.loadingSince.IsZero() {
		t.Fatal("resumeResultMsg 成功后应设置 loadingSince（恢复加载进行中）")
	}

	// 2s 阈值未过：不显示加载中
	if m.home.loading {
		t.Error("阈值未过时不应显示加载中")
	}

	// tick 时间注入 3s 后：派生 loading=true，进度行显示加载中提示
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(3 * time.Second)})
	if !m.home.loading {
		t.Error("恢复加载 2s 未收到 TrackStarted → 应显示加载中")
	}
	if got := m.home.progressRowView(); !strings.Contains(got, "加载中") {
		t.Errorf("进度行应显示加载中提示, got %q", got)
	}

	// TrackStarted 到达（恢复加载成功）：loadingSince 清零、提示结束
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if !m.loadingSince.IsZero() {
		t.Error("TrackStarted 后 loadingSince 应清零")
	}
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(4 * time.Second)})
	if m.home.loading {
		t.Error("TrackStarted 后不应再显示加载中")
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

// TestRootViewToastLayoutStable 回归：toast 与状态栏不得改变 View 行数、不得替换
// 或挤压页面内容——错误提示出现/消失排版零跳动（旧横幅替换中间区末行曾致内容跳动）。
func TestRootViewToastLayoutStable(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	plain := m.View()
	if got := len(strings.Split(plain, "\n")); got != 24 {
		t.Fatalf("空态无 toast View 行数 = %d, want 24", got)
	}
	if last := strings.Split(plain, "\n")[23]; strings.TrimSpace(last) != "" {
		t.Errorf("首页状态栏应留空, got %q", last)
	}

	m, _ = m.showToast("恢复播放失败: 测试错误", toastError)
	withToast := m.View()
	if got := len(strings.Split(withToast, "\n")); got != 24 {
		t.Errorf("有 toast 时 View 行数 = %d, want 24", got)
	}
	if !strings.Contains(withToast, "⚠") || !strings.Contains(withToast, "恢复播放失败") {
		t.Error("View 应包含错误 toast")
	}
	// 除最后一行（toast 覆盖区 = 状态栏行）外，其余行与无 toast 时逐行相同
	p, wt := strings.Split(plain, "\n"), strings.Split(withToast, "\n")
	for i := range p {
		if i == 23 { // 覆盖区 = 最后一行
			continue
		}
		if p[i] != wt[i] {
			t.Errorf("第 %d 行被 toast 改变:\n无 toast: %q\n有 toast: %q", i, p[i], wt[i])
		}
	}
	if !strings.Contains(wt[23], "恢复播放失败") {
		t.Errorf("toast 应覆盖在最后一行（状态栏行）, got %q", wt[23])
	}
	if !strings.HasPrefix(stripAnsiForTest(wt[23]), "⚠") {
		t.Errorf("toast 应左对齐（行首为 ⚠ 图标）, got %q", wt[23])
	}
	// 过期消息命中后 toast 消失，View 与无 toast 时完全一致（状态栏行恢复）
	m, _ = update(m, toastExpireMsg{id: m.toast.id})
	if got := m.View(); got != plain {
		t.Errorf("toast 过期后 View 应与无 toast 时完全一致:\n无 toast: %q\n过期后: %q", plain, got)
	}

	// 播放态（队列页：状态栏显示 顺序 · 1/1，可验证覆盖与恢复）
	fp := newFakePlayer()
	m2 := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, cmd := m2.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m2, _ = update(m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = m2.switchPage("2") // 队列页，状态栏有内容
	if got := len(strings.Split(m2.View(), "\n")); got != 24 {
		t.Errorf("播放态 View 行数 = %d, want 24", got)
	}
	before := m2.View()
	if bar := strings.Split(before, "\n")[23]; !strings.Contains(bar, "顺序") {
		t.Fatalf("队列页状态栏应含 顺序（覆盖基准）, got %q", bar)
	}
	m2, _ = m2.showToast("播放失败: 测试错误", toastError)
	l2 := strings.Split(m2.View(), "\n")
	if !strings.Contains(l2[23], "播放失败") {
		t.Errorf("播放态 toast 应覆盖在最后一行（状态栏行）, got %q", l2[23])
	}
	if strings.Contains(l2[23], "顺序") {
		t.Errorf("toast 应临时覆盖状态栏内容（不再含 顺序）, got %q", l2[23])
	}
	// 过期恢复：toast 消失后状态栏内容恢复，View 与无 toast 时完全一致
	m2, _ = update(m2, toastExpireMsg{id: m2.toast.id})
	if got := m2.View(); got != before {
		t.Errorf("toast 过期后播放态 View 应恢复与无 toast 时一致:\n无 toast: %q\n过期后: %q", before, got)
	}
}

// TestRootViewWideToastStaysWithinWidth 回归：超宽 toast 截断后覆盖行宽恒 ≤ 窗口
// 宽度——曾按 m.width 截断后仍追加分隔符导致覆盖行 m.width+2 格、终端折行超屏。
// 现在整行替换状态栏行：左对齐 + 尾部省略号（保头部语义，截掉句尾是用户要求
// 的变更，与旧 TruncateLeft 保句尾不同）。
func TestRootViewWideToastStaysWithinWidth(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	long := "「这是一首名字特别特别特别特别特别特别特别特别特别特别长的歌」播放失败：YouTube 拒绝访问（风控/限流），可稍后重试，已重试 2 次，跳过继续播放"
	m, _ = m.showToast(long, toastWarning)
	out := m.View()
	if got := len(strings.Split(out, "\n")); got != 24 {
		t.Fatalf("超宽 toast View 行数 = %d, want 24（不超屏）", got)
	}
	lines := strings.Split(out, "\n")
	if w := ansi.StringWidth(lines[23]); w > 80 {
		t.Errorf("覆盖行宽 = %d, want ≤ 80", w)
	}
	stripped := stripAnsiForTest(lines[23])
	if !strings.HasPrefix(stripped, "⚠") || !strings.Contains(stripped, "播放失败") {
		t.Errorf("超宽 toast 应保头部语义（⚠ 图标 + 消息开头）, got %q", lines[23])
	}
	if !strings.HasSuffix(stripped, "…") {
		t.Errorf("超宽 toast 尾部应有省略号截断标记, got %q", lines[23])
	}
}

// TestStatusBarEmptyStateOtherPage：空态（无播放）非首页状态栏左侧显示
// "未在播放"（首页留空，此语义仅其他页生效）。
func TestStatusBarEmptyStateOtherPage(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	m = m.switchPage("2") // 队列页
	lines := strings.Split(m.View(), "\n")
	bar := lines[len(lines)-1]
	if !strings.Contains(bar, "未在播放") {
		t.Errorf("非首页空态状态栏应显示 未在播放, got %q", bar)
	}
	if w := ansi.StringWidth(bar); w > 40 {
		t.Errorf("状态栏行宽 = %d, want ≤ 40", w)
	}
}

// TestStatusBarLayout 回归：状态栏首页留空（信息与首页控制栏重复）、
// 其他页左侧歌曲名 + 右侧播放顺序；窄窗口下右侧顺序优先、左侧名称截断不折行。
// （曾为左顺序右标题：标题按剩余宽度动态截断，曾按 m.width/2 固定截断致折行。）
func TestStatusBarLayout(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("一首名字特别长特别长特别长特别长特别长特别长的歌"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.WindowSizeMsg{Width: 21, Height: 24})

	// 首页：状态栏行留空（布局行恒在）
	homeLines := strings.Split(m.View(), "\n")
	if got := len(homeLines); got != 24 {
		t.Fatalf("首页 View 行数 = %d, want 24", got)
	}
	if last := homeLines[len(homeLines)-1]; strings.TrimSpace(last) != "" {
		t.Errorf("首页状态栏应留空, got %q", last)
	}

	// 队列页：左 = 歌曲名（截断含 …），右 = 播放顺序（⏵ 顺序 · 1/1）
	m = m.switchPage("2")
	lines := strings.Split(m.View(), "\n")
	bar := lines[len(lines)-1]
	if !strings.Contains(bar, "…") {
		t.Errorf("队列页状态栏左侧名称应截断含省略号, got %q", bar)
	}
	if !strings.Contains(bar, "顺序") || !strings.Contains(bar, "1/1") {
		t.Errorf("队列页状态栏右侧应含播放顺序, got %q", bar)
	}
	// 标题在左侧、顺序在右侧（顺序文本出现在名称之后）；锚点用“歌”：
	// 标题以 "测试歌曲 " 开头，截断后仍保留开头字符（“名”为长 id 中段必被截掉）
	titleIdx := strings.Index(bar, "歌")
	seqIdx := strings.Index(bar, "顺序")
	if titleIdx < 0 || seqIdx < 0 || titleIdx > seqIdx {
		t.Errorf("状态栏应为左名称右顺序, got %q", bar)
	}
	if w := ansi.StringWidth(bar); w > 21 {
		t.Errorf("窄窗口状态栏行宽 = %d, want ≤ 21", w)
	}
}

// TestStatusBarPinnedToBottom 回归：内容不满一屏的页面（搜索空态等）body
// 填充到页面高度，状态栏恒在屏幕最后一行——曾因 body 行数不足状态栏随内容
// 上浮不贴底。
func TestStatusBarPinnedToBottom(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// 切到搜索页空态（内容仅 4 行，远不满屏）
	m = m.switchPage("4")
	lines := strings.Split(m.View(), "\n")
	if got := len(lines); got != 24 {
		t.Fatalf("搜索页空态 View 行数 = %d, want 24（body 填充到页面高度）", got)
	}
	if last := lines[23]; strings.TrimSpace(last) == "" {
		t.Error("搜索页状态栏应贴屏幕底（末行非空）")
	}
	// 队列页空态同样贴底
	m = m.switchPage("2")
	lines = strings.Split(m.View(), "\n")
	if got := len(lines); got != 24 {
		t.Fatalf("队列页空态 View 行数 = %d, want 24", got)
	}
	if strings.TrimSpace(lines[23]) == "" {
		t.Error("队列页状态栏应贴屏幕底（末行非空）")
	}
}

// TestStatusBarNarrowWindowFits 回归：极端窄窗口（宽度 < 右侧顺序文本）下右侧
// 截断兜底，状态栏恒 1 行不折行（曾因右侧永不截断致行宽 > 窗口宽度折行）。
func TestStatusBarNarrowWindowFits(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("一首名字特别长特别长特别长特别长特别长特别长的歌"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.WindowSizeMsg{Width: 10, Height: 24})
	m = m.switchPage("2") // 队列页

	bar := m.statusBarView()
	if strings.Contains(bar, "\n") {
		t.Errorf("状态栏必须恒为 1 行不折行, got %q", bar)
	}
	if w := ansi.StringWidth(bar); w > 10 {
		t.Errorf("极窄窗口(10)状态栏行宽 = %d, want ≤ 10", w)
	}
	// 经 View 渲染的末行也不得超出窗口宽度（折行残片会出现在末行）
	lines := strings.Split(m.View(), "\n")
	if last := lines[len(lines)-1]; ansi.StringWidth(last) > 10 {
		t.Errorf("View 末行行宽 = %d, want ≤ 10", ansi.StringWidth(last))
	}
}

// TestToastLifecycle 集成：showToast 覆盖语义 + 过期消息 id 匹配/不匹配。
func TestToastLifecycle(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = m.showToast("错误 A", toastError)
	if m.toast == nil || m.toast.text != "错误 A" || m.toast.kind != toastError {
		t.Fatalf("showToast 后应显示 toast A, got %+v", m.toast)
	}
	m, _ = m.showToast("错误 B", toastWarning)
	if m.toast == nil || m.toast.text != "错误 B" || m.toast.kind != toastWarning {
		t.Fatalf("覆盖后应显示 toast B, got %+v", m.toast)
	}
	// 旧 toast 的过期消息（id=1）不应清掉新 toast
	m, _ = update(m, toastExpireMsg{id: 1})
	if m.toast == nil || m.toast.text != "错误 B" {
		t.Fatalf("过期消息 id 不匹配不应清除新 toast, got %+v", m.toast)
	}
	// 当前 toast 的过期消息应清除
	m, _ = update(m, toastExpireMsg{id: m.toast.id})
	if m.toast != nil {
		t.Fatalf("过期消息 id 匹配应清除 toast, got %+v", m.toast)
	}
}

// TestShowToastTickCmd 校验 showToast 返回的 cmd 产生匹配 id 的过期消息
// （用 execCmds 执行；时长调小避免测试等待）。
func TestShowToastTickCmd(t *testing.T) {
	toastErrorDuration = time.Millisecond
	defer func() { toastErrorDuration = 5 * time.Second }()
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, cmd := m.showToast("播放失败: 测试", toastError)
	msgs := execCmds(cmd)
	found := false
	for _, msg := range msgs {
		if em, ok := msg.(toastExpireMsg); ok && em.id == m.toast.id {
			found = true
		}
	}
	if !found {
		t.Errorf("showToast 的 cmd 应产生匹配当前 toast id 的过期消息, got %#v", msgs)
	}
}

// ---- 缓存预热时序 + 加载中提示（回归：连播未缓存下一首卡住） ----

// writeFakeYtDlpScript 生成假 yt-dlp 脚本：解析 -o 模板落盘合法产物
// （下载注册成功、不产生重试噪音），并把每次调用追加到 logPath
// （缓存预热调用观测用）。与 cache/download_test.go 的 fakeYtDlpBody 同款。
func writeFakeYtDlpScript(t *testing.T, logPath string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yt-dlp")
	body := fmt.Sprintf(`#!/bin/sh
echo invoked >> %q
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$(printf '%%s' "$a" | sed 's/%%(ext)s/webm/')
  fi
  prev="$a"
done
[ -n "$out" ] || exit 9
printf 'fake-audio-bytes' > "$out"
`, logPath)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// ytDlpCallCount 统计假 yt-dlp 脚本被调用次数（日志文件行数）。
func ytDlpCallCount(logPath string) int {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "invoked")
}

// 缓存预热移后（回归：连播未缓存下一首卡住）：beginPlay 不再触发 CacheAsync
// ——避免与 mpv 内置 yt-dlp 并发访问同一 URL 放大 403 风控；TrackStartedEvent
// （mpv 取流成功）后才启动后台下载。假 yt-dlp 脚本记录调用时序验证。
func TestTrackStartedTriggersCacheWarmup(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	logPath := filepath.Join(t.TempDir(), "ytdlp.log")
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, writeFakeYtDlpScript(t, logPath))
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModelBaseWithCache(t, fp, fa, nil, nil, cm)

	// 手动播放 t1（缓存 miss）：beginPlay 不得触发下载
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if fp.playCount() != 1 {
		t.Fatalf("playCount = %d, want 1", fp.playCount())
	}
	// 短暂窗口内假 yt-dlp 不得被调用（下载若已触发会立刻执行）
	time.Sleep(200 * time.Millisecond)
	if got := ytDlpCallCount(logPath); got != 0 {
		t.Fatalf("beginPlay（缓存 miss）不应触发缓存下载, calls = %d", got)
	}

	// 连播：TrackEnded → 下一首 t2（缓存 miss）→ beginPlay 仍不得触发下载
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("TrackEnded 后应连播 t2: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	time.Sleep(200 * time.Millisecond)
	if got := ytDlpCallCount(logPath); got != 0 {
		t.Fatalf("连播 beginPlay（缓存 miss）不应触发缓存下载, calls = %d", got)
	}

	// TrackStartedEvent（mpv 取流成功）→ 后台下载才启动
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.state.Track == nil || m.state.Track.ID != "t2" {
		t.Fatalf("TrackStarted 后 state.Track 应为 t2, got %+v", m.state.Track)
	}
	waitFor(t, 3*time.Second, func() bool { return ytDlpCallCount(logPath) >= 1 })
}

// 加载中提示（回归：取流悬挂卡住可感知）：beginPlay 后 2s 未收到
// TrackStartedEvent → 首页进度行显示"加载中…"；TrackStarted 到达后恢复进度条。
// TickMsg.Time 是 tick 发送时刻：测试注入任意时间，无需真实等待 2s。
func TestLoadingIndicatorShownUntilTrackStarted(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if m.loadingSince.IsZero() {
		t.Fatal("beginPlay 成功后应设置 loadingSince")
	}

	// 2s 阈值未过：不显示加载中
	if m.home.loading {
		t.Error("阈值未过时不应显示加载中")
	}

	// tick 时间注入 3s 后：派生 loading=true，进度行显示加载中提示
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(3 * time.Second)})
	if !m.home.loading {
		t.Error("切歌 2s 未收到 TrackStarted → 应显示加载中")
	}
	if got := m.home.progressRowView(); !strings.Contains(got, "加载中") {
		t.Errorf("进度行应显示加载中提示, got %q", got)
	}

	// TrackStarted 到达：加载中结束（loadingSince 清零，进度条恢复）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if !m.loadingSince.IsZero() {
		t.Error("TrackStarted 后 loadingSince 应清零")
	}
	m, _ = update(m, spinner.TickMsg{Time: time.Now().Add(4 * time.Second)})
	if m.home.loading {
		t.Error("TrackStarted 后不应再显示加载中")
	}
	if got := m.home.progressRowView(); strings.Contains(got, "加载中") {
		t.Errorf("TrackStarted 后进度行不应再显示加载中, got %q", got)
	}
}

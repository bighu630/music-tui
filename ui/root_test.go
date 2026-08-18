package ui

import (
	"runtime"
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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

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
	restarts     int    // Restart 调用计数
	restartErr   bool   // 为 true 时 Restart 返回错误（测试注入）
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

// Restart 模拟强制重启 mpv 进程（卡住恢复）：记录调用数，可选注入错误。
func (f *fakePlayer) Restart() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	if f.restartErr {
		return errors.New("重启失败")
	}
	return nil
}

// restartCount 返回 Restart 调用次数（卡住自动重启测试）。
func (f *fakePlayer) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

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
// 缓存为禁用态（Disabled）：本测试线聚焦 UI 播放状态机，不含缓存集成——缓存命中/
// 下载/兑底路径由 cache_test.go 的 newCacheTestModel*（显式注入真实缓存）覆盖。
// 禁用缓存 = “下载不可用”：ErrorEvent 的缓存兑底分支（需 CacheAsync 启动下载）
// 不参与，取流失败直接走现有重试链路——与既有 LoadFail 系列测试语义一致。
func newTestModelBase(t *testing.T, fp *fakePlayer, fa *fakeSearchAdapter, yt *ytm.Client, onTrack func(*model.Track)) Model {
	cm := cache.Disabled()
	return newTestModelBaseWithCache(t, fp, fa, yt, onTrack, cm)
}

// newTestModelBaseWithCache 同 newTestModelBase，但缓存管理器由调用方注入
// （缓存预热时序测试用：注入假 yt-dlp 脚本观测下载调用）。
// 默认 ytdlpConfigured=false（未配置 yt-dlp cookie；需要已配置场景的测试直接改 m.ytdlpConfigured）。
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
		cf, hist, sess, pls, cm, yt, onTrack, nil, false, nil)
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

// execSearchCmds 执行搜索 Enter 返回的 cmd 并展开 Batch（Enter 委托按需附加
// spinnerTick 启动命令，Batch 在真实 bubbletea 中由事件循环展开）：返回全部
// 子命令产出的非 nil 消息（与 Update 的 tea.BatchMsg 分支同款模式）。
func execSearchCmds(cmd tea.Cmd) []tea.Msg {
	var msgs []tea.Msg
	for _, c := range execCmds(cmd) {
		if bm, ok := c.(tea.BatchMsg); ok {
			msgs = append(msgs, execCmds(bm...)...)
			continue
		}
		msgs = append(msgs, c)
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

// Ctrl+←/→ 从切页改为全局上下一首（不再切 Tag；切页保留 Tab/Shift+Tab/数字 1-5）。
// 任何页面按下都产生 nextTrackMsg/prevTrackMsg（root 在 delegate 前全局消费），
// 且页面不切换。切页行为回归由 TestTabSwitchesPages/TestShiftTabSwitchesPagesReverse 覆盖。
func TestCtrlArrowGlobalPrevNext(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // 首页 → 队列（非首页验证“全局”+“不切页”）

	// Ctrl+Right → nextTrackMsg，页面留在队列
	got, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlRight})
	msgs := execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("Ctrl+Right 消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(nextTrackMsg); !ok {
		t.Errorf("Ctrl+Right 消息类型 = %T, want nextTrackMsg", msgs[0])
	}
	if got.current != pageQueue {
		t.Errorf("Ctrl+Right 不应切页: current = %v, want pageQueue", got.current)
	}

	// Ctrl+Left → prevTrackMsg，页面留在队列
	got, cmd = update(got, tea.KeyMsg{Type: tea.KeyCtrlLeft})
	msgs = execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("Ctrl+Left 消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(prevTrackMsg); !ok {
		t.Errorf("Ctrl+Left 消息类型 = %T, want prevTrackMsg", msgs[0])
	}
	if got.current != pageQueue {
		t.Errorf("Ctrl+Left 不应切页: current = %v, want pageQueue", got.current)
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

// 搜索输入框聚焦时 Ctrl+←/→ 仍应全局上下一首（root 在 delegate 前消费按键）：
// 不切页、不干扰输入框内容。textinput 的 ctrl+←/→ 词跳转绑定收不到；
// 代价是输入框内失去 ctrl+←/→ 词跳转，alt+←/→ 仍可用（与旧切页绑定同代价）。
func TestCtrlArrowGlobalPrevNextWhenSearchInputFocused(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // → 队列
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // → 播放列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // → 搜索页，输入框聚焦
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})

	// Ctrl+Left → prevTrackMsg；页面留在搜索页，输入框内容保留
	got, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlLeft})
	msgs := execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("Ctrl+Left 消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(prevTrackMsg); !ok {
		t.Errorf("Ctrl+Left 消息类型 = %T, want prevTrackMsg", msgs[0])
	}
	if got.current != pageSearch {
		t.Errorf("搜索输入框聚焦时 Ctrl+Left 不应切页: current = %v, want pageSearch", got.current)
	}
	if v := got.searchPage.input.Value(); v != "晴天" {
		t.Errorf("输入框内容应保留: input = %q, want %q", v, "晴天")
	}

	// Ctrl+Right → nextTrackMsg；页面仍在搜索页
	got, cmd = update(got, tea.KeyMsg{Type: tea.KeyCtrlRight})
	msgs = execCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("Ctrl+Right 消息数 = %d, want 1", len(msgs))
	}
	if _, ok := msgs[0].(nextTrackMsg); !ok {
		t.Errorf("Ctrl+Right 消息类型 = %T, want nextTrackMsg", msgs[0])
	}
	if got.current != pageSearch {
		t.Errorf("Ctrl+Right 不应切页: current = %v, want pageSearch", got.current)
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
	msgs := execSearchCmds(cmd)
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

// ---------------------------------------------------------------------------
// 事件链回归：TrackEnded 自动连播后 waitForPlayerEvents 必须重新发出
// ---------------------------------------------------------------------------
// 背景：waitForPlayerEvents 是一次性 cmd（每次只读 1 个事件），由 onPlayerEvent
// 重新发出才能继续读下一个。TrackEndedEvent 分支此前提前 return beginPlay/
// stopAfterEnd 的结果，丢弃了链——连播后无人再读 p.Events()，256 缓冲满后
// emit 丢弃新事件：UI 进度冻结在 0.00、歌词不动，而 MPRIS（独立 Subscribe
// 广播通道）仍正常 → playerctl 进度正常。手动切歌/暂停不受影响（链在按键
// 时仍活着），只有自然播完自动连播触发。

// execReturnedChain 执行 update 返回的 cmd，并把产出消息回灌 Update
// （模拟 bubbletea 事件循环：cmd 执行结果作为下一条消息处理）。
// 返回处理后的模型。
func execReturnedChain(m Model, cmd tea.Cmd) Model {
	msgs := execCmds(cmd)
	for _, msg := range msgs {
		m, _ = update(m, msg)
	}
	return m
}

// TrackEnded 自动连播下一首（用户报告场景：连播未缓存下一首走 URL）后，
// 返回的 cmd 必须重新包含 waitForPlayerEvents：预推的 ProgressEvent 应被
// UI 消费并更新 Position（否则进度冻结 0.00、歌词不动）。
func TestTrackEndedAutoAdvanceKeepsEventChainAlive(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.queue.Replace(testTrack("t1"))
	m.queue.Add(testTrack("t2"))
	m, cmd := m.playQueueTrack() // 播放 t1
	_ = execCmds(cmd)

	// 预推连播后首个进度事件：若 TrackEnded 返回的 cmd 不含事件链，
	// 该事件将永远滞留在播放器通道中（生产：256 缓冲满后被 emit 丢弃）。
	fp.events <- player.ProgressEvent{Position: 5, Duration: 200}
	m, cmd = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Fatalf("TrackEnded 应自动连播 t2: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if cmd == nil {
		t.Fatal("TrackEnded 后必须重新发出事件链（否则 UI 永久收不到播放器事件）")
	}
	m = execReturnedChain(m, cmd)
	if m.state.Position != 5 {
		t.Fatalf("连播后首个 ProgressEvent 未被 UI 消费: Position=%v, want 5（事件链断裂，进度冻结）", m.state.Position)
	}
}

// TrackEnded 时队列为空（停止播放）同样必须保持事件链：之后用户手动播放
// 新曲时，进度事件才能继续到达 UI。
func TestTrackEndedStopKeepsEventChainAlive(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.queue.Replace(testTrack("t1"))
	m, cmd := m.playQueueTrack()
	_ = execCmds(cmd)

	// 清空队列 → t1 播完 → 停止路径
	m, _ = update(m, queueClearMsg{})
	fp.events <- player.ProgressEvent{Position: 3, Duration: 200}
	m, cmd = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if !m.ended {
		t.Fatalf("队列为空时 TrackEnded 应停止: ended=%v", m.ended)
	}
	if cmd == nil {
		t.Fatal("TrackEnded(停止) 后必须重新发出事件链（否则后续手动播放进度冻结）")
	}
	m = execReturnedChain(m, cmd)
	if m.state.Position != 3 {
		t.Fatalf("停止后事件链未消费预推 ProgressEvent: Position=%v, want 3", m.state.Position)
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

// TestCoverReadyCallback coverResultMsg 匹配当前曲目且成功时触发 onCoverReady
//（MPRIS 重发 Metadata 回调）；失败或过期 trackID 不触发；nil 安全。
func TestCoverReadyCallback(t *testing.T) {
	fp := newFakePlayer()
	calls := 0
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.onCoverReady = func() { calls++ }
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 成功 + 匹配当前曲目 → 触发
	m, _ = update(m, coverResultMsg{trackID: "t1", path: "/tmp/x.jpg"})
	if calls != 1 {
		t.Fatalf("匹配当前曲目且成功时应触发 onCoverReady: calls=%d", calls)
	}
	// 失败（err 非 nil）→ 不触发
	m, _ = update(m, coverResultMsg{trackID: "t1", err: errors.New("boom")})
	if calls != 1 {
		t.Fatalf("失败不应触发 onCoverReady: calls=%d", calls)
	}
	// 过期 trackID（非当前曲目）→ 不触发
	m, _ = update(m, coverResultMsg{trackID: "stale", path: "/tmp/y.jpg"})
	if calls != 1 {
		t.Fatalf("过期 trackID 不应触发 onCoverReady: calls=%d", calls)
	}
	// onCoverReady 为 nil（默认）→ 不 panic
	m2 := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m2, cmd = m2.startPlay(testTrack("t2"))
	_ = execCmds(cmd)
	_, _ = update(m2, coverResultMsg{trackID: "t2", path: "/tmp/z.jpg"})
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

// 首页 , . 与中文标点 ，。仅在首页生效（root 委托给当前页）；
// 其他页面按下不产生上一下一/下一首（切页需 Tab/数字，切曲需 Ctrl+←/→）。
func TestPrevNextKeysHomeOnly(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	// 切到队列页（非首页）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageQueue {
		t.Fatalf("前置: current=%v, want pageQueue", m.current)
	}

	for _, k := range []string{",", ".", "，", "。"} {
		got, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		msgs := execCmds(cmd)
		if len(msgs) != 0 {
			t.Errorf("%q 在非首页不应产生命令消息: %v", k, msgs)
		}
		if got.current != pageQueue {
			t.Errorf("%q 在非首页不应切页: current=%v", k, got.current)
		}
		m = got
	}
	// 播放未被触发：仍只播放过初始 t1
	if fp.playCount() != 1 {
		t.Errorf("非首页 , . 不应触发播放: playCount=%d, want 1", fp.playCount())
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

// 未配置 yt-dlp cookie 且取流失败属风控类（no audio or video data played / 403）：
// toast 附加「配置 YT Music 登录」引导，给用户可操作方向（TDD 引导行为）。
func TestLoadFailHintGuidesCookieWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name     string
		fileErr  string
		wantHint string
	}{
		{
			name:     "no audio or video data played",
			fileErr:  "no audio or video data played",
			wantHint: "可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效",
		},
		{
			name:     "403 forbidden",
			fileErr:  "yt-dlp: HTTP Error 403: Forbidden",
			wantHint: "可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePlayer()
			m := newTestModel(t, fp, &fakeSearchAdapter{}, nil) // 默认 ytdlpConfigured=false
			m, cmd := m.startPlay(testTrack("t1"))
			_ = execCmds(cmd)

			m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: tc.fileErr}}})
			toast := activeToastText(m)
			if !strings.Contains(toast, "配置 YT Music 登录") {
				t.Errorf("toast = %q, want 含“配置 YT Music 登录”引导", toast)
			}
			if !strings.Contains(toast, tc.wantHint) {
				t.Errorf("toast = %q, want 含完整引导 %q", toast, tc.wantHint)
			}
		})
	}
}

// 已配置 yt-dlp cookie（ytdlpConfigured=true）：同类风控失败不加引导（用户已知），
// 但重试提示本身保留。
func TestLoadFailHintNoGuideWhenConfigured(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m.ytdlpConfigured = true
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}})
	toast := activeToastText(m)
	if strings.Contains(toast, "配置 YT Music 登录") {
		t.Errorf("已配置 cookie 时 toast = %q, want 不含“配置 YT Music 登录”引导", toast)
	}
	if !strings.Contains(toast, "正在自动重试（1/2）") {
		t.Errorf("toast = %q, want 仍含“正在自动重试（1/2）”", toast)
	}
}

// 未配置 cookie 但失败与风控无关（网络解析失败）：不加 cookie 引导，避免噪音。
func TestLoadFailHintNoGuideForNonRiskFailure(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)

	m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: &player.LoadFailedError{FileError: "Couldn't resolve host: youtube.com"}}})
	if toast := activeToastText(m); strings.Contains(toast, "配置 YT Music 登录") {
		t.Errorf("非风控失败 toast = %q, want 不含“配置 YT Music 登录”引导", toast)
	}
}

// failureHint 方法级规则：未配置 + 风控类失败追加引导；已配置或非风控失败不加。
// 403 判定同时覆盖 hint 映射与 FileError 原始文本两条触发路径。
func TestFailureHint(t *testing.T) {
	guide := "可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效"
	riskCases := []*player.LoadFailedError{
		{FileError: "no audio or video data played"},     // hint 含“风控”
		{FileError: "yt-dlp: HTTP Error 403: Forbidden"}, // hint 含“拒绝访问”+ FileError 含 403
		{FileError: "request failed with status 403"},    // FileError 含 403 兜底
	}
	for _, le := range riskCases {
		if h := (Model{}).failureHint(le); !strings.Contains(h, guide) {
			t.Errorf("未配置 + %q: failureHint = %q, want 含 cookie 引导", le.FileError, h)
		}
	}
	// 已配置：同类失败不加引导
	if h := (Model{ytdlpConfigured: true}).failureHint(riskCases[0]); strings.Contains(h, guide) {
		t.Errorf("已配置 + 风控失败: failureHint = %q, want 不含引导", h)
	}
	// 未配置 + 非风控失败（含空 FileError）：不加引导
	for _, fileErr := range []string{"Couldn't resolve host: youtube.com", "", "mpv: file not found"} {
		if h := (Model{}).failureHint(&player.LoadFailedError{FileError: fileErr}); strings.Contains(h, guide) {
			t.Errorf("非风控失败 %q: failureHint = %q, want 不含引导", fileErr, h)
		}
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
// 其他页三段式：左 = 歌曲名 + 右 = 播放顺序（中间歌词区无歌词时留空）；
// 名称按剩余宽度 40% 截断、右侧顺序优先完整、不折行。
// （曾为左顺序右标题：标题按剩余宽度动态截断，曾按 m.width/2 固定截断致折行。）
func TestStatusBarLayout(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("一首名字特别长特别长特别长特别长特别长特别长的歌"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.WindowSizeMsg{Width: 40, Height: 24})

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
	// 标题以 "测试歌曲 " 开头，40% 截断后仍保留开头字符（更窄窗口如 21
	// 列名称会被全截掉——窄窗口兜底见 TestStatusBarNarrowWindowFits）
	titleIdx := strings.Index(bar, "歌")
	seqIdx := strings.Index(bar, "顺序")
	if titleIdx < 0 || seqIdx < 0 || titleIdx > seqIdx {
		t.Errorf("状态栏应为左名称右顺序, got %q", bar)
	}
	if w := ansi.StringWidth(bar); w > 40 {
		t.Errorf("状态栏行宽 = %d, want ≤ 40", w)
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

// TestStatusBarLyricCenter 回归：状态栏三段式——左名称 + 中间当前歌词行
// （居中）+ 右播放顺序；无歌词时中间留空。
func TestStatusBarLyricCenter(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m.switchPage("2") // 队列页（首页状态栏留空）

	// 无歌词：中间留空，左右仍在
	bar := strings.Split(m.View(), "\n")
	last := bar[len(bar)-1]
	if !strings.Contains(last, "t1") || !strings.Contains(last, "顺序") {
		t.Errorf("无歌词时状态栏应含左名称右顺序, got %q", last)
	}

	ly, err := lyrics.ParseLRC([]byte("[00:10.00]第一行歌词文本\n[00:20.00]第二行歌词文本\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	last = strings.Split(m.View(), "\n")[len(strings.Split(m.View(), "\n"))-1]
	if !strings.Contains(last, "第一行歌词文本") {
		t.Errorf("状态栏应显示当前歌词行, got %q", last)
	}
	// 高亮样式与首页歌词区一致：加粗 + 粉色 212。lipgloss 将 bold+颜色合并
	// 渲染为单序列 \x1b[1;38;5;212m（实测输出），故按实际形式断言：
	// SGR 参数 1（加粗）+ 256 色索引 212（粉色）。
	lipgloss.SetColorProfile(termenv.TrueColor)
	if !strings.Contains(last, "\x1b[1;") {
		t.Errorf("状态栏歌词行应加粗高亮, got %q", last)
	}
	if !strings.Contains(last, "38;5;212") {
		t.Errorf("状态栏歌词行应含粉色 212, got %q", last)
	}
	// 歌词行在左右之间（起始列 > 左名称列、歌词中心 ≈ 中间区域中心）
	vis := stripAnsiForTest(last)
	lIdx := strings.Index(vis, "t1")
	yIdx := strings.Index(vis, "第一行")
	sIdx := strings.Index(vis, "顺序")
	if !(lIdx >= 0 && yIdx > lIdx && sIdx > yIdx) {
		t.Errorf("状态栏应为 左名称 < 歌词 < 右顺序 布局, got %q", vis)
	}
	// 居中：歌词行中心 ≈ 中间区域中心（窗口中心 40 ± 6）。
	// strings.Index 返回的是字节偏移（CJK 每字 3 字节 ≠ 2 列），
	// 起始列须用 ansi.StringWidth 对前缀按列宽计算。
	lyricW := ansi.StringWidth("第一行歌词文本")
	lyricStart := ansi.StringWidth(vis[:yIdx])
	center := lyricStart + lyricW/2
	if center < 34 || center > 46 {
		t.Errorf("歌词行中心列 = %d, want ≈ 40（居中）, got %q", center, vis)
	}
	if w := ansi.StringWidth(last); w > 80 {
		t.Errorf("状态栏行宽 = %d, want ≤ 80", w)
	}

	// 超长歌词行：中间段截断含省略号，行宽恒 ≤ 80
	ly2, err := lyrics.ParseLRC([]byte("[00:10.00]" + strings.Repeat("很长的歌词", 20) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly2})
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 12}})
	last = strings.Split(m.View(), "\n")[len(strings.Split(m.View(), "\n"))-1]
	if !strings.Contains(stripAnsiForTest(last), "…") {
		t.Errorf("超长歌词行应中间截断含省略号, got %q", last)
	}
	if w := ansi.StringWidth(last); w > 80 {
		t.Errorf("超长歌词行状态栏行宽 = %d, want ≤ 80", w)
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
printf '\032\105\337\243' > "$out"
head -c 2044 /dev/zero >> "$out"
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
// 预加载集成后下载来源有两个：当前曲预热（TrackStarted）+ 下一首预载
// （refreshPreload，单曲回绕跳过），二者均与 beginPlay 解耦。
func TestTrackStartedTriggersCacheWarmup(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("依赖 POSIX sh 假 yt-dlp 脚本或系统根目录可读性差异（Access denied），Windows skip")
    }
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	logPath := filepath.Join(t.TempDir(), "ytdlp.log")
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, writeFakeYtDlpScript(t, logPath), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModelBaseWithCache(t, fp, fa, nil, nil, cm)

	// 手动播放 t1（缓存 miss）：beginPlay 不得触发下载；单曲队列预加载
	// 自回绕跳过（refreshPreload 不设目标），窗口内假 yt-dlp 不得被调用。
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

	// TrackStartedEvent（mpv 取流成功）→ 当前曲预热：后台下载启动（第 1 次调用）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Fatalf("TrackStarted 后 state.Track 应为 t1, got %+v", m.state.Track)
	}
	waitFor(t, 3*time.Second, func() bool { return ytDlpCallCount(logPath) >= 1 })

	// 追加 t2：预加载立即接管"下一首"的下载（第 2 次调用，无需等 TrackEnded）——
	// 下载发生在 beginPlay 之前，切歌时直接命中缓存秒切。
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	if got := targetID(m); got != "t2" {
		t.Fatalf("追加 t2 后预加载目标 = %q, want t2", got)
	}
	waitFor(t, 3*time.Second, func() bool { return ytDlpCallCount(logPath) >= 2 })

	// 连播：TrackEnded → beginPlay(t2) 不得再触发新下载（t2 已由预加载下载；
	// 预加载恰好在切歌前完成时命中缓存播本地文件，未完成时播网络 URL——两种
	// 时序都合法，故不断言播放路径，只断播放次数；"不触发新下载"由下载总数
	// 保持 2 覆盖，后续 TrackStarted 的预热对已缓存条目是 no-op）。
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 2 {
		t.Fatalf("TrackEnded 后应连播 t2（命中缓存秒切或网络取流）, playCount=%d", fp.playCount())
	}
	time.Sleep(200 * time.Millisecond)
	if got := ytDlpCallCount(logPath); got != 2 {
		t.Fatalf("连播 beginPlay（缓存 miss）不应触发缓存下载（应保持预加载的 2 次）, calls = %d", got)
	}
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

// TestStatusBarShowsAITitle AI 识别结果到达后，底部状态栏右侧显示清洗后
// 标题；切歌后回落新曲原始标题。
func TestStatusBarShowsAITitle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	raw := model.Track{ID: "t1", Title: "T1", Artist: "A", Duration: 200, URL: "http://x/1", Source: "youtube"}
	m, cmd := m.startPlay(raw)
	_ = execCmds(cmd)
	// 首页状态栏留空（master 布局），切到队列页：状态栏左侧显示曲目标题
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.current != pageQueue {
		t.Fatalf("Tab 后 current = %v, want pageQueue", m.current)
	}

	if got := m.View(); !strings.Contains(got, "T1 - A") {
		t.Errorf("AI 到达前状态栏应显示原始标题, got %q", got)
	}
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly, title: "晴天", artist: "周杰伦"})
	if got := m.View(); !strings.Contains(got, "晴天 - 周") {
		t.Errorf("状态栏应显示 AI 清洗标题, got %q", got)
	}
	if strings.Contains(m.View(), "T1 - A") {
		t.Errorf("状态栏不应再显示原始标题: %q", m.View())
	}
}

// TestAITrackNotifiesMPRIS AI 识别结果到达时，onTrack 回调收到清洗后
// 曲目（MPRIS 元数据同步）。
func TestAITrackNotifiesMPRIS(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	var got []*model.Track
	m := newTestModel(t, fp, fa, func(track *model.Track) {
		cp := *track
		got = append(got, &cp)
	})
	raw := model.Track{ID: "t1", Title: "T1", Artist: "A", Duration: 200, URL: "http://x/1", Source: "youtube"}
	m, cmd := m.startPlay(raw)
	_ = execCmds(cmd)
	if len(got) != 1 {
		t.Fatalf("startPlay 应通知 1 次, got %d", len(got))
	}
	ly, _ := lyrics.ParseLRC([]byte("[00:01.00]行\n"))
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly, title: "晴天", artist: "周杰伦"})
	if len(got) != 2 {
		t.Fatalf("AI 结果应再通知 1 次, got %d", len(got))
	}
	last := got[1]
	if last.Title != "晴天" || last.Artist != "周杰伦" || last.ID != "t1" {
		t.Errorf("AI 通知 = %+v, want 晴天/周杰伦/t1", last)
	}
	// 确定性结果（无 AI 信息）不触发额外通知
	m, _ = update(m, lyricsResultMsg{trackID: "t1", lyrics: ly})
	if len(got) != 2 {
		t.Errorf("无 AI 信息不应触发通知, got %d", len(got))
	}
}

// TestGlobalKeysYieldToFilter 队列页过滤聚焦时：空格/a/q 让位给过滤输入框，
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

// TestQueueFilterHintOnLastLine 过滤态提示行应在内容区最后一行（聚焦/确认两态）。
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

// TestGlobalKeysYieldToFilterHistory 历史页过滤聚焦时：空格/a/q 同样让位
// 给过滤输入框（与队列页同构的 typingText 分支）。
func TestGlobalKeysYieldToFilterHistory(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if err := m.history.Add(testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.historyPage.filterInput.Value() != " " {
		t.Errorf("空格应输入过滤词, got %q", m.historyPage.filterInput.Value())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.plPicker != nil {
		t.Fatal("过滤聚焦时 a 不应打开选择器")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatal("过滤聚焦时 q 不应退出")
		}
	}
}

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

	// 无当前曲时插队首：直接入队为首项，不触发播放
	fp0 := newFakePlayer()
	m0 := newTestModel(t, fp0, &fakeSearchAdapter{}, nil)
	m0, _ = update(m0, trackInsertNextMsg{track: testTrack("t0")})
	got0 := m0.queue.Tracks()
	if len(got0) != 1 || got0[0].ID != "t0" {
		t.Fatalf("Tracks = %+v, want [t0]", idsOf(got0))
	}
	if m0.queue.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1", m0.queue.CurrentIndex())
	}
	if fp0.playCount() != 0 {
		t.Errorf("无当前曲插入不应触发播放, playCount = %d", fp0.playCount())
	}
}

// TestQueueMoveRoundTripCurrentIdx 全链路（按键 → queueMoveMsg → 回灌）：
// 移动非当前曲时 currentIdx 不变；移动当前曲本身时 currentIdx 跟随同一首歌；
// 回灌后选中项跟随被移动曲目，且移动不触发播放。
func TestQueueMoveRoundTripCurrentIdx(t *testing.T) {
	// 阶段 1：队列 [t1▶,t2,t3] current=0，选中 t2 下移一格
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg)
	}
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // 队列页

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 t2
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyDown}) // t2(1) → 2
	mv := moveMsgOf(t, cmd)
	if mv.from != 1 || mv.to != 2 {
		t.Fatalf("queueMoveMsg from/to = %d/%d, want 1/2", mv.from, mv.to)
	}
	m, _ = update(m, mv) // 回灌 → queue=[t1,t3,t2]
	if got := idsOf(m.queue.Tracks()); !sameIDs(got, []string{"t1", "t3", "t2"}) {
		t.Fatalf("移动后队列 = %v, want [t1 t3 t2]", got)
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("移动非当前曲后 CurrentIndex = %d, want 0（t1 仍是当前曲）", m.queue.CurrentIndex())
	}
	if it, ok := m.queuePage.list.SelectedItem().(queueItem); !ok || it.track.ID != "t2" {
		t.Errorf("回灌后选中应跟随 t2, got %+v", it)
	}
	if fp.playCount() != 1 {
		t.Errorf("移动不应触发播放, playCount = %d", fp.playCount())
	}

	// 阶段 2（新模型）：移动当前曲本身 t1(0) → 1 → currentIdx 跟随到 1
	fp2 := newFakePlayer()
	m2 := newTestModel(t, fp2, &fakeSearchAdapter{}, nil)
	m2, cmd = m2.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m2, _ = update(m2, msg)
	}
	m2, _ = update(m2, trackAppendMsg{track: testTrack("t2")})
	m2, _ = update(m2, trackAppendMsg{track: testTrack("t3")})
	m2, _ = update(m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m2, _ = update(m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m2, cmd = update(m2, tea.KeyMsg{Type: tea.KeyDown}) // t1(0) → 1
	mv2 := moveMsgOf(t, cmd)
	if mv2.from != 0 || mv2.to != 1 {
		t.Fatalf("queueMoveMsg from/to = %d/%d, want 0/1", mv2.from, mv2.to)
	}
	m2, _ = update(m2, mv2) // 回灌 → queue=[t2,t1,t3]
	if got := idsOf(m2.queue.Tracks()); !sameIDs(got, []string{"t2", "t1", "t3"}) {
		t.Fatalf("移动当前曲后队列 = %v, want [t2 t1 t3]", got)
	}
	if m2.queue.CurrentIndex() != 1 {
		t.Errorf("移动当前曲后 CurrentIndex = %d, want 1（跟随同一首歌）", m2.queue.CurrentIndex())
	}
	if cur, _ := m2.queue.Current(); cur.ID != "t1" {
		t.Errorf("当前曲应仍为 t1, got %s", cur.ID)
	}
	if fp2.playCount() != 1 || fp2.lastPlayed() != testTrack("t1").URL {
		t.Errorf("移动当前曲不应触发播放: playCount=%d lastPlayed=%q", fp2.playCount(), fp2.lastPlayed())
	}
}

// TestQueueMoveThenTrackEndedPlaysNewNext 回归：移动跨越当前曲后 TrackEnded
// 自动连播新顺序的下一首（仿 TestTrackEndedAutoAdvances 的 fp.events 事件链模式）。
func TestQueueMoveThenTrackEndedPlaysNewNext(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg)
	}
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	// 队列页把 t2 移到队尾：队列 [t1▶,t3,t2]（当前曲 t1 不变）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 t2
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyDown}) // t2(1) → 2
	mv := moveMsgOf(t, cmd)
	if mv.from != 1 || mv.to != 2 {
		t.Fatalf("queueMoveMsg from/to = %d/%d, want 1/2", mv.from, mv.to)
	}
	m, _ = update(m, mv)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // 退出移动模式

	// t1 播完 → 自动连播新顺序的下一首 t3（移动前应为 t2）
	// 预推一个事件：TrackEnded 返回的 batch 含 waitForPlayerEvents（事件链），
	// 测试执行该 batch 时需有事件可消费（与 execRetryBatch 同款模式）。
	fp.events <- player.ProgressEvent{Position: 0, Duration: 200}
	m, cmd = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	for _, msg := range execCmds(cmd) {
		m, _ = update(m, msg)
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t3").URL {
		t.Fatalf("连播应播放新顺序下一首 t3: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("连播后 CurrentIndex = %d, want 1（t3）", m.queue.CurrentIndex())
	}
	if m.state.Track == nil || m.state.Track.ID != "t3" || !m.state.Playing {
		t.Errorf("连播后 state = %+v, want t3 播放中", m.state)
	}
}

// ---------------------------------------------------------------------------
// 缓存兜底播放（cache fallback）：mpv URL 取流失败 → 缓存命中立即切本地；
// 未命中则启动/接入下载并限时等待（fallbackWaitTimeout），完成后若仍未开始
// 播放自动切本地；超时或下载失败放弃等待恢复现有重试链路；暂停/切歌/删除当前
// 曲取消兜底；恢复路径不兜底；兜底等待中重复失败忽略。消息驱动（m.Update），
// 异步等待用轮询 + 总超时（waitUntil），不依赖固定 sleep 的计时器竞态。
//
// 模型组装复用 ui/cache_test.go 的 newCacheTestModelWithYtdlp（返回真实缓存
// Manager 与缓存目录）与 presetCache（预置缓存条目）——与既有缓存测试同一套
// 环境；本文件只补兜底状态机相关 helper 与用例。
// ---------------------------------------------------------------------------

// fakeYtDlpOK 写假 yt-dlp 脚本（下载成功版）到 t.TempDir() 并返回路径。
// 基于 writeFakeYtDlp（ui/cache_test.go，与 cache/download_test.go 的
// fakeYtDlpBody/fakeAudioOut 同款 -o 解析）：写 EBML/WebM 魔数 + 零填充到
// 2048 字节（≥ cache.MinAudioSize，内容校验通过）——CacheAsync 真实下载→注册
// 全链路走通。开头 sleep 0.05 是确定性守卫：兜底测试依赖“下载在途时 beginPlay
// 的 Lookup 必 miss”（产物 ≥50ms 后才落盘，同步的 beginPlay 不可能撞上命中）。
func fakeYtDlpOK(t *testing.T) string {
	t.Helper()
	return writeFakeYtDlp(t, `sleep 0.05
printf '\032\105\337\243' > "$out"
head -c 2044 /dev/zero >> "$out"`)
}

// fakeYtDlpFail 写假 yt-dlp 脚本（下载失败版）：立即 exit 1（模拟 yt-dlp 报错，
// 下载预算耗尽失败、不产出文件）。
func fakeYtDlpFail(t *testing.T) string {
	t.Helper()
	return writeFakeYtDlp(t, "exit 1")
}

// waitUntil 轮询等待条件成立（20ms 间隔，总超时 10s），超时 t.Fatal(msg)。
// 用于异步副作用（后台缓存下载完成等）断言——避免固定 sleep 引入计时器竞态。
// （与既有 waitFor(t, timeout, cond) 区分：Go 不支持重载，超时固定 10s + 带
// 语义化失败消息。）
func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// shrinkDownloadRetries 调小失败下载的重试间隔（假 yt-dlp 失败脚本下后台下载
// 预算耗尽从 ~8s 缩到 ~50ms），t.Cleanup 恢复原值。
func shrinkDownloadRetries(t *testing.T) {
	t.Helper()
	old := cache.DownloadRetryBackoff
	cache.DownloadRetryBackoff = 10 * time.Millisecond
	t.Cleanup(func() { cache.DownloadRetryBackoff = old })
}

// speedUpRetry 调小重试间隔与 toast 定时器（execRetryBatch 同步执行 batch 时
// 的 toast tick 会阻塞到定时器到期），t.Cleanup 恢复原值。
func speedUpRetry(t *testing.T) {
	t.Helper()
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	t.Cleanup(func() { retryBackoff = old })
	toastErrorDuration = time.Millisecond
	t.Cleanup(func() { toastErrorDuration = 5 * time.Second })
}

// loadFail 是标准取流失败事件（mpv end-file error）。
func loadFail() player.Event {
	return player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
}

// 缓存已就绪（缓存命中）：URL 取流失败立即改用缓存文件播放（不等待下载），
// 且只兜底一次——之后再失败（缓存文件损坏语义）移除条目、恢复 URL 重试，不再
// 重复切本地。注意顺序：先播放（此时缓存 miss → URL），再预置缓存文件——
// beginPlay 对已命中条目会直接播本地，无法构造“URL 失败后切本地”的兜底场景。
func TestCacheFallbackImmediateWhenCacheReady(t *testing.T) {
	speedUpRetry(t)
	m, fp, cm, dir := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr}) // 缓存 miss：播放 URL
	if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
		t.Fatalf("初始播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	local := presetCache(t, cm, dir, tr.ID) // 下载完成（缓存就绪）

	// URL 取流失败 → 缓存已命中：立即切本地（不等待下载、不走 URL 重试）
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if fp.playCount() != 2 || fp.lastPlayed() != local {
		t.Fatalf("应立即可用缓存文件: playCount=%d lastPlayed=%q, want %q", fp.playCount(), fp.lastPlayed(), local)
	}
	if !strings.Contains(activeToastText(m), "已改用缓存文件") {
		t.Errorf("toast = %q, want 含“已改用缓存文件”", activeToastText(m))
	}
	if !m.playingFromCache {
		t.Error("切本地后 playingFromCache 应为 true")
	}
	if m.fallback.active {
		t.Error("命中切换不应进入兜底等待")
	}

	// 再次失败（从缓存播放失败 = 缓存文件损坏）：不再兜底——条目被移除
	//（Lookup miss），恢复 URL 重试链路（重试是延迟 cmd，先断言不立即播放）
	m, cmd := update(m, playerEventMsg{ev: loadFail()})
	if _, ok := m.cache.Lookup(tr.ID); ok {
		t.Error("缓存文件播放失败后条目应被移除（不再兜底）")
	}
	if fp.playCount() != 2 {
		t.Errorf("重试是延迟 cmd，不应立即 Play: playCount=%d, want 2", fp.playCount())
	}
	toast := activeToastText(m)
	if !strings.Contains(toast, "缓存文件损坏") || !strings.Contains(toast, "自动重试") {
		t.Errorf("toast = %q, want 含“缓存文件损坏”与“自动重试”", toast)
	}
	// 执行重试：条目已移除 → 重新走 URL（而非本地）
	m = execRetryBatch(m, cmd, fp)
	if fp.playCount() != 3 || fp.lastPlayed() != tr.URL {
		t.Errorf("重试应重新走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
}

// 未缓存时取流失败：进入兜底等待（不立即 URL 重试），下载完成后（轮询 Lookup
// 命中）发 cacheFallbackDoneMsg → 改用本地缓存文件播放。
func TestCacheFallbackWaitsDownloadThenPlaysLocal(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("依赖 POSIX sh 假 yt-dlp 脚本或系统根目录可读性差异（Access denied），Windows skip")
    }
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
		t.Fatalf("初始播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}

	// 取流失败 → 启动下载并进入等待：不触发 URL 重试（Play 次数不增）
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	if m.fallback.trackID != tr.ID || m.fallback.gen != m.playGen {
		t.Errorf("fallback = %+v, want trackID=%s gen=%d", m.fallback, tr.ID, m.playGen)
	}
	if fp.playCount() != 1 {
		t.Fatalf("等待下载期间不应触发 URL 重试: playCount=%d, want 1", fp.playCount())
	}
	if !strings.Contains(activeToastText(m), "缓存下载中") {
		t.Errorf("toast = %q, want 含“缓存下载中”", activeToastText(m))
	}

	// 下载完成（轮询 Lookup 命中）→ 发完成消息 → 改用本地文件
	gen := m.playGen
	waitUntil(t, func() bool {
		_, ok := m.cache.Lookup(tr.ID)
		return ok
	}, "后台下载应完成并命中缓存")
	local, _ := m.cache.Lookup(tr.ID)
	m, _ = update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: gen})
	if fp.playCount() != 2 || fp.lastPlayed() != local {
		t.Fatalf("下载完成后应改用缓存文件: playCount=%d lastPlayed=%q, want %q", fp.playCount(), fp.lastPlayed(), local)
	}
	if !strings.Contains(activeToastText(m), "已改用缓存文件") {
		t.Errorf("toast = %q, want 含“已改用缓存文件”", activeToastText(m))
	}
	if !m.playingFromCache {
		t.Error("切本地后 playingFromCache 应为 true")
	}
	if m.fallback.active {
		t.Error("切本地后不应再处于兜底等待")
	}
}

// 下载在途时 beginPlay 注册的兜底监听（WaitDone）：下载完成后若 mpv 仍未开始
// 播放（playStarted=false）→ 切本地；若 TrackStartedEvent 已到（mpv 已开始
// 播放）→ 丢弃，保持 URL 播放（对照组子测试）。
func TestCacheFallbackDoneChecksPlayStarted(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("依赖 POSIX sh 假 yt-dlp 脚本或系统根目录可读性差异（Access denied），Windows skip")
    }
	t.Run("未开始播放则切本地", func(t *testing.T) {
		m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
		tr := testTrack("t1")

		done := m.cache.CacheAsync(tr) // 预热已在途下载
		if done == nil {
			t.Fatal("CacheAsync 应启动下载")
		}
		m, _ = update(m, trackSelectedMsg{track: tr}) // beginPlay 注册 WaitDone 监听
		if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
			t.Fatalf("在途下载期间播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
		}
		if m.playStarted {
			t.Fatal("TrackStarted 未到，playStarted 应为 false")
		}

		waitUntil(t, func() bool {
			_, ok := m.cache.Lookup(tr.ID)
			return ok
		}, "后台下载应完成并命中缓存")
		local, _ := m.cache.Lookup(tr.ID)
		m, _ = update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: m.playGen})
		if fp.playCount() != 2 || fp.lastPlayed() != local {
			t.Fatalf("下载完成且未开始播放应切本地: playCount=%d lastPlayed=%q, want %q", fp.playCount(), fp.lastPlayed(), local)
		}
		if !strings.Contains(activeToastText(m), "已改用缓存文件") {
			t.Errorf("toast = %q, want 含“已改用缓存文件”", activeToastText(m))
		}
	})

	t.Run("已开始播放则丢弃", func(t *testing.T) {
		m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
		tr := testTrack("t1")

		if done := m.cache.CacheAsync(tr); done == nil {
			t.Fatal("CacheAsync 应启动下载")
		}
		m, _ = update(m, trackSelectedMsg{track: tr})
		if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
			t.Fatalf("初始播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
		}
		waitUntil(t, func() bool {
			_, ok := m.cache.Lookup(tr.ID)
			return ok
		}, "后台下载应完成并命中缓存")

		// mpv 已开始播放（URL 取流成功）→ 完成消息应丢弃，保持 URL 播放
		m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
		if !m.playStarted {
			t.Fatal("TrackStarted 后 playStarted 应为 true")
		}
		m, _ = update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: m.playGen})
		if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
			t.Errorf("已开始播放后完成消息应丢弃: playCount=%d lastPlayed=%q, want 仍为 URL", fp.playCount(), fp.lastPlayed())
		}
		if m.playingFromCache {
			t.Error("丢弃后不应标记 playingFromCache")
		}
	})
}

// 兜底等待限时（fallbackWaitTimeout）到期：放弃等待，恢复现有重试链路
// （URL 重试，retryCount=1）——下载仍失败也无妨（超时先到）。
func TestCacheFallbackTimeoutFallsThroughToRetry(t *testing.T) {
	oldTimeout := fallbackWaitTimeout
	fallbackWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fallbackWaitTimeout = oldTimeout })
	speedUpRetry(t)
	shrinkDownloadRetries(t)

	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	if fp.playCount() != 1 {
		t.Fatalf("初始 playCount = %d, want 1", fp.playCount())
	}

	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}

	// 超时消息（fallbackTimeoutCmd 到期产物）→ 放弃等待，恢复重试链路
	gen := m.playGen
	m, cmd := update(m, cacheFallbackTimeoutMsg{gen: gen})
	if m.fallback.active {
		t.Error("超时后应退出兜底等待")
	}
	if m.retryCount != 1 {
		t.Fatalf("超时放弃后 retryCount = %d, want 1", m.retryCount)
	}
	if !strings.Contains(activeToastText(m), "自动重试") {
		t.Errorf("toast = %q, want 含“自动重试”", activeToastText(m))
	}

	// 执行重试 batch：URL 重试发生（Play 调用次数增加）
	m = execRetryBatch(m, cmd, fp)
	waitUntil(t, func() bool { return fp.playCount() >= 2 }, "超时放弃后应发生 URL 重试（Play 调用次数增加）")
	if fp.playCount() != 2 || fp.lastPlayed() != tr.URL {
		t.Errorf("重试应再次播放 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
}

// 下载失败（假 yt-dlp exit 1，预算耗尽）：WaitDone 信号关闭但 Lookup 未命中
// → cacheFallbackDoneMsg 走放弃分支 → retryOrSkipLoadFailure（URL 重试）。
func TestCacheFallbackDownloadFailedFallsThrough(t *testing.T) {
	speedUpRetry(t)
	shrinkDownloadRetries(t)

	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}

	// 下载彻底结束（inflight 清除 = WaitDone 返回 nil）且 Lookup 持续 miss
	gen := m.playGen
	waitUntil(t, func() bool { return m.cache.WaitDone(tr.ID) == nil }, "下载应结束（inflight 清除）")
	for i := 0; i < 5; i++ {
		if _, ok := m.cache.Lookup(tr.ID); ok {
			t.Fatal("下载失败后不应命中缓存")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 完成消息（下载失败）：放弃兜底 → 恢复重试链路
	m, cmd := update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: gen})
	if m.fallback.active {
		t.Error("下载失败后应退出兜底等待")
	}
	if m.retryCount != 1 {
		t.Fatalf("放弃后 retryCount = %d, want 1", m.retryCount)
	}
	if !strings.Contains(activeToastText(m), "自动重试") {
		t.Errorf("toast = %q, want 含“自动重试”", activeToastText(m))
	}
	if fp.playCount() != 1 {
		t.Fatalf("重试是延迟 cmd，不应立即 Play: playCount=%d, want 1", fp.playCount())
	}

	// 执行重试 batch：URL 再次播放
	m = execRetryBatch(m, cmd, fp)
	waitUntil(t, func() bool { return fp.playCount() >= 2 }, "放弃后应发生 URL 重试")
	if fp.playCount() != 2 || fp.lastPlayed() != tr.URL {
		t.Errorf("重试应再次播放 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
}

// 兜底只发生一次：切本地后再失败（缓存文件损坏语义）不再次兜底——条目被移除
// （Lookup miss），重试走 URL 而非本地（回归：避免“URL 失败→切本地→本地损坏→
// 重下同一 URL→再失败”的死循环）。
func TestCacheFallbackOnlyOnce(t *testing.T) {
	speedUpRetry(t)
	m, fp, cm, dir := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr}) // 缓存 miss：URL
	presetCache(t, cm, dir, tr.ID)                // 缓存就绪
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	local, _ := m.cache.Lookup(tr.ID)
	if fp.lastPlayed() != local {
		t.Fatalf("第一次失败应切本地: lastPlayed=%q, want %q", fp.lastPlayed(), local)
	}

	// 再次失败：不再兜底——条目移除（Lookup miss），重试走 URL
	m, cmd := update(m, playerEventMsg{ev: loadFail()})
	if _, ok := m.cache.Lookup(tr.ID); ok {
		t.Error("二次失败后缓存条目应被移除（Remove 被调用）")
	}
	toast := activeToastText(m)
	if !strings.Contains(toast, "缓存文件损坏") || !strings.Contains(toast, "自动重试") {
		t.Errorf("toast = %q, want 含“缓存文件损坏”与“自动重试”", toast)
	}
	m = execRetryBatch(m, cmd, fp)
	if fp.lastPlayed() == local {
		t.Error("重试不应再播本地（兜底只发生一次）")
	}
	if fp.lastPlayed() != tr.URL {
		t.Errorf("重试应走 URL: lastPlayed=%q, want %q", fp.lastPlayed(), tr.URL)
	}
}

// 兜底等待期间用户暂停：取消兜底（ended + active=false + canceled=true +
// toast），下载完成消息随后到达 → 丢弃（不再播放）。
func TestCacheFallbackPauseCancels(t *testing.T) {
	shrinkDownloadRetries(t)
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	gen := m.playGen

	// 暂停 = 取消兜底（转停止态，空格可重播）
	m, _ = update(m, togglePlayMsg{})
	if !m.ended {
		t.Error("取消后 ended 应为 true")
	}
	if m.fallback.active {
		t.Error("取消后 fallback.active 应为 false")
	}
	if !m.fallback.canceled {
		t.Error("取消后 fallback.canceled 应为 true")
	}
	if m.state.Playing {
		t.Error("取消后 Playing 应为 false")
	}
	if !strings.Contains(activeToastText(m), "已取消缓存兜底") {
		t.Errorf("toast = %q, want 含“已取消缓存兜底”", activeToastText(m))
	}

	// 下载完成消息（取消后到达）：丢弃，不再播放
	m, _ = update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: gen})
	if fp.playCount() != 1 {
		t.Errorf("取消后完成消息应丢弃（不再 Play）: playCount=%d, want 1", fp.playCount())
	}
	if fp.lastPlayed() != tr.URL {
		t.Errorf("取消后不应切本地: lastPlayed=%q", fp.lastPlayed())
	}
}

// 兜底等待期间用户切歌：播放代际递增，旧曲完成消息（gen 不匹配）丢弃——
// 最后播放的是新曲 URL，无本地切换。
func TestCacheFallbackSwitchTrackCancels(t *testing.T) {
	shrinkDownloadRetries(t)
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	trackA, trackB := testTrack("t1"), testTrack("t2")

	m, _ = update(m, trackSelectedMsg{track: trackA})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	genA := m.playGen // G1

	// 切到 B：代际递增，兜底状态作废
	m, _ = update(m, trackSelectedMsg{track: trackB})
	if fp.playCount() != 2 || fp.lastPlayed() != trackB.URL {
		t.Fatalf("切歌后应播放 B 的 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if m.playGen == genA {
		t.Fatal("切歌后播放代际应递增")
	}
	if m.fallback.active {
		t.Error("切歌后不应再处于兜底等待")
	}

	// 旧曲 A 的完成消息（gen=G1）：丢弃
	m, _ = update(m, cacheFallbackDoneMsg{trackID: trackA.ID, gen: genA})
	if fp.playCount() != 2 || fp.lastPlayed() != trackB.URL {
		t.Errorf("旧曲完成消息应丢弃: playCount=%d lastPlayed=%q, want 仍为 B 的 URL", fp.playCount(), fp.lastPlayed())
	}
	if m.playingFromCache {
		t.Error("丢弃后不应标记 playingFromCache")
	}
}

// 续播恢复路径不参与缓存兜底：恢复加载（PlayPaused）后取流失败走“恢复播放
// 失败”分支（状态重置 + 横幅），不触发兜底等待（Play 次数不变、fallback 不激活）。
func TestCacheFallbackResumeNotApplied(t *testing.T) {
	m, fp := newResumeTestModel(t, sessionState(66.6, false), nil)
	if !m.resuming {
		t.Fatal("恢复场景应置 resuming 标记")
	}

	msgs := execCmds(resumeCmd(m))
	m, cmd := update(m, msgs[0])
	_ = execCmds(cmd) // 歌词/封面等与本测试无关
	if fp.pausedCount() != 1 {
		t.Fatalf("恢复应 PlayPaused: paused=%d", fp.pausedCount())
	}

	// 恢复加载取流失败 → “恢复播放失败”分支（不兜底）
	m, cmd = update(m, playerEventMsg{ev: loadFail()})
	if !strings.Contains(activeToastText(m), "恢复播放失败") {
		t.Errorf("toast = %q, want 含“恢复播放失败”", activeToastText(m))
	}
	if fp.playCount() != 0 {
		t.Errorf("恢复失败不应调用 Play: playCount=%d", fp.playCount())
	}
	if m.fallback.active {
		t.Error("恢复失败不应激活缓存兜底")
	}
	if m.resuming {
		t.Error("恢复失败处理后 resuming 应复位")
	}
	if m.state.Track != nil || m.state.Playing {
		t.Errorf("恢复失败后状态应重置: %+v", m.state)
	}
	if cmd == nil {
		t.Fatal("恢复失败后事件监听链应存活（cmd 应为非 nil）")
	}
}

// 兜底等待期间删除当前曲（queueSkip=true）：完成消息丢弃（不再播放），
// queueSkip 保持（删除解耦语义不受干扰）。
func TestCacheFallbackDeleteCurrentCancels(t *testing.T) {
	shrinkDownloadRetries(t)
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	gen := m.playGen

	// 删除当前曲：queueSkip 置位
	m, _ = update(m, queueDeleteMsg{index: m.queue.CurrentIndex()})
	if !m.queueSkip {
		t.Fatal("删除当前曲后应置 queueSkip")
	}

	// 完成消息：丢弃（不再 Play），queueSkip 保持
	m, _ = update(m, cacheFallbackDoneMsg{trackID: tr.ID, gen: gen})
	if fp.playCount() != 1 {
		t.Errorf("删除当前曲后完成消息应丢弃（不再 Play）: playCount=%d, want 1", fp.playCount())
	}
	if !m.queueSkip {
		t.Error("丢弃后 queueSkip 应保持")
	}
}

// 兜底等待中再次收到播放失败：忽略（不重复启动下载、不消耗重试预算），
// 等待状态保持，由下载完成/超时统一收口。
func TestCacheFallbackActiveIgnoresSecondError(t *testing.T) {
	shrinkDownloadRetries(t)
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	if fp.playCount() != 1 {
		t.Fatalf("初始 playCount = %d, want 1", fp.playCount())
	}

	// 等待中再次失败：忽略——不重复启动（Play 次数不变）、active 保持、
	// retryCount 不消耗、toast 不变（等待继续）
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if fp.playCount() != 1 {
		t.Errorf("二次失败不应重复启动: playCount=%d, want 1", fp.playCount())
	}
	if !m.fallback.active {
		t.Error("二次失败后兜底等待应保持 active")
	}
	if m.retryCount != 0 {
		t.Errorf("二次失败不应消耗重试预算: retryCount=%d, want 0", m.retryCount)
	}
	if !strings.Contains(activeToastText(m), "缓存下载中") {
		t.Errorf("toast = %q, want 仍为等待提示（含“缓存下载中”）", activeToastText(m))
	}
}

// 回归（审查 P1）：mpv 取流悬挂 → LoadTimeoutError 激活兜底（active=true）→
// 取流恢复 TrackStartedEvent 只置 playStarted 不清 fallback.active → 90s 超时
// 消息（gen 未变、active 仍 true）对正在播放的曲目伪重试（retryCount=1 +
// 自动重试 toast + retryPlayCmd 重播当前曲，用户可听中断/跳回 0:00）。修复：
// TrackStarted 复位 fallback（active=false），超时消息再丢弃（playStarted 门控）。
func TestCacheFallbackTrackStartedResetsActive(t *testing.T) {
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
		t.Fatalf("初始播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}

	// 取流失败 → 兜底激活（等待下载，阻断 URL 重试，retryCount 不消耗）
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}
	if m.retryCount != 0 {
		t.Fatalf("兜底等待不应消耗重试预算: retryCount=%d, want 0", m.retryCount)
	}

	// mpv 取流恢复：TrackStartedEvent → 兜底状态必须复位（active=false），
	// 否则超时消息会对正在播放的曲目伪重试（P1）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 100}})
	if !m.playStarted {
		t.Fatal("TrackStarted 后 playStarted 应为 true")
	}
	if m.fallback.active {
		t.Error("TrackStarted 后 fallback.active 应复位为 false（兜底状态作废）")
	}

	// 超时消息（悬挂期间 fallbackTimeoutCmd 的到期产物，gen 未变）→ 丢弃：
	// 不触发 URL 重试（Play 不增）、retryCount 不增、无“自动重试”toast
	gen := m.playGen
	m, cmd := update(m, cacheFallbackTimeoutMsg{gen: gen})
	_ = cmd
	if fp.playCount() != 1 {
		t.Errorf("已开始播放后超时消息应丢弃（不再 Play）: playCount=%d, want 1", fp.playCount())
	}
	if m.retryCount != 0 {
		t.Errorf("已开始播放后超时消息不应消耗重试预算: retryCount=%d, want 0", m.retryCount)
	}
	if strings.Contains(activeToastText(m), "自动重试") {
		t.Errorf("toast = %q, want 不含“自动重试”", activeToastText(m))
	}
}

// 回归（审查 P2）：togglePlay 取消兜底转停止态（ended=true）但未清空预加载
// 目标——与其余停止路径（stopAfterEnd/ErrorEvent ended 分支）不一致：已取消
// 的播放不应再预载下一首。修复：取消路径补 refreshPreload()（ended 门控）。
func TestCacheFallbackPauseCancelRefreshesPreload(t *testing.T) {
	m, fp, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpFail(t))
	tr1, tr2 := testTrack("t1"), testTrack("t2")

	m, _ = update(m, trackSelectedMsg{track: tr1})
	if fp.playCount() != 1 || fp.lastPlayed() != tr1.URL {
		t.Fatalf("初始播放应走 URL: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	// 队列追加下一首 → 预加载目标建立（tr2）
	m, _ = update(m, trackAppendMsg{track: tr2})
	if tgt := m.preloader.Target(); tgt == nil || tgt.ID != tr2.ID {
		t.Fatalf("追加下一首后预加载目标应为 %s: got %+v", tr2.ID, tgt)
	}

	// 取流失败 → 兜底等待
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if !m.fallback.active {
		t.Fatal("取流失败后应进入缓存兜底等待")
	}

	// 暂停取消兜底 → 停止态：预加载目标必须清空（ended 门控，与其他停止路径一致）
	m, _ = update(m, togglePlayMsg{})
	if !m.ended {
		t.Fatal("取消兜底后 ended 应为 true")
	}
	if m.preloader.Target() != nil {
		t.Errorf("取消兜底后预加载目标应清空: got %+v, want nil", m.preloader.Target())
	}
}

// ---------------------------------------------------------------------------
// 本地曲目（Source=local）播放链路完全绕过网络缓存
// ---------------------------------------------------------------------------
// 本地文件已在磁盘，缓存命中/下载/兜底对本地曲目无意义：beginPlay 不查
// Lookup、不注册 WaitDone 兜底监听；TrackStarted 不预热；预加载不预载本地
// 下一首；续播恢复不查 Lookup。cache 层 CacheAsync 另有 Source=local no-op
// 防御（cache/cache_test.go TestCacheAsyncLocalTrackNoOp）。

// localTestTrack 构造本地曲目：ID/URL 均为绝对路径（与 local 包扫描产物
// 同构），播放链路应直接交给 mpv 播放、完全不触碰网络缓存。
func localTestTrack() model.Track {
	return model.Track{
		ID:       "/data/music/测试本地歌曲.mp3",
		Title:    "测试本地歌曲",
		Artist:   "测试歌手",
		Duration: 200,
		URL:      "/data/music/测试本地歌曲.mp3",
		Source:   model.SourceLocal,
		CoverURL: "",
	}
}

// beginPlay 本地曲目：不查 Lookup（playingFromCache 恒 false，mpv 直接播放
// 本地绝对路径）、不注册 WaitDone 兜底监听。验证手法：预置同名 ID 的在途下载
// （假 yt-dlp 50ms 后落盘成功）——若 beginPlay 误注册 WaitDone 监听，下载完成
// 信号到达后缓存兜底分支会命中缓存并重播本地文件（playCount=2 + “已改用缓存
// 文件” toast）；正确实现则无监听、无重播（防未来调用点对本地曲目启动下载的
// 极端场景）。
func TestBeginPlayLocalTrackSkipsCache(t *testing.T) {
	m, fp, cm, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
	tr := localTestTrack()

	// 伪造同名 ID 的在途下载（Source=youtube，仅 ID 与本地曲目相同）：
	// beginPlay 若误查 WaitDone 会拿到该信号并注册兜底监听
	done := cm.CacheAsync(model.Track{ID: tr.ID, URL: "https://youtube.com/watch?v=x"})
	if done == nil {
		t.Fatal("前置：应在途下载")
	}
	if cm.WaitDone(tr.ID) == nil {
		t.Fatal("前置：inflight 应已注册")
	}

	m, cmd := m.startPlay(tr)
	// 展开 batch 并回灌结果（测试驱动方式，与既有缓存测试一致）：
	// 若本地曲目误注册了 WaitDone 监听，这里会等到下载完成并触发缓存兜底重播
	m, _ = update(m, cmd().(tea.BatchMsg))
	if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
		t.Fatalf("本地曲目应直接播放本地绝对路径且不触发缓存兜底重播: playCount=%d lastPlayed=%q, want 1/%q", fp.playCount(), fp.lastPlayed(), tr.URL)
	}
	if m.playingFromCache {
		t.Error("本地曲目 playingFromCache 应为 false（跳过 Lookup）")
	}
	if strings.Contains(activeToastText(m), "已改用缓存文件") {
		t.Errorf("本地曲目不应注册 WaitDone 兜底监听: toast = %q", activeToastText(m))
	}
}

// TrackStartedEvent 预热：本地曲目不触发 CacheAsync（网络缓存对本地文件无意义）。
// 假 yt-dlp 脚本记录调用：播放本地曲目 + TrackStarted 后不得有任何下载调用，
// 也不得产生 inflight 条目。
func TestTrackStartedLocalSkipsCacheWarmup(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	logPath := filepath.Join(t.TempDir(), "ytdlp.log")
	cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, writeFakeYtDlpScript(t, logPath), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModelBaseWithCache(t, fp, fa, nil, nil, cm)
	tr := localTestTrack()

	m, cmd := m.startPlay(tr)
	_ = execCmds(cmd)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	time.Sleep(200 * time.Millisecond) // 若误触发下载，假脚本会立即执行
	if got := ytDlpCallCount(logPath); got != 0 {
		t.Fatalf("本地曲目 TrackStarted 后不应触发缓存预热下载, calls = %d", got)
	}
	if done := cm.WaitDone(tr.ID); done != nil {
		t.Fatalf("本地曲目不应产生 inflight 下载条目")
	}
}

// refreshPreload：队列下一首是本地曲目时不 SetTarget——本地文件无需预下载
// （预载目标保持空，不跳过本地曲目向后找下一首网络曲目）。
func TestPreloadLocalNextSkipsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// 追加本地曲目为下一首 + TrackStarted：PeekNext 命中本地曲目 → 不设目标
	m, _ = update(m, trackAppendMsg{track: localTestTrack()})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if got := targetID(m); got != "" {
		t.Fatalf("本地下一首不应设置预加载目标, got %q, want 空", got)
	}
	// 再追加一首网络曲目：下一首仍为本地曲目 → 目标保持空（不跳过本地向后找）
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})
	if got := targetID(m); got != "" {
		t.Fatalf("下一首为本地曲目时预加载目标应保持空, got %q", got)
	}
}

// 续播恢复：本地曲目跳过缓存 Lookup（fromCache 恒 false，直接 PlayPaused
// 本地绝对路径）——即使缓存中预置了同名条目也不得命中（本地文件不参与缓存，
// 命中会错误地把播放目标替换成缓存文件路径）。
func TestResumeLocalTrackSkipsCacheLookup(t *testing.T) {
	tr := localTestTrack()
	q := queue.New()
	q.Replace(tr)
	q.Add(testTrack("c")) // 下一首：保证队列非空，恢复路径正常建立
	st := &session.State{Queue: q.Snapshot(), Position: 10, Ended: false}
	m, fp, cm, dir := newCacheTestModelWithYtdlp(t, st, "/nonexistent/yt-dlp")

	// 预置同名缓存条目：若 resumeCmd 误查 Lookup 会命中并播放缓存文件
	presetCache(t, cm, dir, tr.ID)

	msgs := execCmds(resumeCmd(m))
	var res resumeResultMsg
	for _, msg := range msgs {
		if r, ok := msg.(resumeResultMsg); ok {
			res = r
		}
	}
	if res.fromCache {
		t.Fatal("本地曲目恢复 fromCache 应为 false（跳过 Lookup）")
	}
	if fp.pausedCount() != 1 || fp.lastPaused() != tr.URL {
		t.Fatalf("恢复应 PlayPaused 本地绝对路径: paused=%d last=%q, want %q", fp.pausedCount(), fp.lastPaused(), tr.URL)
	}
	m, _ = update(m, msgs[0])
	if m.playingFromCache {
		t.Error("本地曲目恢复后 playingFromCache 应为 false")
	}
}

// ---- 播放列表本地路径导入（root 编排：plLocalAddMsg → 目录校验 → 根路径防护 → Scan → Create → AddTracks） ----

// 本地目录导入成功：输入目录 → 自动新建「本地-<目录名>」列表 + 歌曲入库 +
// 成功 toast + 退出输入 + 概览出现新列表（不再依赖选中列表）。
func TestPlaylistLocalAddMsgSuccess(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	dir := t.TempDir()
	for _, f := range []string{"a.mp3", "b.flac", "c.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 无关扩展名被 local.Scan 过滤
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	listName := "本地-" + filepath.Base(dir)
	// 模拟真实流程：先按 l 进入输入模式（成功后退出输入由 root 完成）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.plPage.mode != plLocalAdd {
		t.Fatal("l 后应进入本地路径输入模式")
	}
	m, _ = update(m, plLocalAddMsg{path: dir}) // toast 过期 tick cmd 忽略（同既有测试）
	if got := activeToastText(m); got != fmt.Sprintf("已从 %s 添加 3 首到「%s」", dir, listName) {
		t.Errorf("toast = %q, want 已从 %s 添加 3 首到「%s」", got, dir, listName)
	}
	trs := m.pl.Tracks(listName)
	if len(trs) != 3 || trs[0].Source != model.SourceLocal {
		t.Fatalf("添加后歌曲 = %+v, want 3 首本地来源", trs)
	}
	if m.plPage.mode != plOverview || m.plPage.typing() {
		t.Errorf("成功后应退出输入回概览: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
	// 概览自动出现新列表且计数刷新
	view := stripANSI(m.plPage.view())
	if !strings.Contains(view, listName) {
		t.Errorf("概览应出现自动新建的列表 %q, got %q", listName, view)
	}
	if !strings.Contains(view, "3 首") {
		t.Errorf("列表页应显示 3 首, got %q", view)
	}
}

// 本地目录导入：单文件路径 → 拒绝（仅支持目录）+ 不新建列表 + 留在输入框可重试。
func TestPlaylistLocalAddMsgFileRejected(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	file := filepath.Join(t.TempDir(), "solo.mp3")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m, _ = update(m, plLocalAddMsg{path: file})
	if m.toast == nil || m.toast.kind != toastError || !strings.Contains(m.toast.text, "仅支持目录路径") {
		t.Errorf("toast = %+v, want toastError 且含「仅支持目录路径」", m.toast)
	}
	if len(m.pl.Lists()) != 0 {
		t.Errorf("单文件被拒绝不应新建列表: %+v", m.pl.Lists())
	}
	if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
		t.Errorf("失败应留在输入框可重试: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
}

// 本地目录导入失败：路径不存在 → toastError、不新建列表、留在输入模式（可重试）。
func TestPlaylistLocalAddMsgFailure(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	missing := filepath.Join(t.TempDir(), "不存在")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m, _ = update(m, plLocalAddMsg{path: missing})
	if m.toast == nil || m.toast.kind != toastError || !strings.Contains(m.toast.text, "路径不存在") {
		t.Errorf("toast = %+v, want toastError 且含路径不存在", m.toast)
	}
	if len(m.pl.Lists()) != 0 {
		t.Error("失败不应新建列表")
	}
	if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
		t.Errorf("失败应留在输入框可重试: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
}

// 本地目录导入根路径：输入 "/"（或 "."）→ 根路径退化，无目录名可取 →
// toastError 含「无法从该路径生成列表名」、不新建列表、留在输入框可重试。
// 防护必须先于 local.Scan 触发：否则 "/" 会先递归扫描整个文件系统
// （本测试快速安全正依赖此顺序）。
func TestPlaylistLocalAddMsgRootPath(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("依赖 POSIX sh 假 yt-dlp 脚本或系统根目录可读性差异（Access denied），Windows skip")
    }
	for _, p := range []string{string(os.PathSeparator), "."} {
		m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		m, _ = update(m, plLocalAddMsg{path: p})
		if m.toast == nil || m.toast.kind != toastError || !strings.Contains(m.toast.text, "无法从该路径生成列表名") {
			t.Errorf("path=%q: toast = %+v, want toastError 且含「无法从该路径生成列表名」", p, m.toast)
		}
		if len(m.pl.Lists()) != 0 {
			t.Errorf("path=%q: 根路径被拒绝不应新建列表: %+v", p, m.pl.Lists())
		}
		if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
			t.Errorf("path=%q: 失败应留在输入框可重试: mode=%v typing=%v", p, m.plPage.mode, m.plPage.typing())
		}
	}
}

// 本地目录导入：空目录（无音频文件）→ toastError 含「目录中没有找到支持的音频文件」、
// 不新建列表、留在输入模式（可重试）。
func TestPlaylistLocalAddMsgEmptyDir(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	dir := t.TempDir() // 空目录：无任何音频文件
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m, _ = update(m, plLocalAddMsg{path: dir})
	if m.toast == nil || m.toast.kind != toastError || !strings.Contains(m.toast.text, "目录中没有找到支持的音频文件") {
		t.Errorf("toast = %+v, want toastError 且含「目录中没有找到支持的音频文件」", m.toast)
	}
	if len(m.pl.Lists()) != 0 {
		t.Error("空目录失败不应新建列表")
	}
	if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
		t.Errorf("失败应留在输入框可重试: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
}

// 本地目录导入重名：已存在「本地-<目录名>」→ toastError 含「已存在同名列表」、
// 留在输入框、不重复添加。
func TestPlaylistLocalAddMsgDuplicateName(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	listName := "本地-" + filepath.Base(dir)
	if _, err := m.pl.Create(listName); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m, _ = update(m, plLocalAddMsg{path: dir})
	if m.toast == nil || m.toast.kind != toastError || !strings.Contains(m.toast.text, "已存在同名列表") {
		t.Errorf("toast = %+v, want toastError 且含「已存在同名列表」", m.toast)
	}
	if trs := m.pl.Tracks(listName); len(trs) != 0 {
		t.Errorf("重名失败不应添加歌曲, got %+v", trs)
	}
	if len(m.pl.Lists()) != 1 {
		t.Errorf("重名失败不应新建列表, got %+v", m.pl.Lists())
	}
	if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
		t.Errorf("失败应留在输入框可重试: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
}

// 本地目录导入：目录路径带尾部斜杠 → filepath.Clean 生效，列表名仍为「本地-<目录名>」。
func TestPlaylistLocalAddMsgTrailingSlash(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	base := "music"
	dir := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	withSlash := dir + string(os.PathSeparator)
	m, _ = update(m, plLocalAddMsg{path: withSlash})
	listName := "本地-" + base
	if got := activeToastText(m); got != fmt.Sprintf("已从 %s 添加 1 首到「%s」", withSlash, listName) {
		t.Errorf("toast = %q, want 已从 %s 添加 1 首到「%s」", got, withSlash, listName)
	}
	if trs := m.pl.Tracks(listName); len(trs) != 1 {
		t.Fatalf("添加后歌曲 = %+v, want 1 首", trs)
	}
	if m.plPage.mode != plOverview {
		t.Errorf("成功后应退出输入回概览: mode=%v", m.plPage.mode)
	}
}

// 本地目录导入：目录名含空格/中文 → 列表名完整保留「本地-<目录名>」。
func TestPlaylistLocalAddMsgChineseDirName(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	base := "我的 音乐"
	dir := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, plLocalAddMsg{path: dir})
	listName := "本地-" + base
	if got := activeToastText(m); got != fmt.Sprintf("已从 %s 添加 1 首到「%s」", dir, listName) {
		t.Errorf("toast = %q, want 已从 %s 添加 1 首到「%s」", got, dir, listName)
	}
	if trs := m.pl.Tracks(listName); len(trs) != 1 {
		t.Fatalf("添加后歌曲 = %+v, want 1 首", trs)
	}
	if m.plPage.mode != plOverview {
		t.Errorf("成功后应退出输入回概览: mode=%v", m.plPage.mode)
	}
}

// 本地路径输入模式（plLocalAdd）下数字键让位给输入框：路径含数字极常见
// （如 "/Music/2024/03 - 歌曲.mp3"），数字切页会打断输入（其余页面输入框
// 保持"数字始终切页"的既有语义，TestTabSwitchesPages 回归）。
func TestPlaylistLocalAddDigitsTypeNotSwitchPage(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}) // 切到播放列表页
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}) // 进入本地路径输入
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("0")})
	if m.current != pagePlaylists {
		t.Fatalf("本地路径输入时按 2/0 不应切页: current=%v", m.current)
	}
	if got := m.plPage.input.Value(); got != "20" {
		t.Errorf("input = %q, want %q", got, "20")
	}
	// 输入模式之外数字仍切页（既有语义不回归）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	if m.current != pageHistory {
		t.Errorf("退出输入后按 5 应切到历史页, got %v", m.current)
	}
}

// 本地曲目取流失败（LoadFailed）：不进入缓存兜底（本地文件失败 = 文件损坏/
// 被删，无下载可兜——cache 层 CacheAsync 对 local 是 no-op，root 层显式跳过
// 与 beginPlay/resumeCmd 口径一致），直接走重试/跳过链路。
func TestErrorEventLocalSkipsCacheFallback(t *testing.T) {
	m, _, _, _ := newCacheTestModelWithYtdlp(t, nil, fakeYtDlpOK(t))
	m, _ = update(m, trackSelectedMsg{track: localTestTrack()})
	m, _ = update(m, playerEventMsg{ev: loadFail()})
	if m.fallback.active {
		t.Fatal("本地曲目取流失败不应进入缓存兜底等待")
	}
	// 走重试链路（本地文件损坏重试无意义但无害，与失败曲目统一处理）：
	// 不启动任何下载（兜底分支是唯一会启动下载的路径，已排除）
	if !m.loadingSince.IsZero() {
		t.Error("失败后不应处于加载中")
	}
}

// ---- 卡住自动重启（StalledEvent → Restart + 重播；上限 + 协调） ----

// execStallBatch 执行 handleStall 返回的 batch（tea.BatchMsg：toast tick +
// stalledRestartCmd + waitForPlayerEvents）。waitForPlayerEvents 阻塞在事件
// 通道，须先预推一个普通事件让其返回；toast tick 会阻塞 toastDuration（警告 5s）
// ——与 execRetryBatch 同款取舍（Update 的 BatchMsg 分支同步执行每个子 cmd）。
// 返回 batch 处理后的 Model。
func execStallBatch(t *testing.T, m Model, cmd tea.Cmd, fp *fakePlayer) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	fp.events <- player.ProgressEvent{Position: 0, Duration: 200}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return m
	}
	m2, _ := update(m, batch)
	return m2
}

// 卡住 → Restart + 重播同曲（Play 次数 +1，URL 不变）；重启计数保留供上限判定。
func TestStallRestartReplaysSameTrack(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	if fp.playCount() != 1 || fp.lastPlayed() != tr.URL {
		t.Fatalf("初始播放: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	// file-loaded → playStarted（stall 前提）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if !m.playStarted {
		t.Fatal("TrackStarted 后 playStarted 应为 true")
	}

	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	if !strings.Contains(activeToastText(m), "重启播放器") {
		t.Errorf("toast = %q, want 含“重启播放器”", activeToastText(m))
	}
	if m.stallRestarts != 1 {
		t.Errorf("stallRestarts = %d, want 1", m.stallRestarts)
	}
	if cmd == nil {
		t.Fatal("StalledEvent 后应返回重启 batch")
	}

	// 执行 batch：Restart 调一次 + 重播同曲
	m = execStallBatch(t, m, cmd, fp)
	if fp.restartCount() != 1 {
		t.Errorf("Restart 应被调用一次: %d", fp.restartCount())
	}
	if fp.playCount() != 2 || fp.lastPlayed() != tr.URL {
		t.Errorf("重播同曲: playCount=%d lastPlayed=%q, want 2/%q", fp.playCount(), fp.lastPlayed(), tr.URL)
	}
	if m.stallRestarts != 1 {
		t.Errorf("重播后 stallRestarts 应保留 1（供上限判定）: %d", m.stallRestarts)
	}
	if m.playStarted {
		t.Error("重播后 playStarted 应复位 false（等待新的 TrackStarted）")
	}
}

// 卡住时缓存兜底等待中（fallback.active）→ 不重启（下载完自动切本地更优）。
func TestStallRestartSkipsWhenFallbackActive(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m.fallback = fallbackState{active: true, trackID: tr.ID, gen: m.playGen}

	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 0 {
		t.Errorf("兜底进行中不应重启: restarts=%d", fp.restartCount())
	}
	if fp.playCount() != 1 {
		t.Errorf("兜底进行中不应重播: playCount=%d, want 1", fp.playCount())
	}
}

// 恢复（PlayPaused 静默加载）期间卡住 → 不重启（重启发声破坏恢复语义）。
func TestStallRestartSkipsWhenResuming(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m.resuming = true

	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 0 || fp.playCount() != 1 {
		t.Errorf("恢复中不应重启/重播: restarts=%d playCount=%d", fp.restartCount(), fp.playCount())
	}
}

// TrackStarted 未到（playStarted=false）→ 忽略（本机制只管“已加载未推进”）。
func TestStallRestartIgnoredBeforeTrackStarted(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	if m.playStarted {
		t.Fatal("TrackStarted 未到，playStarted 应为 false")
	}
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 0 || fp.playCount() != 1 {
		t.Errorf("playStarted=false 不应重启: restarts=%d playCount=%d", fp.restartCount(), fp.playCount())
	}
}

// 重启重播后仍卡（StalledEvent #2）→ 上限（maxStallRestarts=1）→ 标记 hopeless
// 并直接跳过/停止（单曲队列 = 停止，ended=true；不复用 retry 子路径——retryCount
// 被每次 TrackStarted 归零，stall 型失败走 retry 永不收敛，审查 P2-2）。
func TestStallRestartExhaustedStopsSingleTrack(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})

	// 第一次卡住 → 执行 batch：Restart + 重播
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	if fp.playCount() != 2 {
		t.Fatalf("第一次卡住应重播一次: playCount=%d", fp.playCount())
	}
	// 重播的 file-loaded（restart 计数保留）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.stallRestarts != 1 {
		t.Fatalf("重播后计数应保留: %d", m.stallRestarts)
	}

	// 第二次卡住 → 上限耗尽 → 标记 hopeless + 停止（ended，单曲无曲可跳）
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 1 {
		t.Errorf("上限内只重启一次，不应二次重启: %d", fp.restartCount())
	}
	if !m.ended {
		t.Error("单曲队列卡住上限耗尽后应停止（ended=true）")
	}
	if !m.stallHopeless[tr.ID] {
		t.Error("上限耗尽后应标记 hopeless")
	}
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("上限耗尽 toast = %q, want 含“播放失败”", activeToastText(m))
	}
	if fp.playCount() != 2 {
		t.Errorf("上限耗尽后不立即 Play: playCount=%d", fp.playCount())
	}
	if m.stallRestarts != 0 {
		t.Errorf("上限耗尽后 stallRestarts 应清零: %d", m.stallRestarts)
	}
}

// 重启进行中用户换曲（gen 不匹配）→ 丢弃：不重播旧曲，新曲预算正常。
func TestStallRestartGenMismatchIgnored(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr1 := testTrack("t1")
	tr2 := testTrack("t2")

	m, _ = update(m, trackSelectedMsg{track: tr1}) // Play t1
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}}) // 发起重启
	if m.stallRestarts != 1 {
		t.Fatalf("卡住后计数 = %d, want 1", m.stallRestarts)
	}
	genAtStall := m.playGen

	// 用户换曲（beginPlay t2 自增 playGen，且 beginPlay 清零 stallRestarts）
	m, _ = update(m, trackSelectedMsg{track: tr2})
	if fp.playCount() != 2 || fp.lastPlayed() != tr2.URL {
		t.Fatalf("换曲: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	if m.stallRestarts != 0 {
		t.Errorf("换曲 beginPlay 应清零 stallRestarts: %d", m.stallRestarts)
	}

	// 旧重启 done msg（gen 过期）到达 → 丢弃，不重播 t1
	m, _ = update(m, stalledRestartDoneMsg{gen: genAtStall, track: tr1})
	if fp.playCount() != 2 {
		t.Errorf("gen 不匹配不应重播旧曲: playCount=%d, want 2", fp.playCount())
	}
}

// 重播后真正推进（ProgressEvent>0）→ stallRestarts 清零（会话健康）。
func TestStallRestartResetOnProgress(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.stallRestarts != 1 {
		t.Fatalf("重播后计数应保留: %d", m.stallRestarts)
	}

	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 5, Duration: 200}})
	if m.stallRestarts != 0 {
		t.Errorf("真正推进后 stallRestarts 应清零: %d", m.stallRestarts)
	}
}

// 重启失败（player.Restart 返回错误）→ 走统一失败链（不为难重播）。
func TestStallRestartFailureFallsToRetryChain(t *testing.T) {
	speedUpRetry(t)
	fp := newFakePlayer()
	fp.restartErr = true
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	if cmd == nil {
		t.Fatal("应发起重启 batch")
	}
	m = execStallBatch(t, m, cmd, fp)
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("重启失败 toast = %q, want 含播放失败", activeToastText(m))
	}
	if fp.playCount() != 1 {
		t.Errorf("重启失败不应重播: playCount=%d, want 1", fp.playCount())
	}
}

// 已判定"重启无效"（hopeless）的曲目在队列回绕再次遇到时：不再重启，直接走
// 统一失败链——否则 RepeatAll/回绕时对同一首永远卡住的曲每轮重启一次
// （健康曲的 TrackStarted 会重置 stallRestarts，令上限失效 → 重启风暴，审查 P2）。
func TestStallRestartHopelessTrackNoRestartOnWrap(t *testing.T) {
	speedUpRetry(t)
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	// 第一轮：卡住 → 重启一次 → 重播后仍卡 → 上限耗尽，标记 hopeless
	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 1 {
		t.Fatalf("第一轮应恰重启一次: %d", fp.restartCount())
	}
	if !m.stallHopeless[tr.ID] {
		t.Fatal("上限耗尽后应标记 hopeless")
	}

	// 第二轮（回绕重播同一首，模拟队列回绕：重试/连播再次 beginPlay t1）
	m, _ = update(m, retryPlayMsg{gen: m.playGen})
	if fp.playCount() != 3 {
		t.Fatalf("回绕重播应 Play 一次: playCount=%d", fp.playCount())
	}
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() != 1 {
		t.Errorf("hopeless 曲目回绕不应再重启: restarts=%d, want 1", fp.restartCount())
	}
	if fp.playCount() != 3 {
		t.Errorf("hopeless 曲目应直接停止（不重试不重播）: playCount=%d", fp.playCount())
	}
	if !m.ended {
		t.Error("hopeless 曲目回绕后应停止（ended=true）——必然收敛")
	}
	if !strings.Contains(activeToastText(m), "播放失败") {
		t.Errorf("hopeless 停止 toast = %q, want 含播放失败", activeToastText(m))
	}
}

// 用户手动重选（startPlay）解除 hopeless：给该曲全新预算（尊重用户新意图）。
func TestStallRestartHopelessClearedByUserIntent(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if !m.stallHopeless[tr.ID] {
		t.Fatal("前提：应已标记 hopeless")
	}

	// 用户手动重选同一首（startPlay 清空 hopeless）
	m, _ = update(m, trackSelectedMsg{track: tr})
	if len(m.stallHopeless) != 0 {
		t.Errorf("startPlay 应清空 hopeless: %v", m.stallHopeless)
	}
	if fp.playCount() != 3 {
		t.Fatalf("手动重选应 Play: playCount=%d", fp.playCount())
	}
	// 新预算：再卡住允许再重启
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd = update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	if fp.restartCount() != 2 {
		t.Errorf("手动重选后应允许再次重启: restarts=%d, want 2", fp.restartCount())
	}
}

// 真正推进清除 hopeless（该曲自己推进 = 可重新信任）。
func TestStallRestartHopelessClearedOnProgress(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if !m.stallHopeless[tr.ID] {
		t.Fatal("前提：应已标记 hopeless")
	}

	// 该曲终于推进 → 标记解除
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 3, Duration: 200}})
	if m.stallHopeless[tr.ID] {
		t.Error("真正推进应解除 hopeless")
	}
	// 相邻健康曲推进不应误删其它曲的 hopeless（回归护栏）：先标记 t2 再推进 t1
	m.stallHopeless["t2"] = true
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 4, Duration: 200}})
	if !m.stallHopeless["t2"] {
		t.Error("当前曲推进不应解除其它曲目的 hopeless")
	}
}

// 多曲队列：hopeless 曲目卡住 → 跳过继续连播（队列推进，不卡死不循环）。
func TestStallRestartHopelessSkipsToNextInQueue(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	t1 := testTrack("t1")
	t2 := testTrack("t2")

	m, _ = update(m, trackSelectedMsg{track: t1}) // 队列 [t1]
	m, _ = update(m, trackAppendMsg{track: t2})   // 队列 [t1, t2]
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})

	// 上限耗尽 → hopeless 标记 → exhaustedOrSkip → 跳过到 t2（继续连播）
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if !m.stallHopeless[t1.ID] {
		t.Error("应标记 t1 hopeless")
	}
	if fp.lastPlayed() != t2.URL {
		t.Errorf("应跳过 t1 播放 t2: lastPlayed=%q, want %q", fp.lastPlayed(), t2.URL)
	}
	if fp.restartCount() != 1 {
		t.Errorf("跳过后不应再重启: restarts=%d, want 1", fp.restartCount())
	}
	if m.ended {
		t.Error("有下一曲可跳时不应停止")
	}
}

// 收敛回归（审查 P2-2）：完整循环（卡住 → 重启/重播 → TrackStarted → 再卡住 → …）
// 迭代有限次后必然终止（单曲队列 ended）——不会无限重试/无限重启。
func TestStallRestartConvergesToStop(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	tr := testTrack("t1")

	m, _ = update(m, trackSelectedMsg{track: tr})
	// 模拟连续多轮"卡住"循环，断言收敛（≤3 轮必停止，重启 ≤1 次）。
	// 注意：cmd 必须在循环外先声明——循环体内 `m, cmd := ...` 会因作用域遮蔽
	// 新建一份 m（各轮从重播前的旧状态出发，预算永不耗尽 → 永不收敛）。
	var cmd tea.Cmd
	for i := 0; i < 6; i++ {
		if m.ended {
			break
		}
		m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
		m, cmd = update(m, playerEventMsg{ev: player.StalledEvent{}})
		if cmd == nil {
			t.Fatal("StalledEvent 后应返回 cmd")
		}
		m = execStallBatch(t, m, cmd, fp) // 重启 batch（若预算内）→ 重播或停止
	}
	if !m.ended {
		t.Fatal("卡住循环未收敛到停止（ended）——存在无限循环")
	}
	if fp.restartCount() > 1 {
		t.Errorf("收敛过程中重启不应超过 1 次: %d", fp.restartCount())
	}
}

// 两首永久卡住的曲目在回绕队列中：hopeless 候选被否决 → 最终停止（ended=true），
// 而非无限互相跳过（审查 P2-2 终审场景：≥2 首卡住曲回绕）。收敛 + 重启有界。
func TestStallRestartTwoHopelessWrapsStop(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	t1 := testTrack("t1")
	t2 := testTrack("t2")

	m, _ = update(m, trackSelectedMsg{track: t1}) // 队列 [t1]
	m, _ = update(m, trackAppendMsg{track: t2})   // 队列 [t1, t2]

	// 模拟连续多轮"卡住"循环（两首都会卡），断言收敛到停止。
	// cmd 预声明避免循环体内 := 遮蔽 m（见 TestStallRestartConvergesToStop 注释）。
	var cmd tea.Cmd
	for i := 0; i < 10; i++ {
		if m.ended {
			break
		}
		m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
		m, cmd = update(m, playerEventMsg{ev: player.StalledEvent{}})
		if cmd == nil {
			t.Fatal("StalledEvent 后应返回 cmd")
		}
		m = execStallBatch(t, m, cmd, fp)
	}
	if !m.ended {
		t.Fatal("两首永久卡住曲回绕未收敛到停止（ended）——无限互相跳过")
	}
	// 每首至多重启一次（上限 maxStallRestarts=1），总重启有界
	if fp.restartCount() > 2 {
		t.Errorf("无效重启总次数 = %d, want ≤ 2（每曲上限 1）", fp.restartCount())
	}
}

// 混合队列（1 首卡住 + 1 首健康）回绕：卡住曲跳过、健康曲正常播放，不停止（有曲可听）。
func TestStallRestartMixedQueueSkipsStuckKeepsHealthy(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, fp, fa, nil)
	t1 := testTrack("t1") // 卡住
	t2 := testTrack("t2") // 健康

	m, _ = update(m, trackSelectedMsg{track: t1})
	m, _ = update(m, trackAppendMsg{track: t2})

	// 第一轮：t1 卡住 → 重启→重播→仍卡 → hopeless + 跳 t2
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, cmd := update(m, playerEventMsg{ev: player.StalledEvent{}})
	m = execStallBatch(t, m, cmd, fp)
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if m.ended {
		t.Fatal("有健康曲在前方时不应停止")
	}
	if !m.stallHopeless[t1.ID] || fp.lastPlayed() != t2.URL {
		t.Fatalf("应标记 t1 hopeless 并跳到 t2: hopeless=%v lastPlayed=%q", m.stallHopeless, fp.lastPlayed())
	}
	// 第二轮回绕到 t1：hopeless → 跳过（不重启），t2 继续可播
	m, _ = update(m, playerEventMsg{ev: player.ProgressEvent{Position: 6, Duration: 200}}) // t2 推进
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}}) // t1 又加载（回绕）
	m, _ = update(m, playerEventMsg{ev: player.StalledEvent{}})
	if fp.restartCount() > 1 {
		t.Errorf("回绕到 hopeless t1 不应再重启: restarts=%d", fp.restartCount())
	}
	if m.ended {
		t.Error("混合队列回绕不应停止（t2 仍可播）")
	}
}

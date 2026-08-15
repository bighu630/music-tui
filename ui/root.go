// Package ui 实现 bubbletea 五页面（首页/队列/播放列表/搜索/历史）与全局事件路由。
package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// page 页面枚举（顺序即 Tab 栏从左到右的顺序）。
type page int

const (
	pageHome page = iota
	pageQueue
	pagePlaylists
	pageSearch
	pageHistory
)

// ---- 消息类型 ----

// spinnerTick 是全局 spinner 节拍命令（100ms，与 spinner.Dot 的 10FPS 对齐）。
// 消息不带 ID/tag，两个页面（ID 路由会拒绝陌生 ID）都能消费。
// （bubbles v1.0.0 包级 spinner.Tick 无延时，直接用会形成忙循环。）
var spinnerTick tea.Cmd = tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
	return spinner.TickMsg{Time: time.Now()}
})

// playerEventMsg 由 waitForPlayerEvents cmd 从 Player 事件流注入。
type playerEventMsg struct {
	ev player.Event
}

// playResultMsg 播放结果（预留：异步播放改造时使用）。
// 注意：当前实现中 startPlay 同步调用 player.Play（见 startPlay 注释），
// 因此本消息当前无人生产；保留类型仅为兼容外部消息路由。
type playResultMsg struct {
	track model.Track
	err   error
}

// playerOpResultMsg 暂停/继续/seek 的结果。
type playerOpResultMsg struct {
	err error
}

// trackSelectedMsg 搜索页/历史页请求播放某首歌曲（替换语义）。
type trackSelectedMsg struct {
	track model.Track
}

// trackAppendMsg 搜索页/历史页请求把曲目追加到队尾（不打断当前播放）。
type trackAppendMsg struct {
	track model.Track
}

// lyricsResultMsg 歌词异步加载结果。
type lyricsResultMsg struct {
	trackID string
	lyrics  *lyrics.Lyrics
	err     error
}

// coverResultMsg 封面异步下载结果。
type coverResultMsg struct {
	trackID string
	path    string
	err     error
}

// historyResultMsg 历史写入结果（add/remove/clear 共用）。
type historyResultMsg struct {
	err error
}

// resumeResultMsg 续播恢复结果（PlayPaused（含定位）成败）。
// fromCache 标记本次恢复是否命中本地缓存（命中 → 异步 LoadFailedError
// （onPlayerEvent）路径据此移除损坏缓存；IPC 层失败与文件无关，不删）。
type resumeResultMsg struct {
	err       error
	fromCache bool
}

// resumeInfo 待恢复的会话信息（NewModel 从 session 填充，Init 消费）。
type resumeInfo struct {
	track model.Track
	pos   float64
}

// ---- YT Music 异步结果消息（root 自身产出；页面消息见 playlists.go） ----

// ytVerifyDoneMsg VerifyLogin 异步结果。
type ytVerifyDoneMsg struct {
	err error
}

// ytSyncDoneMsg SyncAll 异步结果（部分失败时 results 含成功项）。
type ytSyncDoneMsg struct {
	results []ytm.SyncResult
	err     error
}

// ytImportDoneMsg ImportURL 异步结果。
type ytImportDoneMsg struct {
	res ytm.SyncResult
	err error
}

// ytRefreshDoneMsg SyncOne 异步结果。
type ytRefreshDoneMsg struct {
	res ytm.SyncResult
	err error
}

// ytVerifyCmd 异步校验登录（30s 超时）。
func ytVerifyCmd(c *ytm.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return ytVerifyDoneMsg{err: c.VerifyLogin(ctx)}
	}
}

// ytSyncAllCmd 异步同步全部歌单。超时预算按歌单数动态分配：
// 先枚举歌单数 N（30s 超时），总预算 = 30s 枚举余量 + 30s×N 拉取，上限 10min
// （审查 m2：原固定 5min 在 >9 个歌单时可能误杀慢速网络下的合法同步）。
func ytSyncAllCmd(c *ytm.Client, pl *playlists.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		remotes, err := c.ListPlaylists(ctx)
		cancel()
		if err != nil {
			return ytSyncDoneMsg{err: err}
		}
		budget := syncAllBudget(len(remotes))
		ctx, cancel = context.WithTimeout(context.Background(), budget)
		defer cancel()
		results, err := c.SyncAll(ctx, pl)
		return ytSyncDoneMsg{results: results, err: err}
	}
}

// syncAllBudget 按歌单数计算同步总预算：30s 枚举余量 + 每歌单 30s，上限 10min。
func syncAllBudget(n int) time.Duration {
	budget := 30*time.Second + 30*time.Second*time.Duration(n)
	if budget > 10*time.Minute {
		return 10 * time.Minute
	}
	return budget
}

// ytImportCmd 异步导入歌单 URL（2min 超时）。
func ytImportCmd(c *ytm.Client, pl *playlists.Store, url string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := c.ImportURL(ctx, pl, url)
		return ytImportDoneMsg{res: res, err: err}
	}
}

// ytRefreshCmd 异步刷新单个同步列表（2min 超时）。
func ytRefreshCmd(c *ytm.Client, pl *playlists.Store, playlistID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := c.SyncOne(ctx, pl, playlistID)
		return ytRefreshDoneMsg{res: res, err: err}
	}
}

// ytVerifyErrorText 把登录验证错误映射为可操作的友好文案。
func ytVerifyErrorText(err error) string {
	switch {
	case errors.Is(err, ytm.ErrNotLoggedIn):
		return "登录无效，请重新导出 cookie"
	case errors.Is(err, ytm.ErrSessionInvalid):
		return "登录已失效，请重新导出 cookie"
	case errors.Is(err, ytm.ErrNoLogin):
		return "未配置登录"
	}
	return err.Error()
}

// ytInvalidAfterSync 按同步/导入/刷新结果更新验证失败标记：
// 成功 → 清除；ErrNotLoggedIn/ErrSessionInvalid → 置位；其他错误（网络等）→ 保持。
func ytInvalidAfterSync(prev bool, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ytm.ErrNotLoggedIn) || errors.Is(err, ytm.ErrSessionInvalid) {
		return true
	}
	return prev
}

// saveInterval 播放中自动保存会话的节流间隔。
const saveInterval = 5 * time.Second

// 播放失败自动重试：取流失败（如 YouTube 403 风控）多为瞬态错误，
// 重试 = 重新 loadfile = 重新取流拿新签名 URL，大概率恢复。
const maxPlayRetries = 2           // 每首曲目最多自动重试次数
var retryBackoff = 2 * time.Second // 重试间隔（包级变量：测试可调小以缩短等待）

// Model 顶层模型：持有共享播放状态、播放队列与五个页面，负责全局按键、
// 页面切换、服务调用与结果路由。
type Model struct {
	player  player.Player
	lyrics  *lyrics.Client
	cover   *cover.Fetcher
	history *history.Store
	queue   *queue.Queue
	session *session.Store
	pl      *playlists.Store
	yt      *ytm.Client // YT Music 同步客户端；nil = 未集成（测试/降级）

	cache            *cache.Manager // 音频缓存（命中优先本地文件；未命中后台下载）
	playingFromCache bool           // 当前曲目是否播放自缓存文件（LoadFailed 时据此移除损坏条目）

	state    model.PlaybackState
	current  page
	width    int // 窗口宽度（分隔线按此宽度渲染，不写死）
	hoverTab int // Tab 栏悬停标签下标（= page 枚举值）；-1 = 无悬停
	// toast 活跃 toast（单条覆盖；定时自动消失，不参与布局）。替代旧 lastError/notice 横幅。
	toast   *toast
	toastID uint64 // toast 自增 id：过期消息按 id 匹配，防误清被覆盖后的新 toast
	ended   bool   // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）

	// YT Music 同步状态（登录配置 + 同步中 + 验证失败标记；页面经 setYTSyncStatus 推入）
	ytLogin   ytm.LoginConfig
	ytSyncing bool
	// ytInvalid 最近一次 VerifyLogin 失败：状态区/设置页降级为「已登录（验证失败）」；
	// 初始启动未验证过为 false（配置存在即显示已登录，只有验证失败才降级）。
	ytInvalid bool
	// queueSkip 标记删除当前曲导致的指针解耦：mpv 仍播放被删曲目，
	// 但队列指针已顺延。下次 TrackEnded 应播放顺延曲目（不推进），
	// 避免跳过顺延曲目（回归：TestDeleteCurrentThenTrackEndedPlaysSlidTrack）。
	queueSkip bool

	// 播放失败自动重试状态
	retryCount int // 当前曲目已自动重试次数（新曲加载成功/结束/手动播放时重置）
	playGen    int // 播放代际计数器：每次 beginPlay 自增；过期重试消息（用户已换曲）丢弃
	// failedTracks 本轮取流失败（重试耗尽被跳过）的曲目 ID 集合：
	// 队列回绕时防止“失败→跳过→回绕→再失败”无限交替重播死循环
	// （回归：TestLoadFailAllTracksFailStopsLoop）。TrackStartedEvent 清空——
	// 任何曲目加载成功即视为新一轮，集合作废。
	failedTracks map[string]bool

	resume *resumeInfo // 续播恢复信息（NewModel 填充，Init 消费；nil = 无会话）
	// resuming 续播恢复进行中标记：恢复加载（PlayPaused 静默加载）撞取流失败时
	// 不自动重试（重试会走 beginPlay→Play()：发声、从 0:00、非暂停，静默丢弃
	// 恢复语义），保留“恢复播放失败”语义（回归：TestResumeLoadFailNoAutoRetry）。
	resuming bool
	lastSave time.Time // 最近一次自动保存会话的时刻（节流）

	home        homeModel
	searchPage  searchModel
	historyPage historyModel
	queuePage   queueModel
	plPage      playlistModel

	plPicker *plPickerModel // 全局 a 键“添加到”选择器；nil = 未打开

	onTrack func(*model.Track) // 外部消费者（MPRIS）感知当前曲目；nil 安全
}

// NewModel 组装 UI。p/s 为接口（可注入 fake 测试），l/c/h/sess/pl/cm/yt 为具体服务，
// cm 为音频缓存管理器（nil = 未集成），yt 为 YT Music 同步客户端（nil = 未集成），
// onTrack 在播放状态变化时同步回调当前曲目（nil 表示无曲目；可为 nil）。
// 若 sess 存在已保存会话（队列 + 进度），同步恢复队列与播放状态（暂停态），
// mpv 的静默加载由 Init 返回的 resumeCmd 完成。
func NewModel(p player.Player, s search.SearchAdapter, l *lyrics.Client, c *cover.Fetcher, h *history.Store, sess *session.Store, pl *playlists.Store, cm *cache.Manager, yt *ytm.Client, onTrack func(*model.Track)) Model {

	m := Model{
		player:       p,
		lyrics:       l,
		cover:        c,
		history:      h,
		queue:        queue.New(),
		session:      sess,
		pl:           pl,
		cache:        cm,
		yt:           yt,
		onTrack:      onTrack,
		current:      pageHome,
		hoverTab:     -1,
		failedTracks: map[string]bool{},
		home:         newHomeModel(p),
		searchPage:   newSearchModel(s),
		historyPage:  newHistoryModel(),
		queuePage:    newQueueModel(),
		plPage:       newPlaylistModel(),
	}
	// 续播恢复：会话存在且队列有当前曲目才恢复；否则丢弃会话从空态开始
	if st := sess.State(); st != nil {
		m.queue.Restore(st.Queue)
		if cur, ok := m.queue.Current(); ok {
			pos := st.Position
			if pos < 0 {
				pos = 0
			}
			// 上界 clamp：损坏但可解析的超大进度按曲目时长收口
			if cur.Duration > 0 && pos > cur.Duration {
				pos = cur.Duration
			}
			if st.Ended {
				// 退出时已播完：有下一首则从下一首开头（暂停），否则当前曲从头
				if next, ok := m.queue.Next(); ok {
					cur = next
					pos = 0
				} else {
					pos = 0
				}
			}
			m.state = model.PlaybackState{Track: &cur, Position: pos, Duration: cur.Duration, Playing: false}
			m.home = m.home.resetForTrack(&cur)
			m.home = m.home.syncState(m.state)
			m.notifyTrack(&cur)
			m.resume = &resumeInfo{track: cur, pos: pos}
			// 续播恢复进行中。注：不能放在 Init——bubbletea 调用 Init 的是模型
			// 副本（值接收者修改不回流），标记须随恢复上下文在此一起设置。
			m.resuming = true
			// 预置节流基准：恢复后 loadfile 会触发 time-pos=0 的 ProgressEvent
			//（先于 Seek 定位到达），若 lastSave 为零值会立即触发保存，
			// 把磁盘上的恢复进度覆盖为 0（回归：TestResumeFirstProgressEventDoesNotOverwriteDisk）
			m.lastSave = time.Now()
		} else {
			// 队列无当前曲目（损坏/手改数据）：丢弃会话
			m.queue = queue.New()
		}
	}
	m = m.syncQueueViews()
	m.historyPage = m.historyPage.setEntries(h.Entries())
	m.plPage = m.plPage.setLists(pl.Lists())
	if yt != nil {
		// 初始 YT 状态：登录配置 + 既有同步映射（详情 r 刷新提示）
		m.ytLogin = yt.Login()
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, false, false).setYTSyncs(yt.SyncEntries())
	}
	return m
}

// Init 启动两个常驻 cmd：播放器事件监听 + spinner 全局 tick；
// 存在已保存会话时追加续播恢复 cmd（PlayPaused 静默加载并定位）。
// （不用包级 spinner.Tick：bubbles v1.0.0 的包级 Tick 无延时，会形成忙循环。）
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{waitForPlayerEvents(m.player), spinnerTick}
	if m.resume != nil {
		cmds = append(cmds, resumeCmd(m))
	}
	return tea.Batch(cmds...)
}

// Update 全局消息路由：先处理播放器事件/服务结果/全局按键，
// 其余消息委托给当前页面。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case playerEventMsg:
		return m.onPlayerEvent(msg)

	case trackSelectedMsg:
		return m.startPlay(msg.track)

	case trackAppendMsg:
		// 追加到队尾：不打断当前播放，也不自动开播（队列为空时同样只入队）
		m.queue.Add(msg.track)
		return m.syncQueueViews(), nil

	case queuePlayMsg:
		// 队列页 Enter：跳转语义——保留队列其余曲目，仅把当前指针
		// 移到所选曲目并播放（与搜索/历史的替换语义区分）。
		if !m.queue.JumpTo(msg.index) {
			return m, nil
		}
		m.retryCount = 0    // 手动跳转播放：全新重试预算
		m.queueSkip = false // 跳转即重新对齐，解除删除解耦标记
		m.current = pageHome
		return m.playQueueTrack()

	case prevTrackMsg:
		// 上一首（首页 , 键 / ⏮ 按钮）：手动操作重置重试预算并解除删除解耦标记
		if tr, ok := m.queue.Prev(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			return m.beginPlay(tr)
		}
		return m, nil

	case nextTrackMsg:
		// 下一首（首页 . 键 / ⏭ 按钮）：同上
		if tr, ok := m.queue.Next(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			return m.beginPlay(tr)
		}
		return m, nil

	case togglePlayMsg:
		return m.togglePlay()

	case toggleModeMsg:
		return m.cycleMode()

	case queueDeleteMsg:
		// 删除当前曲目时 mpv 仍播放被删曲目（不打断），队列指针已顺延：
		// 记录解耦标记，下次 TrackEnded 播放顺延曲目而非推进。
		if idx := m.queue.CurrentIndex(); idx >= 0 && msg.index == idx {
			m.queueSkip = true
		}
		m.queue.Remove(msg.index)
		return m.syncQueueViews(), nil

	case queueClearMsg:
		m.queue.Clear()
		m.queueSkip = false
		return m.syncQueueViews(), nil

	case queueModeMsg:
		// 队列页 s 键：与首页模式按钮共用三态循环
		return m.cycleMode()

	case plLoadMsg:
		// 播放列表详情 Enter：整列表替换进队列，从选中曲开始播放
		// （替换语义同 startPlay：清空队列 → 新队列 → 播放）。
		// 列表存在性须用 Lists() 遍历判断：Tracks 对“空列表”同样返回 nil，
		// 不能据此报“不存在”（空列表加载 = 清空队列，UI 不可达，防御即可）。
		exists := false
		for _, l := range m.pl.Lists() {
			if l.Name == msg.name {
				exists = true
				break
			}
		}
		if !exists {
			m, cmd := m.showToast("播放列表「"+msg.name+"」不存在", toastError)
			return m, cmd
		}
		m.queue.ReplaceAll(m.pl.Tracks(msg.name), msg.index)
		m.retryCount = 0    // 手动播放：全新重试预算
		m.queueSkip = false // 替换即重新对齐，解除删除解耦标记
		m.current = pageHome
		m = m.syncQueueViews()
		return m.playQueueTrack()

	case plCreateMsg:
		if _, err := m.pl.Create(msg.name); err != nil {
			m, cmd := m.showToast("新建播放列表失败: "+err.Error(), toastError)
			m.plPage = m.plPage.setLists(m.pl.Lists())
			return m, cmd
		}
		m.plPage = m.plPage.exitNaming() // 成功退出命名输入；失败保留输入便于修改
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, nil

	case plRenameMsg:
		if err := m.pl.Rename(msg.oldName, msg.newName); err != nil {
			m, cmd := m.showToast("重命名失败: "+err.Error(), toastError)
			m.plPage = m.plPage.setLists(m.pl.Lists())
			return m, cmd
		}
		m.plPage = m.plPage.exitNaming()
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, nil

	case plDeleteMsg:
		if err := m.pl.Delete(msg.name); err != nil {
			m, cmd := m.showToast("删除播放列表失败: "+err.Error(), toastError)
			m.plPage = m.plPage.setLists(m.pl.Lists())
			return m, cmd
		}
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, nil

	case plRemoveTrackMsg:
		if err := m.pl.RemoveTrack(msg.name, msg.index); err != nil {
			m, cmd := m.showToast("移除歌曲失败: "+err.Error(), toastError)
			m.plPage = m.plPage.setLists(m.pl.Lists())
			return m, cmd
		}
		m.plPage = m.plPage.setLists(m.pl.Lists())
		return m, nil

	// ---- YT Music 同步编排 ----

	case ytLoginMsg:
		// 浏览器方式登录提交：保存配置 → 异步验证
		if m.yt == nil {
			return m, nil
		}
		if err := m.yt.SetLogin(msg.cfg); err != nil {
			m, cmd := m.showToast("保存登录配置失败: "+err.Error(), toastError)
			return m, cmd
		}
		m.ytLogin = m.yt.Login()
		m.ytInvalid = false // 新配置未验证
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, m.ytSyncing, m.ytInvalid)
		m, cmd := m.showToast("已保存登录配置，验证中…", toastInfo)
		return m, tea.Batch(cmd, ytVerifyCmd(m.yt))

	case ytLoginFileMsg:
		// cookies.txt 方式：校验文件可读 → 保存配置 → 异步验证
		if m.yt == nil {
			return m, nil
		}
		path := strings.TrimSpace(msg.path)
		if path == "" {
			return m, nil
		}
		if _, err := os.Stat(path); err != nil {
			m, cmd := m.showToast("cookies.txt 不可读: "+err.Error(), toastError)
			return m, cmd
		}
		cfg := ytm.LoginConfig{Method: ytm.MethodCookiesFile, CookiesPath: path}
		if err := m.yt.SetLogin(cfg); err != nil {
			m, cmd := m.showToast("保存登录配置失败: "+err.Error(), toastError)
			return m, cmd
		}
		m.ytLogin = m.yt.Login()
		m.ytInvalid = false // 新配置未验证
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, m.ytSyncing, m.ytInvalid)
		m, cmd := m.showToast("已保存登录配置，验证中…", toastInfo)
		return m, tea.Batch(cmd, ytVerifyCmd(m.yt))

	case ytLoginPasteMsg:
		// 粘贴 Cookie 方式：落盘 cookies 文件 + 保存配置 → 异步验证
		if m.yt == nil {
			return m, nil
		}
		text := strings.TrimSpace(msg.text)
		if text == "" {
			return m, nil
		}
		if _, err := m.yt.SetPastedLogin(text); err != nil {
			m, cmd := m.showToast("保存登录配置失败: "+err.Error(), toastError)
			return m, cmd
		}
		m.ytLogin = m.yt.Login()
		m.ytInvalid = false // 新配置未验证
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, m.ytSyncing, m.ytInvalid)
		m, cmd := m.showToast("已保存登录配置，验证中…", toastInfo)
		return m, tea.Batch(cmd, ytVerifyCmd(m.yt))

	case ytLogoutMsg:
		// 退出登录：清除配置 + 页面状态复位
		if m.yt == nil {
			return m, nil
		}
		if err := m.yt.ClearLogin(); err != nil {
			m, cmd := m.showToast("退出登录失败: "+err.Error(), toastError)
			return m, cmd
		}
		m.ytLogin = ytm.LoginConfig{}
		m.ytInvalid = false
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, m.ytSyncing, m.ytInvalid)
		m, cmd := m.showToast("已退出 YT Music 登录", toastSuccess)
		return m, cmd

	case ytVerifyDoneMsg:
		// 验证结果：无论成败刷新页面登录状态；失败时状态区/设置页降级展示
		if m.yt == nil {
			return m, nil // 防御：未集成 yt 时丢弃结果
		}
		m.ytInvalid = msg.err != nil
		var cmd tea.Cmd
		if msg.err != nil {
			m, cmd = m.showToast(ytVerifyErrorText(msg.err), toastError)
		} else {
			m, cmd = m.showToast("YT Music 登录有效", toastSuccess)
		}
		m.ytLogin = m.yt.Login()
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, m.ytSyncing, m.ytInvalid)
		return m, cmd

	case ytSyncAllMsg:
		// 同步全部：同步中忽略重复触发
		if m.yt == nil || m.ytSyncing {
			return m, nil
		}
		m.ytSyncing = true
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, true, m.ytInvalid)
		return m, ytSyncAllCmd(m.yt, m.pl)

	case ytSyncDoneMsg:
		if m.yt == nil {
			return m, nil // 防御：未集成 yt 时丢弃结果
		}
		m.ytSyncing = false
		m.ytInvalid = ytInvalidAfterSync(m.ytInvalid, msg.err)
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, false, m.ytInvalid)
		if msg.err != nil {
			m, cmd := m.showToast("同步失败: "+msg.err.Error(), toastError)
			// 部分失败也刷新（成功项已入库）
			m.plPage = m.plPage.setLists(m.pl.Lists())
			m.plPage = m.plPage.setYTSyncs(m.yt.SyncEntries())
			return m, cmd
		}
		total := 0
		for _, r := range msg.results {
			total += r.TrackCount
		}
		m, cmd := m.showToast(fmt.Sprintf("已同步 %d 个歌单 · 共 %d 首", len(msg.results), total), toastSuccess)
		// 部分失败也刷新（成功项已入库）
		m.plPage = m.plPage.setLists(m.pl.Lists())
		m.plPage = m.plPage.setYTSyncs(m.yt.SyncEntries())
		return m, cmd

	case ytImportMsg:
		// URL 导入：同步/导入中忽略重复触发；空 URL 忽略
		if m.yt == nil || m.ytSyncing {
			return m, nil
		}
		url := strings.TrimSpace(msg.url)
		if url == "" {
			return m, nil
		}
		m.ytSyncing = true
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, true, m.ytInvalid)
		return m, ytImportCmd(m.yt, m.pl, url)

	case ytImportDoneMsg:
		if m.yt == nil {
			return m, nil // 防御：未集成 yt 时丢弃结果
		}
		m.ytSyncing = false
		m.ytInvalid = ytInvalidAfterSync(m.ytInvalid, msg.err)
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, false, m.ytInvalid)
		if msg.err != nil {
			m, cmd := m.showToast("导入失败: "+msg.err.Error(), toastError)
			return m, cmd
		}
		m, cmd := m.showToast(fmt.Sprintf("已导入「%s」%d 首", msg.res.Remote.Title, msg.res.TrackCount), toastSuccess)
		m.plPage = m.plPage.setLists(m.pl.Lists())
		m.plPage = m.plPage.setYTSyncs(m.yt.SyncEntries())
		return m, cmd

	case ytRefreshMsg:
		// 详情 r：仅 YT 同步列表可刷新；同步中忽略
		if m.yt == nil || m.ytSyncing {
			return m, nil
		}
		playlistID := ""
		found := false
		for _, e := range m.yt.SyncEntries() {
			if e.ListName == msg.listName {
				playlistID = e.PlaylistID
				found = true
				break
			}
		}
		if !found {
			m, cmd := m.showToast("该列表不是 YT Music 同步列表", toastError)
			return m, cmd
		}
		m.ytSyncing = true
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, true, m.ytInvalid)
		return m, ytRefreshCmd(m.yt, m.pl, playlistID)

	case ytRefreshDoneMsg:
		if m.yt == nil {
			return m, nil // 防御：未集成 yt 时丢弃结果
		}
		m.ytSyncing = false
		m.ytInvalid = ytInvalidAfterSync(m.ytInvalid, msg.err)
		m.plPage = m.plPage.setYTSyncStatus(m.ytLogin, false, m.ytInvalid)
		if msg.err != nil {
			m, cmd := m.showToast("刷新失败: "+msg.err.Error(), toastError)
			return m, cmd
		}
		m, cmd := m.showToast(fmt.Sprintf("已刷新「%s」%d 首", msg.res.ListName, msg.res.TrackCount), toastSuccess)
		m.plPage = m.plPage.setLists(m.pl.Lists())
		m.plPage = m.plPage.setYTSyncs(m.yt.SyncEntries())
		return m, cmd

	case playResultMsg:
		// 预留分支：若未来把 player.Play 改回异步 cmd，此分支处理其失败结果。
		// 当前 startPlay 同步调用 Play，本分支不会触发。
		if msg.err != nil {
			m, cmd := m.showToast("播放失败: "+msg.err.Error(), toastError)
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, cmd
		}
		return m, nil

	case playerOpResultMsg:
		if msg.err != nil {
			m, cmd := m.showToast(msg.err.Error(), toastError)
			return m, cmd
		}
		return m, nil

	case retryPlayMsg:
		// 取流失败自动重试（retryPlayCmd 延迟触发）：代际不匹配说明用户
		// 已手动换曲/重播，丢弃过期重试；匹配则重走播放流程（状态一致性）。
		if msg.gen != m.playGen {
			return m, waitForPlayerEvents(m.player)
		}
		// 重试与队列当前状态重新对齐：删除当前曲已使指针顺延，残留标记
		// 会让 TrackEnded 重复播放顺延曲目（回归：TestRetryPlayClearsQueueSkip）。
		m.queueSkip = false
		if _, ok := m.queue.Current(); !ok {
			// 重试等待期间队列被清空/删光：无曲可播，停止重试
			//（避免“正在自动重试”toast 悬挂）。
			m.ended = true
			m, cmd := m.showToast("播放失败：队列已清空，已停止自动重试", toastError)
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, tea.Batch(cmd, waitForPlayerEvents(m.player))
		}
		m2, cmd := m.playQueueTrack()
		return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))

	case resumeResultMsg:
		// 命中/未命中标记回填（成功分支据此在异步 LoadFailed 时移除损坏缓存）。
		m.playingFromCache = msg.fromCache
		if msg.err != nil {
			// 恢复失败：状态重置（当前曲播放不了），但队列保留展示——用户仍
			// 可查看/跳转播放其他曲目；磁盘会话保留，下次启动重试（mpv 瞬时
			// 故障可恢复），用户播放新曲或退出时自然覆盖/清除。
			// 注意：不依据 fromCache 移除缓存条目——IPC 层错误（PlayPaused 命令
			// 失败）只代表命令未被 mpv 接受（连接/参数瞬态问题），与缓存文件损坏
			// 无关（坏文件 mpv 会接受 loadfile 后异步报 end-file error）；删除健康
			// 缓存有害，真实损坏由异步 LoadFailedError 路径处理。
			m.resuming = false // 恢复上下文作废
			m, cmd := m.showToast("恢复播放失败: "+msg.err.Error(), toastError)
			m.state = model.PlaybackState{}
			m.home = m.home.syncState(m.state)
			m.queueSkip = false
			m.notifyTrack(nil)
			return m.syncQueueViews(), cmd
		}
		// 恢复成功：暂停态也加载歌词/封面展示。
		// 注意：不在成功分支清除 resuming——PlayPaused 的 IPC 成功只代表
		// 命令被接受，mpv 异步取流失败（end-file error → LoadFailedError）随后
		// 才到；resuming 须保持到 TrackStartedEvent（加载真成功）或
		// LoadFailedError（真失败）或 beginPlay（用户新意图）。
		if m.state.Track == nil {
			return m, nil
		}
		// 恢复成功：按当前模式补 SetLoop（beginPlay 有显式 SetLoop，恢复路径
		// 此前漏设——单曲循环模式下恢复会丢失 mpv loop-file 语义；回归：
		// TestResumeSuccessSetsLoopPerMode）。失败仅记 toast 不阻断恢复。
		var loopCmd tea.Cmd
		if err := m.player.SetLoop(m.queue.Mode() == queue.RepeatOne); err != nil {
			m, loopCmd = m.showToast("设置循环失败: "+err.Error(), toastError)
		}
		return m, tea.Batch(
			loopCmd,
			fetchLyricsCmd(m.lyrics, *m.state.Track),
			fetchCoverCmd(m.cover, *m.state.Track),
		)

	case searchResultsMsg:
		m.searchPage = m.searchPage.withResults(msg)
		return m, nil

	case lyricsResultMsg:
		// 过期结果（已切歌）丢弃
		if m.state.Track != nil && msg.trackID == m.state.Track.ID {
			m.home = m.home.setLyrics(msg.err, msg.lyrics)
		}
		return m, nil

	case coverResultMsg:
		if m.state.Track != nil && msg.trackID == m.state.Track.ID {
			m.home = m.home.setCover(msg.trackID, msg.path, msg.err)
		}
		return m, nil

	case historyResultMsg:
		if msg.err != nil {
			log.Printf("写入历史失败（不影响播放）: %v", msg.err)
		}
		return m, nil

	case deleteEntryMsg:
		if err := m.history.Remove(msg.id, msg.source); err != nil {
			m, cmd := m.showToast("删除历史失败: "+err.Error(), toastError)
			return m.refreshHistory(), cmd
		}
		return m.refreshHistory(), nil

	case clearHistoryMsg:
		if err := m.history.Clear(); err != nil {
			m, cmd := m.showToast("清空历史失败: "+err.Error(), toastError)
			return m.refreshHistory(), cmd
		}
		return m.refreshHistory(), nil

	case toastExpireMsg:
		m.toast = expireToast(m.toast, msg.id)
		return m, nil

	case tea.BatchMsg:
		// 同步执行 batch 子命令并回灌结果：仅供测试的 update/execCmds 驱动方式使用。
		// 真实 bubbletea 程序中 BatchMsg 由事件循环处理（execBatchMsg），不会到达 Update。
		// （计划缺陷：测试的 execCmds 对 batch cmd 只调用一次得到 BatchMsg，
		//   子命令不会自动执行，必须在 Update 里展开。）
		for _, c := range msg {
			if c == nil {
				continue
			}
			if r := c(); r != nil {
				tm, _ := m.Update(r)
				m = tm.(Model)
			}
		}
		return m, nil

	case spinner.TickMsg:
		// 全局唯一 tick 链：两个页面的 spinner 都推进。
		// 注意：bubbles v1.0.0 包级 spinner.Tick 无延时（立即返回 TickMsg），
		// 若直接返回它会形成忙循环，必须用 tea.Tick 定时节拍。
		m.home = m.home.tick(msg)
		m.searchPage = m.searchPage.tick(msg)
		return m, spinnerTick

	case tea.WindowSizeMsg:
		// 顶部空行 + Tab 栏 + 分隔线占 3 行、底部状态栏占 1 行，页面高度相应减 4
		m.width = msg.Width
		m.home = m.home.setSize(msg.Width, msg.Height-4)
		m.searchPage = m.searchPage.setSize(msg.Width, msg.Height-4)
		m.historyPage = m.historyPage.setSize(msg.Width, msg.Height-4)
		m.queuePage = m.queuePage.setSize(msg.Width, msg.Height-4)
		m.plPage = m.plPage.setSize(msg.Width, msg.Height-4)
		if m.plPicker != nil {
			picker := m.plPicker.setSize(msg.Width, msg.Height-4)
			m.plPicker = &picker
		}
		return m, nil

	case tea.MouseMsg:
		return m.onMouse(msg)

	case tea.KeyMsg:
		// 选择器打开时：所有按键交给选择器（完成/取消时带回成功提示并刷新列表页）。
		if m.plPicker != nil {
			var cmd tea.Cmd
			var picker plPickerModel
			picker, cmd = m.plPicker.Update(msg)
			if picker.closed {
				if picker.notice != "" {
					m2, tcmd := m.showToast(picker.notice, toastSuccess)
					m = m2
					cmd = tea.Batch(cmd, tcmd) // Batch 跳过 nil
				}
				m.plPicker = nil
				m.plPage = m.plPage.setLists(m.pl.Lists())
				return m, cmd
			}
			m.plPicker = &picker
			return m, cmd
		}
		switch msg.String() {
		case "tab", "ctrl+right":
			return m.switchPage(msg.String()), nil
		case "shift+tab", "ctrl+left":
			return m.switchPage(msg.String()), nil
		case "1", "2", "3", "4", "5":
			// 数字键始终切页。注：计划原代码在搜索输入框聚焦时把数字让给输入框，
			// 但 TestTabSwitchesPages 要求搜索页聚焦时按 3/1 仍能切页，故取消例外。
			return m.switchPage(msg.String()), nil
		case " ":
			// 输入框聚焦（搜索关键词/播放列表命名）时空格是输入字符。
			// bubbletea 把空格解析为 KeySpace 类型（真实终端解析会带 Runes，
			// 但测试构造的 KeySpace 无 Runes）；textinput 按 msg.Runes 插字符，
			// 统一转成 KeyRunes(' ') 保证插入。
			if m.typingText() {
				return m.delegate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
			}
			return m.togglePlay()
		case "q", "ctrl+c":
			// 注意：输入框聚焦时 ctrl+c 会被 textinput 吞掉
			// （bubbles v1.0.0 textinput 无 ctrl+c 绑定，按键被消费且不转发），
			// 此时无法用 ctrl+c 退出，只能按 q。
			if m.typingText() {
				return m.delegate(msg)
			}
			m.saveSession() // 退出前持久化会话（队列 + 进度）
			return m, tea.Quit
		case "a":
			// 输入框聚焦时 a 是输入字符（同空格/q 模式）；否则弹出“添加到”选择器
			//（当前播放队列/各播放列表/新建列表）。p 已改为页面层播放键，全局不拦截。
			if m.typingText() {
				return m.delegate(msg)
			}
			track, ok := m.selectedTrack()
			if !ok {
				m, cmd := m.showToast("当前没有可添加的歌曲（请先在搜索/历史/播放列表页选中歌曲）", toastError)
				return m, cmd
			}
			m.hoverTab = -1 // 打开选择器时清除悬停高亮（打开期间鼠标事件被忽略，防残留）
			m.plPicker = newPlPicker(m.pl, track)
			return m, nil
		}
		return m.delegate(msg)
	}

	return m.delegate(msg)
}

// View 渲染当前页面（选择器打开时全屏替换），底部附常驻状态栏；
// 顶部首行留空（布局整体下移一行：空行第 1 行、Tab 栏第 2 行、分隔线第 3 行、
// 页面自第 4 行起）；活跃 toast 覆盖在状态栏上方一行的右端（不参与布局，行数不变）。
func (m Model) View() string {
	var body string
	if m.plPicker != nil {
		body = m.plPicker.view()
	} else {
		switch m.current {
		case pageHome:
			body = m.home.view()
		case pageQueue:
			body = m.queuePage.view()
		case pagePlaylists:
			body = m.plPage.view()
		case pageSearch:
			body = m.searchPage.view()
		case pageHistory:
			body = m.historyPage.view()
		}
	}
	out := "\n" + m.tabBar() + "\n" + body + "\n" + m.statusBarView()
	return m.overlayToast(out)
}

// statusBarView 底部常驻状态栏（恒 1 行，布局稳定）：首页自身已展示曲目
// 信息（控制栏：标题/播放状态/模式/队列位置），状态栏与之重复——首页时
// 状态栏行留空（行恒存在，布局稳定）；其余页面左 = 歌曲名（截断），
// 右 = 播放状态 + 模式 + 队列位置。toast 覆盖在其上方一行的右端。
func (m Model) statusBarView() string {
	// 首页控制栏已展示曲目信息，状态栏留空（View 的 "\n" + "" 仍保持行数）
	if m.current == pageHome {
		return ""
	}
	left := "未在播放"
	if m.state.Track != nil {
		left = m.state.Track.Title + " - " + m.state.Track.Artist
	}
	right := ""
	if m.state.Track != nil {
		icon := "⏵"
		switch {
		case m.ended:
			icon = "⏹" // 播放结束/出错停止：重播同曲语义，与空格行为一致
		case !m.state.Playing:
			icon = "⏸"
		}
		pos := 0
		if m.queue.CurrentIndex() >= 0 {
			pos = m.queue.CurrentIndex() + 1
		}
		right = fmt.Sprintf("%s %s · %d/%d", icon, modeName(m.queue.Mode()), pos, m.queue.Len())
	}
	style := lipgloss.NewStyle().Faint(true)
	if m.width <= 0 {
		return style.Render(left)
	}
	// 右侧播放顺序信息优先完整，左侧歌曲名按剩余宽度截断（曾按 left 优先：
	// 名称截断基准须随右侧宽度动态变化）。
	rightRendered := style.Render(right)
	rightW := ansi.StringWidth(rightRendered)
	leftMax := m.width - rightW - 1
	// 极端窄窗口（宽度小于右侧顺序文本）：左侧已无可截断空间，右侧截断兜底，
	// 保证状态栏恒 1 行不折行（与 overlayToast 的 width≤2 兜底同模式）。
	if rightW >= m.width {
		right = ansi.Truncate(right, m.width, "…")
		rightRendered = style.Render(right)
		rightW = ansi.StringWidth(rightRendered)
		leftMax = m.width - rightW - 1
	}
	if leftMax < 0 {
		leftMax = 0
	}
	left = ansi.Truncate(left, leftMax, "…")
	leftRendered := style.Render(left)
	pad := m.width - ansi.StringWidth(leftRendered) - rightW
	if pad < 0 {
		pad = 0
	}
	return leftRendered + strings.Repeat(" ", pad) + rightRendered
}

// toastText 按类型渲染 toast 文案（图标 + 颜色，与 lipgloss 主题一致）。
func (m Model) toastText(t toast) string {
	switch t.kind {
	case toastError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("⚠ " + t.text)
	case toastWarning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("⚠ " + t.text)
	case toastSuccess:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✔ " + t.text)
	default:
		return lipgloss.NewStyle().Faint(true).Render("ℹ " + t.text)
	}
}

// overlayToast 把活跃 toast 覆盖到完整输出（tabBar+body+statusBar）中状态栏
// 上方一行的右端：行数不变、其余内容不变 → 出现/消失排版零跳动。
// 无 toast 或行数不足时原样返回。超宽 toast 按 m.width-2 截断（预留分隔符
// 空间），覆盖行恒不超窗口宽度，不会触发终端折行把 Tab 栏滚出屏幕。
func (m Model) overlayToast(out string) string {
	if m.toast == nil || out == "" {
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return out
	}
	idx := len(lines) - 2 // 状态栏上方一行
	text := m.toastText(*m.toast)
	tw := ansi.StringWidth(text)
	// 截断基准 m.width-2（预留 "  " 分隔符 2 格）：覆盖行 = 截断 toast + "  "
	// 恒 ≤ m.width，不折行。截断保留尾部语义——失败原因/后续动作（如
	// “已重试 N 次，跳过继续播放”）在句尾，头部歌曲名在状态栏/队列已可见。
	// ⚠/✔/ℹ 图标与消息头部随截断一起被截掉（ANSI 颜色样式保留）——这是有意
	// 取舍：动作语义在句尾，头部歌曲名在状态栏/队列可见，并非截断丢失。
	// 注意 ansi.TruncateLeft 的 n 是“从左侧删掉多少格”而非目标宽度
	// （result = tw - n + prefixW，… 占 1 格）；且跨界字符整簇保留，结果可比
	// tw - n + 1 再宽 1 格（CJK 宽字符），故 n 多给 1 格余量保证 ≤ m.width-2。
	if m.width > 2 && tw > m.width-2 {
		text = ansi.TruncateLeft(text, tw-m.width+4, "…")
		tw = ansi.StringWidth(text)
	}
	keep := m.width - tw - 2
	if keep < 0 {
		keep = 0
	}
	// 截断后追加 reset，防止未闭合样式渗透进 toast（ansi.Truncate 截断 styled
	// 行时样式 reset 可能落在 keep 点之后）
	line := ""
	if keep > 0 {
		line = ansi.Truncate(lines[idx], keep, "") + "\x1b[0m"
	}
	if m.width > 0 && m.width <= 2 {
		// 极端窄窗口（1-2 列）：分隔符 2 格就占满，原文放不下——按可用宽度
		// 截断（2 列保留类型图标 + "…"，1 列仅 "…"），覆盖行宽恒 ≤ m.width
		// 不折行。不用 ansi.Truncate(text, m.width, "…") 的原因：1 列预算下它
		// 仍会输出首字符整簇（如 "⚠" 1 格 + "…" = 2 格）超宽。
		if m.width == 2 {
			lines[idx] = ansi.Truncate(text, 2, "…")
		} else {
			lines[idx] = "…"
		}
	} else if m.width <= 0 {
		// 窗口尺寸未初始化（首帧/测试直接 View）：尚无宽度约束，按原文渲染
		lines[idx] = text
	} else {
		lines[idx] = line + "  " + text
	}
	return strings.Join(lines, "\n")
}

// onPlayerEvent 更新共享播放状态并同步首页。
func (m Model) onPlayerEvent(msg playerEventMsg) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{waitForPlayerEvents(m.player)}
	switch ev := msg.ev.(type) {
	case player.ProgressEvent:
		m.state.Position = ev.Position
		m.state.Duration = ev.Duration
		m.home = m.home.syncState(m.state)
		// 自动保存（节流）：播放中定期持久化会话，崩溃/断电也能恢复
		if time.Since(m.lastSave) >= saveInterval {
			m.lastSave = time.Now()
			m.saveSession()
		}
	case player.StateEvent:
		m.state.Playing = ev.Playing
		m.home = m.home.syncState(m.state)
	case player.TrackStartedEvent:
		m.retryCount = 0                   // 新曲加载成功，重试预算重置
		m.failedTracks = map[string]bool{} // 任何曲目加载成功 = 新一轮：本轮失败集合作废
		m.resuming = false                 // 恢复加载成功：进入正常播放态，恢复上下文作废
		m.ended = false
		// 仅在拿到真实时长时覆盖：Duration=0 表示 observe 与 Get 兜底
		// 均失败（直播/特殊流），此时保留搜索元数据提供的时长，避免被抹零。
		if ev.Duration > 0 {
			m.state.Duration = ev.Duration
		}
		m.home = m.home.syncState(m.state)
	case player.TrackEndedEvent:
		m.retryCount = 0 // 下一首开始，重试预算重置
		// 自动连播。解耦标记（删除当前曲）存在时：播放顺延曲目（当前位），
		// 无当前位则从头，队列为空则停止；否则正常推进到下一首。
		// 两种情况均不切换当前页面。
		if m.queueSkip {
			m.queueSkip = false
			if tr, ok := m.queue.Current(); ok {
				return m.beginPlay(tr)
			}
			if tr, ok := m.queue.Next(); ok {
				return m.beginPlay(tr)
			}
			return m.stopAfterEnd()
		}
		if tr, ok := m.queue.Next(); ok {
			return m.beginPlay(tr)
		}
		return m.stopAfterEnd()
	case player.ErrorEvent:
		// 取流失败（LoadFailedError）多为瞬态错误（如 YouTube 403 风控）：
		// 预算内自动重试；耗尽后队列有下一首则跳过继续连播，否则停止。
		// 其他错误（连接断开/重连失败）保持原有行为，不自动重试。
		var le *player.LoadFailedError
		if errors.As(ev.Err, &le) {
			// 播放自缓存文件时取流失败 → 缓存条目损坏（下载不完整/已过期）：
			// 移除条目 + 复位标记，后续重试/跳过自然回退网络 URL 重新取流。
			// fromCache 须在移除动作之前捕获（移除后 playingFromCache 已复位）。
			fromCache := m.playingFromCache && m.state.Track != nil
			if fromCache {
				m.cache.Remove(m.state.Track.ID)
				m.playingFromCache = false
			}
			// 提示区分两类失败：缓存损坏（从缓存播放失败，已删条目下次重下）
			// 与网络取流失败（file_error 诊断映射）；恢复中/重试中/耗尽跳过共用。
			var hint string
			if fromCache {
				hint = "缓存文件损坏，已移除（下次播放将重新下载）"
			} else {
				hint = loadFailureHint(le.FileError)
			}
			// 续播恢复（PlayPaused 静默加载）期间撞取流失败：不自动重试——
			// 恢复上下文已作废（重试会走 beginPlay→Play()：发声、从 0:00、
			// 非暂停，静默丢弃恢复语义），保留“恢复播放失败”语义：状态重置 + 横幅
			// 带 hint 诊断，队列保留展示（可查看/跳转其他曲目）；磁盘会话保留，
			// 下次启动重试。
			if m.resuming {
				m.resuming = false
				m, cmd := m.showToast("恢复播放失败: "+hint, toastError)
				m.state = model.PlaybackState{}
				m.home = m.home.syncState(m.state)
				m.queueSkip = false
				m.notifyTrack(nil)
				return m.syncQueueViews(), tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
			}
			if m.retryCount < maxPlayRetries {
				m.retryCount++
				m, cmd := m.showToast(fmt.Sprintf("播放失败：%s，正在自动重试（%d/%d）…", hint, m.retryCount, maxPlayRetries), toastWarning)
				m.state.Playing = false
				m.home = m.home.syncState(m.state)
				return m, tea.Batch(cmd, waitForPlayerEvents(m.player), retryPlayCmd(m.playGen))
			}
			// 重试耗尽：跳过失败曲目继续连播。注意 Next 现为回绕语义——单曲队列/
			// 重复 ID 会把失败曲目自身送回，继续播放会陷入“失败→重试→耗尽→
			// 重播失败曲”死循环，故候选与失败曲目同 ID 时视为无曲可跳，保留
			// “停止 + 等待用户操作”语义（ended=true）。
			var skip *model.Track
			if m.queueSkip {
				// 重试耗尽且存在删除解耦标记：镜像 TrackEnded 的兜底逻辑——
				// 指针已顺延，播放顺延曲目（当前位）而非 Next()，避免跳过头
				// （回归：TestLoadFailExhaustedSkipRespectsQueueSkip）。
				m.queueSkip = false
				if tr, ok := m.queue.Current(); ok {
					skip = &tr
				} else if tr, ok := m.queue.Next(); ok {
					skip = &tr
				}
			} else if tr, ok := m.queue.Next(); ok {
				skip = &tr
			}
			if skip != nil && (m.state.Track == nil || skip.ID != m.state.Track.ID) && !m.failedTracks[skip.ID] {
				// 跳过失败曲目继续连播（toast 告知用户哪首失败）。
				// 目标曲目已在本轮失败集合中（队列回绕撞回已失败曲目）：不跳，
				// 走下方停止路径，避免无限交替重播（回归：TestLoadFailAllTracksFailStopsLoop）。
				m2, cmd := m.skipFailedTrack(*skip, hint)
				return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))
			}
			// 单曲重试耗尽：停止播放，等待用户操作（空格重播同曲）
			m.ended = true
			m, cmd := m.showToast(fmt.Sprintf("播放失败：%s，已重试 %d 次。请稍后重试或更换歌曲", hint, maxPlayRetries), toastError)
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
		}
		m.retryCount = 0
		m.ended = true
		m, cmd := m.showToast(ev.Err.Error(), toastError)
		m.state.Playing = false
		m.home = m.home.syncState(m.state)
		return m, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
	}
	return m, tea.Batch(cmds...)
}

// waitForPlayerEvents 阻塞监听播放器事件流；通道关闭时返回 nil（循环终止）。
func waitForPlayerEvents(p player.Player) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-p.Events()
		if !ok {
			return nil
		}
		return playerEventMsg{ev: ev}
	}
}

// retryPlayMsg 触发一次自动重试（经 update 重新走 playQueueTrack，保证状态一致性）。
type retryPlayMsg struct{ gen int }

// retryPlayCmd 延迟 retryBackoff 后发送重试消息；期间用户换了歌（gen 不匹配）则丢弃。
func retryPlayCmd(gen int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(retryBackoff)
		return retryPlayMsg{gen: gen}
	}
}

// loadFailureHint 把 mpv 的 file_error 诊断文本映射为可操作的中文提示。
func loadFailureHint(fileErr string) string {
	switch {
	case strings.Contains(fileErr, "no audio or video data played"):
		return "YouTube 未返回可播放音轨（可能被风控、视频不可用或地区限制）"
	case strings.Contains(fileErr, "403"):
		return "YouTube 拒绝访问（风控/限流），可稍后重试"
	case strings.Contains(fileErr, "Couldn't resolve") || strings.Contains(fileErr, "resolve"):
		return "网络解析失败，请检查网络连接"
	case strings.Contains(fileErr, "unavailable"):
		return "视频不可用"
	case fileErr == "":
		return "mpv 无法播放该地址"
	default:
		return "播放出错：" + fileErr
	}
}

// skipFailedTrack 重试耗尽后跳过失败曲目：播放 tr 并设置跳过横幅。
// 失败曲目标题须在 beginPlay 前捕获（beginPlay 会替换 state.Track）。
func (m Model) skipFailedTrack(tr model.Track, hint string) (Model, tea.Cmd) {
	failed := m.state.Track
	m.retryCount = 0
	// 本轮已证明取流失败的曲目记入失败集合：队列回绕再次撞上它时停止而非重播
	//（集合在 TrackStartedEvent 清空：后续某曲成功加载即新一轮）。
	if failed != nil {
		m.failedTracks[failed.ID] = true
	}
	m2, cmd := m.beginPlay(tr)
	failedTitle := "当前歌曲"
	if failed != nil {
		failedTitle = failed.Title
	}
	m2, tcmd := m2.showToast(fmt.Sprintf("「%s」播放失败：%s，已重试 %d 次，跳过继续播放", failedTitle, hint, maxPlayRetries), toastWarning)
	return m2, tea.Batch(cmd, tcmd)
}

// stopAfterEnd 播放结束且无下一首：停在当前位置等待用户操作（空格重播同曲）。
func (m Model) stopAfterEnd() (Model, tea.Cmd) {
	m.ended = true
	m.state.Playing = false
	m.home = m.home.syncState(m.state)
	return m.syncQueueViews(), nil
}

// startPlay 手动播放某曲目（替换语义）：清空队列 → 该曲入队为当前 → 播放。
// 成功时并行触发 歌词 / 封面 / 历史 三个异步 cmd（核心链路 1）；
// Play 失败时跳过全部异步 cmd（失败播放不进历史、不请求歌词/封面），
// 状态重置为空回到"未在播放"空态 + 错误 toast。
func (m Model) startPlay(track model.Track) (Model, tea.Cmd) {
	m.queue.Replace(track)
	m.retryCount = 0    // 手动播放：全新重试预算
	m.queueSkip = false // 替换即重新对齐，解除删除解耦标记
	m.current = pageHome
	return m.playQueueTrack()
}

// playQueueTrack 播放队列当前曲目（不修改队列结构）。
func (m Model) playQueueTrack() (Model, tea.Cmd) {
	tr, ok := m.queue.Current()
	if !ok {
		return m, nil
	}
	return m.beginPlay(tr)
}

// beginPlay 核心播放流程：置播放状态、同步调用 player.Play（
// root_test 的 TestPlayFlow 在 update 返回后立即断言 playCount，故必须同步）、
// 刷新队列展示；成功时并行触发 歌词/封面/历史 三个异步 cmd，
// Play 失败时跳过全部异步 cmd，状态重置为空回到"未在播放"空态 + 错误 toast。
// 音频缓存：命中 → 播本地文件（playingFromCache 置位，后续 LoadFailed 时
// 据此移除损坏条目）；未命中 → 播网络 URL + 后台异步下载（不阻塞播放）。
// 注意：不重置 retryCount——自动重试也走本路径，重置会让重试预算
// 永不耗尽（回归：TestLoadFailRetriesExhaustedSkipsInQueue/StopsSingle）；
// 预算在 TrackStarted/TrackEnded/手动播放入口重置。
func (m Model) beginPlay(track model.Track) (Model, tea.Cmd) {
	m.resuming = false // 任何 beginPlay = 用户新意图或重试：恢复上下文作废
	// （重试路径不会在 resuming=true 时发生——恢复中取流失败走恢复失败分支，不调度重试）
	m.playGen++ // 播放代际递增：使在途重试消息过期（用户换曲后不再重试旧曲）
	m.ended = false
	m.state = model.PlaybackState{Track: &track, Playing: true, Duration: track.Duration}
	m.toast = nil // 新曲目开始 = 新状态：清除活跃 toast
	m.home = m.home.resetForTrack(&track)
	target := track.URL
	if path, ok := m.cache.Lookup(track.ID); ok {
		target = path
		m.playingFromCache = true
	} else {
		m.playingFromCache = false
		m.cache.CacheAsync(track) // 后台下载，不阻塞播放
	}
	if err := m.player.Play(target); err != nil {
		m, cmd := m.showToast("播放失败: "+err.Error(), toastError)
		m.state = model.PlaybackState{}
		m.home = m.home.syncState(m.state)
		m.queuePage = m.queuePage.sync(m.queue)
		m.notifyTrack(nil)
		return m, cmd
	}
	m.notifyTrack(&track)
	// 按模式设置单曲循环（mpv loop-file 是 per-file 属性，新 loadfile 自动重置，
	// 但 UI 显式设置保证切歌/切模式后循环状态与模式始终同步）。SetLoop 是
	// 同步调用，失败仅记 toast 不阻断播放；返回 cmd 结构与原来一致
	// （与歌词/封面/历史 fetch cmds 的 tea.Batch 合并）。
	var loopCmd tea.Cmd
	if err := m.player.SetLoop(m.queue.Mode() == queue.RepeatOne); err != nil {
		m, loopCmd = m.showToast("设置循环失败: "+err.Error(), toastError)
	}
	return m.syncQueueViews(), tea.Batch(
		loopCmd,
		fetchLyricsCmd(m.lyrics, track),
		fetchCoverCmd(m.cover, track),
		addHistoryCmd(m.history, track),
	)
}

// showToast 显示一条 toast（覆盖语义）：替换旧 toast 并重置消失定时器。
// 返回的 cmd 产生 toastExpireMsg（时长见 toastDuration）。
func (m Model) showToast(text string, kind toastKind) (Model, tea.Cmd) {
	m.toastID++
	id := m.toastID
	m.toast = &toast{text: text, kind: kind, id: id}
	return m, tea.Tick(toastDuration(kind), func(time.Time) tea.Msg {
		return toastExpireMsg{id: id}
	})
}

// notifyTrack 把当前曲目同步给外部消费者（如 MPRIS 服务）。回调在 tea
// update 循环内同步执行，外部实现需自行保证并发安全；onTrack 为 nil 时
// 直接跳过。
func (m Model) notifyTrack(t *model.Track) {
	if m.onTrack != nil {
		m.onTrack(t)
	}
}

// saveSession 把当前播放会话写入磁盘（尽力而为，失败仅记日志）。
// 无播放中曲目时删除会话文件（会话自然结束，避免恢复陈旧状态）。
func (m Model) saveSession() {
	if m.state.Track == nil {
		if err := m.session.Clear(); err != nil {
			log.Printf("清除会话失败: %v", err)
		}
		return
	}
	st := session.State{
		Queue:    m.queue.Snapshot(),
		Position: m.state.Position,
		Ended:    m.ended,
	}
	if err := m.session.Save(st); err != nil {
		log.Printf("保存会话失败: %v", err)
	}
}

// resumeCmd 续播恢复：PlayPaused 静默加载当前曲目并定位（不发声；定位随
// loadfile 的 start= 选项原子完成，避免加载窗口内 seek 被 mpv 拒绝的竞态，
// 见 mpv.go PlayPaused）。命中缓存 → 播本地文件（fromCache 标记回填至
// playingFromCache，异步 LoadFailedError 时据此移除损坏条目）；未命中 →
// 播网络 URL，并触发后台下载（下载完成即缓存，下次恢复/播放走本地）。
// IPC 层失败（PlayPaused 命令被拒）与缓存文件无关，不删条目。
// CacheAsync 在 PlayPaused 之前触发，故 IPC 失败时下载仍会进行（缓存预热
// 供下次恢复命中，与 beginPlay 一致），并非缺陷。
func resumeCmd(m Model) tea.Cmd {
	track := m.resume.track
	pos := m.resume.pos
	return func() tea.Msg {
		target := track.URL
		fromCache := false
		if path, ok := m.cache.Lookup(track.ID); ok {
			target = path
			fromCache = true
		} else {
			// 与 beginPlay 对齐：恢复播放的歌曲也后台下载，下载完成即缓存；
			// 不阻塞恢复加载（CacheAsync 对 Disabled/已存在条目是 no-op 安全）。
			m.cache.CacheAsync(track)
		}
		if err := m.player.PlayPaused(target, pos); err != nil {
			return resumeResultMsg{err: err, fromCache: fromCache}
		}
		return resumeResultMsg{fromCache: fromCache}
	}
}

func fetchLyricsCmd(c *lyrics.Client, track model.Track) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ly, err := c.Fetch(ctx, track)
		return lyricsResultMsg{trackID: track.ID, lyrics: ly, err: err}
	}
}

func fetchCoverCmd(f *cover.Fetcher, track model.Track) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path, err := f.Fetch(ctx, track)
		return coverResultMsg{trackID: track.ID, path: path, err: err}
	}
}

func addHistoryCmd(h *history.Store, track model.Track) tea.Cmd {
	return func() tea.Msg {
		return historyResultMsg{err: h.Add(track)}
	}
}

// cycleMode 三态循环切换播放模式：Sequential→Shuffle→RepeatOne→Sequential。
// 首页模式按钮（toggleModeMsg）与队列页 s 键（queueModeMsg）共用。
// 切换时同步 mpv 单曲循环状态：切入 RepeatOne → SetLoop(true)（当前文件
// 开始无缝循环）；切出 → SetLoop(false)（解除正在循环的文件，否则 mpv
// 不再产生 TrackEnded，UI 无法感知结束）。失败仅记 toast 不阻断。
func (m Model) cycleMode() (Model, tea.Cmd) {
	var next queue.Mode
	switch m.queue.Mode() {
	case queue.Sequential:
		next = queue.Shuffle
	case queue.Shuffle:
		next = queue.RepeatOne
	default:
		next = queue.Sequential
	}
	m.queue.SetMode(next)
	if err := m.player.SetLoop(next == queue.RepeatOne); err != nil {
		m, cmd := m.showToast("设置循环失败: "+err.Error(), toastError)
		return m.syncQueueViews(), cmd
	}
	return m.syncQueueViews(), nil
}

// togglePlay 全局空格：播放中→暂停；已结束/出错→重播同曲（ended）；
// 暂停→继续；未播放时忽略。
func (m Model) togglePlay() (Model, tea.Cmd) {
	if m.ended {
		return m.restartSameTrack()
	}
	if m.state.Track == nil {
		return m, nil
	}
	if m.state.Playing {
		return m, func() tea.Msg {
			return playerOpResultMsg{err: m.player.Pause()}
		}
	}
	return m, func() tea.Msg {
		return playerOpResultMsg{err: m.player.Resume()}
	}
}

// restartSameTrack 播放结束/出错后空格 → 重播当前歌曲。
// mpv 存活时正常重载；mpv 已死时 Play 失败会走 startPlay 失败路径
// （重置状态并报错），行为自洽。Track 为 nil（如播放失败已重置）时忽略。
func (m Model) restartSameTrack() (Model, tea.Cmd) {
	if m.state.Track == nil {
		return m, nil
	}
	return m.startPlay(*m.state.Track)
}

// seekCmd 首页 ←/→ 触发的 seek（绝对位置，UI 侧已 clamp）。
func seekCmd(p player.Player, target float64) tea.Cmd {
	return func() tea.Msg {
		return playerOpResultMsg{err: p.Seek(target)}
	}
}

func emitTrackSelected(track model.Track) tea.Cmd {
	return func() tea.Msg { return trackSelectedMsg{track: track} }
}

func emitTrackAppend(track model.Track) tea.Cmd {
	return func() tea.Msg { return trackAppendMsg{track: track} }
}

func emitQueuePlay(index int) tea.Cmd {
	return func() tea.Msg { return queuePlayMsg{index: index} }
}

func emitQueueDelete(index int) tea.Cmd {
	return func() tea.Msg { return queueDeleteMsg{index: index} }
}

func emitQueueClear() tea.Cmd {
	return func() tea.Msg { return queueClearMsg{} }
}

func emitQueueMode() tea.Cmd {
	return func() tea.Msg { return queueModeMsg{} }
}

func emitDeleteEntry(id, source string) tea.Cmd {
	return func() tea.Msg { return deleteEntryMsg{id: id, source: source} }
}

func emitClearHistory() tea.Cmd {
	return func() tea.Msg { return clearHistoryMsg{} }
}

// onMouse 处理鼠标事件：Tab 栏（屏幕第 2 行 Y==1，bubbletea X/Y 为 0-based）——
// 点击标签（左键按下）切换页面，移动更新悬停高亮；Y==0 为顶部空行、Y==2 为
// 分隔线行（点击/移动与页面区行为一致：清除悬停并委托页面，无特殊处理）；
// 页面区（Y>=3）事件不拦截，交给当前页面（歌词区 viewport 原生支持滚轮；
// bubbles v1.0.0 的列表/输入框暂无鼠标处理）。
func (m Model) onMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	// 选择器打开时忽略一切鼠标事件（与“选择器打开时所有输入交给选择器”的语义一致）：
	// 点击 Tab 栏/页面区域不得穿透改 m.current 或落到页面。
	if m.plPicker != nil {
		return m, nil
	}
	if msg.Y != 1 {
		// 鼠标不在 Tab 栏（空行/分隔线/页面区）：清除悬停高亮，事件交给页面
		if m.hoverTab >= 0 {
			m.hoverTab = -1
		}
		return m.delegate(msg)
	}
	p, ok := m.tabHitAt(msg.X)
	switch msg.Action {
	case tea.MouseActionMotion:
		if ok {
			m.hoverTab = int(p)
		} else {
			m.hoverTab = -1
		}
		return m, nil // 悬停事件不落到页面
	case tea.MouseActionPress:
		m.hoverTab = -1 // 点击后清除悬停
		if msg.Button == tea.MouseButtonLeft && ok {
			m.current = p
		}
		return m, nil
	}
	return m, nil // 其余（释放/滚轮等）在 Tab 栏上不处理
}

// switchPage 处理切页按键：Tab/Ctrl+Right 正向循环
// （首页→队列→播放列表→搜索→历史→首页）、Shift+Tab/Ctrl+Left 反向循环、
// 1/2/3/4/5 直达。
func (m Model) switchPage(key string) Model {
	switch key {
	case "1":
		m.current = pageHome
	case "2":
		m.current = pageQueue
	case "3":
		m.current = pagePlaylists
	case "4":
		m.current = pageSearch
	case "5":
		m.current = pageHistory
	case "shift+tab", "ctrl+left":
		m.current = page((int(m.current) + 4) % 5)
	default: // tab, ctrl+right
		m.current = page((int(m.current) + 1) % 5)
	}
	return m
}

// delegate 把消息交给当前页面处理。
func (m Model) delegate(msg tea.Msg) (Model, tea.Cmd) {
	switch m.current {
	case pageHome:
		var cmd tea.Cmd
		m.home, cmd = m.home.Update(msg)
		return m, cmd
	case pageQueue:
		var cmd tea.Cmd
		m.queuePage, cmd = m.queuePage.Update(msg)
		return m, cmd
	case pagePlaylists:
		var cmd tea.Cmd
		m.plPage, cmd = m.plPage.Update(msg)
		return m, cmd
	case pageSearch:
		var cmd tea.Cmd
		m.searchPage, cmd = m.searchPage.Update(msg)
		return m, cmd
	case pageHistory:
		var cmd tea.Cmd
		m.historyPage, cmd = m.historyPage.Update(msg)
		return m, cmd
	}
	return m, nil
}

// typingText 返回是否有输入框处于聚焦（搜索关键词/播放列表命名）：
// 聚焦时字符类全局键（空格/a/q）让位给输入框。
func (m Model) typingText() bool {
	switch m.current {
	case pageSearch:
		return m.searchPage.typing()
	case pagePlaylists:
		return m.plPage.typing()
	}
	return false
}

// selectedTrack 返回当前页面选中的歌曲（供全局 a 键添加到播放列表）；
// 首页/队列页无选中歌曲语义，返回 false。
func (m Model) selectedTrack() (model.Track, bool) {
	switch m.current {
	case pageSearch:
		return m.searchPage.selectedTrack()
	case pageHistory:
		return m.historyPage.selectedTrack()
	case pagePlaylists:
		return m.plPage.selectedTrack()
	}
	return model.Track{}, false
}

// syncQueueViews 队列变化后同步队列页与首页的队列信息展示。
// 首页展示 1 基位置：currentIdx=-1（无当前曲目）时显示 0。
func (m Model) syncQueueViews() Model {
	m.home = m.home.setQueueInfo(m.queue.CurrentIndex()+1, m.queue.Len(), m.queue.Mode())
	m.queuePage = m.queuePage.sync(m.queue)
	return m
}

// refreshHistory 从 store 重新加载历史并刷新页面。
func (m Model) refreshHistory() Model {
	m.historyPage = m.historyPage.setEntries(m.history.Entries())
	return m
}

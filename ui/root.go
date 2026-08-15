// Package ui 实现 bubbletea 五页面（首页/队列/播放列表/搜索/历史）与全局事件路由。
package ui

import (
	"context"
	"errors"
	"fmt"
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
	"music-tui/local"
	"music-tui/logger"
	"music-tui/lyrics"
	"music-tui/lyricshm"
	"music-tui/model"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/preload"
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

// fallbackState 缓存兜底状态机。active：ErrorEvent 后等待下载完成（阻断 URL 重试，
// 避免与下载并发访问同一 URL 放大 403 风控——回归：连播未缓存下一首卡住）；
// beginPlay 注册的“仅监听”（WaitDone）不置 active。canceled：用户手动操作取消。
type fallbackState struct {
	active        bool
	trackID       string
	gen           int    // 发起时的播放代际（消息 gen 校验）
	hint          string // 原错误提示（放弃兜底时复用）
	isLoadTimeout bool   // 原错误类型（放弃兜底时复用）
	canceled      bool
}

// cacheFallbackDoneMsg 缓存下载完成（成功或失败都触发）；gen 不匹配/已取消则丢弃。
type cacheFallbackDoneMsg struct {
	trackID string
	gen     int
}

// cacheFallbackTimeoutMsg 兜底等待超时（fallbackWaitTimeout）：放弃等待，恢复现有重试链路。
type cacheFallbackTimeoutMsg struct {
	gen int
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

// trackInsertNextMsg 选择器请求把曲目插入到当前曲之后（下一首播放）。
type trackInsertNextMsg struct {
	track model.Track
}

// lyricsResultMsg 歌词异步加载结果；title/artist 为 AI 识别出的清洗后
// 歌名/歌手（空 = 无 AI 信息，展示回落原始标题）。
type lyricsResultMsg struct {
	trackID string
	lyrics  *lyrics.Lyrics
	title   string
	artist  string
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

// fallbackWaitTimeout 缓存兜底等待下载完成的上限（包级变量：测试可调小）。
var fallbackWaitTimeout = 90 * time.Second

// Model 顶层模型：持有共享播放状态、播放队列与五个页面，负责全局按键、
// 页面切换、服务调用与结果路由。
type Model struct {
	player  player.Player
	lyrics  lyrics.Fetcher
	cover   *cover.Fetcher
	history *history.Store
	queue   *queue.Queue
	session *session.Store
	pl      *playlists.Store
	yt      *ytm.Client // YT Music 同步客户端；nil = 未集成（测试/降级）

	// ytdlpConfigured 是否已配置 yt-dlp cookie/headers（main 组装时注入）：
	// 未配置时取流风控类失败（YouTube 403/风控）的提示附加 YT Music 登录引导。
	ytdlpConfigured bool

	cache            *cache.Manager // 音频缓存（命中优先本地文件；未命中后台下载）
	playingFromCache bool           // 当前曲目是否播放自缓存文件（LoadFailed 时据此移除损坏条目）
	// preloader 预加载调度器：队列播放时自动预下载"即将播放的下一首"到缓存
	//（TrackStarted 后触发，队列/模式/播放状态变更时联动重算；缓存未配置时 no-op）
	preloader *preload.Scheduler

	state    model.PlaybackState
	current  page
	width    int // 窗口宽度（分隔线按此宽度渲染，不写死）
	height   int // 页面 body 高度（WindowSizeMsg 时 = 窗口高度 - 4：顶部空行+Tab+分隔线 3 行 + 状态栏 1 行）
	hoverTab int // Tab 栏悬停标签下标（= page 枚举值）；-1 = 无悬停
	// toast 活跃 toast（单条覆盖；定时自动消失，不参与布局）。替代旧 lastError/notice 横幅。
	toast   *toast
	toastID uint64 // toast 自增 id：过期消息按 id 匹配，防误清被覆盖后的新 toast
	ended   bool   // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）
	// loadingSince 当前曲目加载起始时刻（零值 = 非加载中）：beginPlay 设置，
	// TrackStarted/TrackEnded/Error 清除；spinner tick 据此派生首页"加载中…"
	// 提示（回归：连播未缓存下一首取流悬挂卡住时用户可感知）。
	loadingSince time.Time

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
	// playStarted 当前曲目是否已开始播放（TrackStartedEvent 到达置 true；
	// beginPlay/恢复成功重置 false）——缓存兜底据此判断“mpv 未开始播放”时切本地。
	playStarted bool
	// fallback 缓存兜底状态：mpv URL 播放失败/未开始 + 缓存下载完成后改用本地文件播放。
	// 详见 fallbackState 注释。
	fallback fallbackState
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

	mprisCtrl *MprisController // MPRIS 控制器桥（NewModel 创建，main 注入 mpris 服务）
}

// NewModel 组装 UI。p/s 为接口（可注入 fake 测试），l/c/h/sess/pl/cm/yt 为具体服务，
// cm 为音频缓存管理器（nil = 未集成），yt 为 YT Music 同步客户端（nil = 未集成），
// onTrack 在播放状态变化时同步回调当前曲目（nil 表示无曲目；可为 nil）。
// ytdlpConfigured 表示已配置 yt-dlp cookie/headers（未配置且取流风控类失败时，
// 失败提示附加 YT Music 登录 cookie 配置引导）。
// 若 sess 存在已保存会话(队列 + 进度),同步恢复队列与播放状态(暂停态),
// mpv 的静默加载由 Init 返回的 resumeCmd 完成。
// lyricFile 为歌词行实时写入器(nil = 不启用,如测试环境)。
func NewModel(p player.Player, s search.SearchAdapter, l lyrics.Fetcher, c *cover.Fetcher, h *history.Store, sess *session.Store, pl *playlists.Store, cm *cache.Manager, yt *ytm.Client, onTrack func(*model.Track), ytdlpConfigured bool, lyricFile *lyricshm.Writer) Model {

	m := Model{
		player:          p,
		lyrics:          l,
		cover:           c,
		history:         h,
		queue:           queue.New(),
		session:         sess,
		pl:              pl,
		cache:           cm,
		yt:              yt,
		onTrack:         onTrack,
		ytdlpConfigured: ytdlpConfigured,
		current:         pageHome,
		hoverTab:        -1,
		failedTracks:    map[string]bool{},
		home:            newHomeModel(p),
		searchPage:      newSearchModel(s),
		historyPage:     newHistoryModel(),
		queuePage:       newQueueModel(),
		plPage:          newPlaylistModel(),
	}
	m.home.lyricFile = lyricFile
	// 预加载调度器随模型创建：cm 可为 nil（未配置缓存），Scheduler 内部已
	// 做 nil 安全 no-op（SetTarget 直接返回，worker 恒无目标）。
	m.preloader = preload.New(cm)
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
	// MPRIS 控制器桥：必须在 queue 最终确定后创建（恢复失败分支会重建 queue）。
	// 方法签名与 mpris 包 controller 接口一致（隐式满足，编译期由 main 检查）。
	m.mprisCtrl = &MprisController{reqs: make(chan mprisReq, 16), q: m.queue}
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

// refreshPreload 重新计算并更新预加载目标：仅在"有当前曲目、未结束、非单曲循环"
// 时预载队列下一首（PeekNext）；其余情况（无当前/ended/RepeatOne/空队列）SetTarget(nil)。
//
// 额外门控——PeekNext 回绕返回当前曲自身（单曲队列）时不设目标：同 ID 曲目的
// 缓存已由当前曲 TrackStarted 的预热覆盖，预载纯属重复；且在 TrackStarted 之前
//（startPlay/切歌后立即刷新）预载当前曲会与 mpv 取流并发访问同一 URL，放大
// 403 风控（与"预热移后到 TrackStarted"同一回归动机）。
//
// 调用点：TrackStartedEvent（预热当前曲之后）、全部队列形态变更（增/删/清/跳转/
// 替换）、模式切换（cycleMode）、播放停止（stopAfterEnd、ErrorEvent 置 ended 分支）、
// 失败跳过（skipFailedTrack 成功后立即）、重试期间队列被清空（retryPlayMsg 空队列
// 分支）——即所有"下一首候选"或"播放状态"可能变化之处。注意 Model 为值接收者：本方法只
// 更新 preloader（指针字段）的目标槽位，不依赖调用方副本的后续状态，
// 因此在"分支返回前"调用即可生效（scheduler 是共享指针）。
func (m Model) refreshPreload() {
	if m.ended || m.state.Track == nil || m.queue.Mode() == queue.RepeatOne {
		// 无当前曲/已结束/单曲循环：不预载（RepeatOne 下 mpv 无缝循环同曲，
		// queue 指针照常推进——预载"下一首"是浪费，当前曲 TrackStarted 已缓存）
		m.preloader.SetTarget(nil)
		return
	}
	if next, ok := m.queue.PeekNext(); ok && next.ID != m.state.Track.ID {
		m.preloader.SetTarget(&next)
		return
	}
	// 空队列或回绕到当前曲自身：无有效预载目标
	m.preloader.SetTarget(nil)
}

// Init 启动两个常驻 cmd：播放器事件监听 + spinner 全局 tick；
// 存在已保存会话时追加续播恢复 cmd（PlayPaused 静默加载并定位）。
// （不用包级 spinner.Tick：bubbles v1.0.0 的包级 Tick 无延时，会形成忙循环。）
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{waitForPlayerEvents(m.player), spinnerTick, subscribeMprisReqs(m.mprisCtrl.reqs)}
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
		// 队列形态变化：下一首候选已变，重新计算预加载目标
		m.refreshPreload()
		return m.syncQueueViews(), nil

	case trackInsertNextMsg:
		// 插入到当前曲之后（下一首播放）：不打断当前播放，也不自动开播
		m.queue.InsertNext(msg.track)
		// 队列形态变化：下一首候选已变，重新计算预加载目标
		m.refreshPreload()
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
		m2, cmd := m.playQueueTrack()
		// 跳转 = 当前曲变更：在返回的新副本上重算预加载目标
		//（scheduler 为指针，目标槽位更新对副本同样生效）
		m2.refreshPreload()
		return m2, cmd

	case prevTrackMsg:
		// 上一首（首页 , 键 / ⏮ 按钮）：手动操作重置重试预算并解除删除解耦标记
		if tr, ok := m.queue.Prev(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload() // 切歌后重算预加载目标（返回副本上生效）
			return m2, cmd
		}
		return m, nil

	case nextTrackMsg:
		// 下一首（首页 . 键 / ⏭ 按钮）：同上
		if tr, ok := m.queue.Next(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload() // 切歌后重算预加载目标（返回副本上生效）
			return m2, cmd
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
		m.refreshPreload() // 删除后下一首候选可能变化：重新计算预加载目标
		return m.syncQueueViews(), nil

	case queueClearMsg:
		m.queue.Clear()
		m.queueSkip = false
		m.refreshPreload() // 队列清空：无下一首可预载（当前曲若仍在播则清空目标）
		return m.syncQueueViews(), nil

	case queueModeMsg:
		// 队列页 s 键：与首页模式按钮共用三态循环
		return m.cycleMode()

	case mprisReqMsg:
		return m.handleMprisReq(msg.req)

	case queueMoveMsg:
		// 队列页移动模式：把 from 下标曲目移到最终下标 to（currentIdx 跟随
		// 同一首歌）。非法（越界/from==to）由 queue.Move 拒绝，忽略。
		if !m.queue.Move(msg.from, msg.to) {
			return m, nil
		}
		m.refreshPreload() // 移动跨越当前曲时"下一首"候选变化
		return m.syncQueueViews(), nil

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
		m2, cmd := m.playQueueTrack()
		m2.refreshPreload() // 整列表替换 = 队列形态变更：重新计算预加载目标
		return m2, cmd

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

	case plLocalAddMsg:
		// 本地路径导入：同步扫描路径（文件/目录）并把歌曲加入概览选中的列表。
		// 本地磁盘扫描毫秒级，同步执行即可。成功退出输入框并刷新列表页；
		// 失败（路径不存在/无音频/AddTracks 错误）仅 toastError——页面提交后
		// 留在输入模式，用户可直接改路径重试（对比 u 导入：失败需重新按 u）。
		path := strings.TrimSpace(msg.path)
		if path == "" {
			return m, nil
		}
		item, ok := m.plPage.overview.SelectedItem().(overviewItem)
		if !ok {
			m, cmd := m.showToast("请先在概览选择要添加的播放列表", toastError)
			return m, cmd
		}
		tracks, err := local.Scan(path)
		if err != nil {
			m, cmd := m.showToast(err.Error(), toastError)
			return m, cmd
		}
		if err := m.pl.AddTracks(item.list.Name, tracks); err != nil {
			m, cmd := m.showToast("添加失败: "+err.Error(), toastError)
			return m, cmd
		}
		m.plPage = m.plPage.exitLocalAdd()
		m.plPage = m.plPage.setLists(m.pl.Lists())
		m, cmd := m.showToast(fmt.Sprintf("已从 %s 添加 %d 首到「%s」", path, len(tracks), item.list.Name), toastSuccess)
		return m, cmd

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
			m.refreshPreload() // ended 门控：清空预加载目标
			m, cmd := m.showToast("播放失败：队列已清空，已停止自动重试", toastError)
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, tea.Batch(cmd, waitForPlayerEvents(m.player))
		}
		m2, cmd := m.playQueueTrack()
		return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))

	case cacheFallbackDoneMsg:
		// 监听身份校验：代际不匹配（用户已切歌/重播）或已取消（用户暂停）→ 丢弃
		if msg.gen != m.playGen || m.fallback.canceled || m.fallback.trackID != msg.trackID {
			return m, waitForPlayerEvents(m.player)
		}
		if m.state.Track == nil || m.state.Track.ID != msg.trackID {
			return m, waitForPlayerEvents(m.player)
		}
		// 已开始播放/已播本地/已结束/当前曲已被删除（queueSkip）→ 无需兜底，丢弃
		if m.playStarted || m.playingFromCache || m.ended || m.queueSkip {
			return m, waitForPlayerEvents(m.player)
		}
		if path, ok := m.cache.Lookup(msg.trackID); ok {
			logger.Warn("缓存下载完成，改用本地文件: %s", path)
			m2, cmd := m.beginPlay(*m.state.Track)
			m2, tcmd := m2.showToast("已改用缓存文件播放", toastSuccess)
			return m2, tea.Batch(cmd, tcmd, waitForPlayerEvents(m.player))
		}
		// 下载失败（信号关闭但未命中）：若处于兜底等待 → 放弃，走现有重试链路（原错误提示）
		if m.fallback.active {
			m.fallback.active = false
			return m.retryOrSkipLoadFailure(m.fallback.hint, m.fallback.isLoadTimeout, []tea.Cmd{waitForPlayerEvents(m.player)})
		}
		return m, waitForPlayerEvents(m.player)

	case cacheFallbackTimeoutMsg:
		// 丢弃：代际不匹配/兜底未激活/已开始播放（悬挂恢复，P1 回归：
		// TestCacheFallbackTrackStartedResetsActive）/已结束
		if msg.gen != m.playGen || !m.fallback.active || m.playStarted || m.ended {
			return m, waitForPlayerEvents(m.player)
		}
		logger.Warn("缓存兜底等待超时(%s)，恢复自动重试", fallbackWaitTimeout)
		m.fallback.active = false
		return m.retryOrSkipLoadFailure(m.fallback.hint, m.fallback.isLoadTimeout, []tea.Cmd{waitForPlayerEvents(m.player)})

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
			m.resuming = false           // 恢复上下文作废
			m.loadingSince = time.Time{} // 恢复失败：无加载中提示
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
		// 恢复加载进行中：取流可能悬挂（网络曲目加载窗口达数秒），首页显示
		// "⏳ 加载中…"（2s 未收到 TrackStarted 即提示）——恢复路径此前无加载
		// 提示（loadingSince 只在 beginPlay 设置，审查 P2-1）。TrackStartedEvent
		// 到达时统一清除（现有分支已清），失败分支已置零。
		m.loadingSince = time.Now()
		// 恢复加载进行中：mpv 未开始播放（TrackStartedEvent 到达统一置 true），
		// 缓存兜底监听（beginPlay 的 WaitDone）在恢复期间到达时据此判断。
		m.playStarted = false
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
			// 非 NotFound 错误（lrclib 超时/服务端错误）打日志便于诊断：
			// AI 识别失败已在 lyrics 包打印，此处覆盖 lrclib 链。
			if msg.err != nil && !errors.Is(msg.err, lyrics.ErrNotFound) {
				logger.Warn("歌词拉取失败（lrclib 链）: %v", msg.err)
			}
			m.home = m.home.setLyrics(msg.err, msg.lyrics)
			if msg.err == nil && msg.title != "" {
				// AI 识别结果：全局展示覆盖（控制栏/状态栏/队列当前项）
				// + MPRIS 回调（onTrack 以清洗后曲目副本重发；mpris 服务端
				// 仅 TrackStartedEvent 发布元数据，实际保持原始标题，见 19.8）。
				m.home = m.home.setAITrack(msg.title, msg.artist)
				m.queuePage = m.queuePage.setAITrack(msg.title, msg.artist)
				m = m.syncQueueViews() // 队列页立即重建当前项展示（不依赖下次队列事件）
				if m.onTrack != nil && m.state.Track != nil {
					t := *m.state.Track
					t.Title, t.Artist = msg.title, msg.artist
					m.onTrack(&t)
				}
			}
		}
		return m, nil

	case coverResultMsg:
		if m.state.Track != nil && msg.trackID == m.state.Track.ID {
			m.home = m.home.setCover(msg.trackID, msg.path, msg.err)
		}
		return m, nil

	case historyResultMsg:
		if msg.err != nil {
			logger.Warn("写入历史失败（不影响播放）: %v", msg.err)
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
		// 加载中派生状态：切歌 2s 未收到 TrackStarted → 首页显示"加载中…"提示
		//（msg.Time 是 tick 发送时刻：测试可注入任意时间，无需真实等待 2s）。
		m.home.loading = !m.loadingSince.IsZero() && msg.Time.Sub(m.loadingSince) >= 2*time.Second
		return m, spinnerTick

	case tea.WindowSizeMsg:
		// 顶部空行 + Tab 栏 + 分隔线占 3 行、底部状态栏占 1 行，页面高度相应减 4
		m.width = msg.Width
		m.height = msg.Height - 4
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
// 页面自第 4 行起）；活跃 toast 左对齐覆盖在最后一行（状态栏行），报错期间
// 临时显示、消失后恢复（不参与布局，行数不变）。
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
	body = m.padBody(body)
	out := "\n" + m.tabBar() + "\n" + body + "\n" + m.statusBarView()
	return m.overlayToast(out)
}

// padBody 把页面 body 垂直填充到页面高度（m.height），使底部状态栏恒在屏幕
// 最后一行：内容不满一屏的页面（搜索/队列/播放列表/历史空态、列表短、选择器
// 内容少）此前 body 行数不足，状态栏随内容上浮不贴底（回归：搜索页状态栏
// 不再最底部）。恰好撑满/超高的 body 原样返回（不截断）；未收到 WindowSizeMsg
// （width≤0）时原样返回。
func (m Model) padBody(body string) string {
	if m.width <= 0 || m.height <= 0 {
		return body
	}
	if strings.Count(body, "\n")+1 >= m.height {
		return body
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, body)
}

// statusBarView 底部常驻状态栏（恒 1 行，布局稳定）：首页自身已展示曲目
// 信息（控制栏：标题/播放状态/模式/队列位置），状态栏与之重复——首页时
// 状态栏行留空（行恒存在，布局稳定）；其余页面三段式布局：左 = 歌曲名
// （截断，AI 识别结果优先）、中 = 当前歌词行（居中，无歌词/无高亮时留空）、
// 右 = 播放状态 + 模式 + 队列位置。toast 活跃时本行被左对齐临时覆盖
// （报错期间显示报错消息），消失后恢复。
func (m Model) statusBarView() string {
	// 首页控制栏已展示曲目信息，状态栏留空（View 的 "\n" + "" 仍保持行数）
	if m.current == pageHome {
		return ""
	}
	left := "未在播放"
	lyric := ""
	if m.state.Track != nil {
		left = m.home.trackLabel() // AI 识别结果展示覆盖（见 19 章）
		// 中间段：当前歌词行（无歌词/无高亮时留空）
		lyric = m.home.currentLyricText()
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
	// 三段布局：右顺序优先完整 → 左名称按剩余 40% 截断 → 中间歌词行在
	// left 与 right 之间居中（无歌词时留空）。
	rightRendered := style.Render(right)
	rightW := ansi.StringWidth(rightRendered)
	// 极端窄窗口（宽度小于右侧顺序文本）：右侧截断兜底，恒 1 行不折行
	// （与 overlayToast 的 width≤2 兜底同模式）。
	if rightW >= m.width {
		right = ansi.Truncate(right, m.width, "…")
		rightRendered = style.Render(right)
		rightW = ansi.StringWidth(rightRendered)
	}
	leftMax := (m.width - rightW - 2) * 2 / 5 // 名称最多占剩余 40%
	if leftMax < 0 {
		leftMax = 0
	}
	left = ansi.Truncate(left, leftMax, "…")
	leftRendered := style.Render(left)
	leftW := ansi.StringWidth(leftRendered)
	// 中间区域 = 总宽 - 左 - 右 - 2 格间距；歌词行在其中居中截断
	midW := m.width - leftW - rightW - 2
	if midW < 0 {
		midW = 0
	}
	midRendered := ""
	if midW > 0 && lyric != "" {
		lyric = ansi.Truncate(lyric, midW, "…")
		// 高亮与首页歌词区一致：加粗 + 粉色 212（共享 lyricActiveStyle；
		// 无 Faint，转义序列零宽度，宽度计算不受影响）。
		midRendered = lyricActiveStyle.Render(lyric)
		midPad := (midW - ansi.StringWidth(midRendered)) / 2
		if midPad > 0 {
			midRendered = strings.Repeat(" ", midPad) + midRendered
		}
	}
	// 组装：left + 1 空格 + mid（居中）+ 1 空格 + right。
	// 一侧无内容时该侧间距归零（总宽 = m.width 或 m.width-1，恒不超宽不折行）。
	midRendered += strings.Repeat(" ", midW-ansi.StringWidth(midRendered))
	padL := 1
	if leftW == 0 || midW == 0 {
		padL = 0
	}
	padR := 1
	if rightW == 0 || midW == 0 {
		padR = 0
	}
	return leftRendered + strings.Repeat(" ", padL) + midRendered + strings.Repeat(" ", padR) + rightRendered
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

// overlayToast 把活跃 toast 渲染到最后一行（状态栏行）左对齐：报错期间该行
// 显示报错消息（状态栏内容临时被覆盖），自动消失后状态栏内容恢复——行数
// 恒不变、其余行逐字不变，报错出现/消失排版零跳动（toast 不参与布局）。
// 超宽按窗口宽度截断（保头部，尾部省略号）；窗口尺寸未初始化时按原文渲染。
// 极端窄窗口（m.width=1）下截断结果仅为 "…"（1 格）不折行——真实终端不可达。
func (m Model) overlayToast(out string) string {
	if m.toast == nil || out == "" {
		return out
	}
	lines := strings.Split(out, "\n")
	idx := len(lines) - 1 // 最后一行 = 状态栏行
	text := m.toastText(*m.toast)
	if m.width > 0 {
		// 左对齐 + 尾部省略号：保头部（错误类型/消息开头），超宽截断。
		// 整行替换无样式渗透风险（不截断原行内容）。ansi.Truncate 对
		// lipgloss 样式安全：截断点后的样式转义（含尾部 \x1b[0m）原样保留，
		// 样式行内自闭合，尾部省略号正常显示；tail 宽度计入 length，
		// 结果恒 ≤ m.width 不折行。
		text = ansi.Truncate(text, m.width, "…")
	}
	lines[idx] = text
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
		m.playStarted = true               // mpv 已开始播放（缓存兜底据此不再切本地）
		m.ended = false
		m.loadingSince = time.Time{} // 加载成功：加载中提示结束
		// mpv 已开始播放：缓存兜底状态作废（active 复位——否则悬挂恢复后 90s 超时
		// 消息会对正在播放的曲目伪重试，回归：TestCacheFallbackTrackStartedResetsActive）；
		// canceled/hint 一并清空，与 beginPlay 对称（Track 非 nil 才重置：兜底激活
		// 本身要求 Track 非 nil，此处与分支内其余 Track 访问同一守卫）。
		if m.state.Track != nil {
			m.fallback = fallbackState{trackID: m.state.Track.ID, gen: m.playGen}
		}
		// 事件链存活标记：TrackStarted 到达 = UI 事件消费链健康（连播后首个
		// 关键事件；若日志缺失说明事件链断裂，进度/歌词将冻结，见 TrackEnded 分支注释）
		if m.state.Track != nil {
			logger.Info("新曲就绪: %s - %s (id=%s)", m.state.Track.Title, m.state.Track.Artist, m.state.Track.ID)
		}
		// 缓存预热：mpv 取流成功后才启动后台下载——避免与 mpv 内置 yt-dlp
		// 并发访问同一 URL 放大 403 风控（回归：连播未缓存下一首卡住）。
		// CacheAsync 对 Disabled/已存在/在途条目均为 no-op，playingFromCache 时安全。
		if m.cache != nil && m.state.Track != nil {
			m.cache.CacheAsync(*m.state.Track)
		}
		// 预加载：当前曲确认开始后预下载队列下一首（门控见 refreshPreload；
		// 回绕同 ID 时 refreshPreload 不设目标：同曲缓存已由上方预热覆盖，预载
		// 纯属重复；且此时预载会与 mpv 取流并发访问同一 URL，放大 403 风控）。
		m.refreshPreload()
		// 仅在拿到真实时长时覆盖：Duration=0 表示 observe 与 Get 兜底
		// 均失败（直播/特殊流），此时保留搜索元数据提供的时长，避免被抹零。
		if ev.Duration > 0 {
			m.state.Duration = ev.Duration
		}
		m.home = m.home.syncState(m.state)
	case player.TrackEndedEvent:
		m.retryCount = 0 // 下一首开始，重试预算重置
		// 本曲加载/播放已有结论：加载中提示结束（连播下一首时由 beginPlay 重新设置）
		m.loadingSince = time.Time{}
		// 自动连播。解耦标记（删除当前曲）存在时：播放顺延曲目（当前位），
		// 无当前位则从头，队列为空则停止；否则正常推进到下一首。
		// 两种情况均不切换当前页面。
		// 注意：所有分支都必须重新发出 waitForPlayerEvents（cmds）——本分支
		// 提前 return，若丢弃链则连播后无人再读 p.Events()，缓冲满后事件被
		// emit 丢弃：UI 进度冻结 0.00/歌词不动，而 MPRIS 独立订阅通道仍正常
		// （回归：TestTrackEndedAutoAdvanceKeepsEventChainAlive/Stop）。
		if m.queueSkip {
			m.queueSkip = false
			if tr, ok := m.queue.Current(); ok {
				logger.Info("曲目结束(删除解耦): 连播顺延曲目 %s - %s", tr.Title, tr.Artist)
				m2, cmd := m.beginPlay(tr)
				return m2, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
			}
			if tr, ok := m.queue.Next(); ok {
				logger.Info("曲目结束(删除解耦): 连播下一首 %s - %s", tr.Title, tr.Artist)
				m2, cmd := m.beginPlay(tr)
				return m2, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
			}
			logger.Info("曲目结束: 队列为空，停止播放")
			m2, cmd := m.stopAfterEnd()
			return m2, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
		}
		if tr, ok := m.queue.Next(); ok {
			logger.Info("曲目结束: 连播下一首 %s - %s", tr.Title, tr.Artist)
			m2, cmd := m.beginPlay(tr)
			return m2, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
		}
		logger.Info("曲目结束: 无下一首，停止播放")
		m2, cmd := m.stopAfterEnd()
		return m2, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
	case player.ErrorEvent:
		// 加载/播放有结论（失败、断开、超时）：加载中提示结束。重试/跳过路径
		// 会经 beginPlay 重新设置（下一轮加载重新计时）。
		m.loadingSince = time.Time{}
		// 取流失败（LoadFailedError）与加载超时（LoadTimeoutError，看门狗
		// 主动报错，取流悬挂）多为瞬态错误（如 YouTube 403 风控）：
		// 预算内自动重试；耗尽后队列有下一首则跳过继续连播，否则停止。
		// 其他错误（连接断开/重连失败）保持原有行为，不自动重试。
		var le *player.LoadFailedError
		var lte *player.LoadTimeoutError
		isLoadTimeout := errors.As(ev.Err, &lte)
		if errors.As(ev.Err, &le) || isLoadTimeout {
			// 播放自缓存文件时取流失败 → 缓存条目损坏（下载不完整/已过期）：
			// 移除条目 + 复位标记，后续重试/跳过自然回退网络 URL 重新取流。
			// fromCache 须在移除动作之前捕获（移除后 playingFromCache 已复位）。
			// 加载超时（LoadTimeoutError）不触发移除：超时可能发生在本地文件
			//（mpv 卡死/负载高），删健康缓存有害（回归：误删健康缓存）。
			fromCache := m.playingFromCache && m.state.Track != nil && !isLoadTimeout
			if fromCache {
				m.cache.Remove(m.state.Track.ID)
				m.playingFromCache = false
			}
			// 提示区分三类失败：缓存损坏（从缓存播放失败，已删条目下次重下）、
			// 网络取流失败（file_error 诊断映射）、加载超时（看门狗主动报错，
			// 取流悬挂）。恢复中/重试中/耗尽跳过共用。
			var hint string
			switch {
			case isLoadTimeout:
				hint = "取流超时（网络卡顿/风控/取流悬挂）"
			case fromCache:
				hint = "缓存文件损坏，已移除（下次播放将重新下载）"
			default:
				hint = m.failureHint(le)
			}
			logger.Warn("播放失败(%s): %v", hint, ev.Err)
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
			// 缓存兜底：URL 取流失败/超时且未播本地文件 → 缓存已命中立即切本地；
			// 未命中则启动/接入下载并等待完成（限时 fallbackWaitTimeout），完成后若仍未
			// 开始播放自动切本地；下载不可用（禁用/无 yt-dlp）或等待放弃 → 走下方
			// retryOrSkipLoadFailure 现有链路（原错误提示保留）。fromCache 失败（缓存
			// 文件损坏，条目已移除）不兜底——重下同一 URL 大概率再失败，且与 mpv 并发
			// 访问同一 URL 放大 403 风控（回归：TestCacheFallbackOnlyOnce）。
			if !fromCache && !m.playingFromCache && m.state.Track != nil && !m.fallback.active {
				tr := m.state.Track
				if path, ok := m.cache.Lookup(tr.ID); ok {
					logger.Warn("播放失败，改用缓存文件: %s - %s (id=%s) 文件=%s", tr.Title, tr.Artist, tr.ID, path)
					m2, cmd := m.beginPlay(*tr) // beginPlay 内部重新 Lookup 自动命中缓存
					// toast 提示（在 beginPlay 之后调用 showToast，注意 beginPlay 清 toast）
					m2, tcmd := m2.showToast("播放失败，已改用缓存文件播放", toastSuccess)
					return m2, tea.Batch(append([]tea.Cmd{cmd, tcmd}, cmds...)...)
				}
				done := m.cache.CacheAsync(*tr)
				// LoadTimeout 时 mpv 仍在取流（看门狗只报错不杀加载）：兜底下载与
				// mpv 内置 yt-dlp 并发访问同一 URL 可能放大 403 风控——功能固有取舍
				//（不下载则无法兜底），接受该风险；LoadFailed 场景 mpv 已终结无此问题。
				if done == nil {
					done = m.cache.WaitDone(tr.ID) // 已在途：接既有信号
				}
				if done != nil {
					m.fallback = fallbackState{active: true, trackID: tr.ID, gen: m.playGen, hint: hint, isLoadTimeout: isLoadTimeout}
					m, cmd := m.showToast("缓存下载中，完成后将自动播放…", toastWarning)
					return m, tea.Batch(cmd, waitCacheDone(m.cache, m.fallback.trackID, m.fallback.gen, done), fallbackTimeoutCmd(m.fallback.gen), waitForPlayerEvents(m.player))
				}
				// done == nil：缓存禁用/无 yt-dlp/既不在途 → 兜底不可用，走现有链路
			} else if m.fallback.active && m.state.Track != nil && m.fallback.trackID == m.state.Track.ID {
				// 兜底等待中再次收到播放失败：忽略（不重复启动下载/不消耗重试预算），
				// 等待状态保持，下载完成/超时统一收口（回归：TestCacheFallbackActiveIgnoresSecondError）。
				logger.Debug("缓存兜底等待中再次收到播放失败，忽略: %v", ev.Err)
				return m, tea.Batch(waitForPlayerEvents(m.player))
			}
			return m.retryOrSkipLoadFailure(hint, isLoadTimeout, cmds)
		}
		m.retryCount = 0
		logger.Warn("播放器错误: %v", ev.Err)
		m.ended = true
		m.refreshPreload() // ended 门控：清空预加载目标
		m, cmd := m.showToast(ev.Err.Error(), toastError)
		m.state.Playing = false
		m.home = m.home.syncState(m.state)
		return m, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
	}
	return m, tea.Batch(cmds...)
}

// retryOrSkipLoadFailure 播放失败（LoadFailed/LoadTimeout）后的重试/跳过/停止链路：
// 预算内自动重试（URL），耗尽后跳过失败曲目继续连播或停止。hint 为失败提示。
// 供 ErrorEvent 与缓存兜底放弃（下载失败/超时）共用。
func (m Model) retryOrSkipLoadFailure(hint string, isLoadTimeout bool, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	if m.retryCount < maxPlayRetries {
		m.retryCount++
		if t := m.state.Track; t != nil {
			logger.Warn("播放失败，自动重试 %d/%d: %s - %s (id=%s)", m.retryCount, maxPlayRetries, t.Title, t.Artist, t.ID)
		} else {
			logger.Warn("播放失败，自动重试 %d/%d", m.retryCount, maxPlayRetries)
		}
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
		logger.Warn("重试耗尽，跳过失败曲目继续连播: %s - %s (id=%s)", skip.Title, skip.Artist, skip.ID)
		m2, cmd := m.skipFailedTrack(*skip, hint)
		// 跳过 = 播放状态变更（新当前曲）：立即重算预加载目标，不必等下次
		// TrackStarted——否则目标仍指向刚跳到的当前曲，预载会与 mpv 取流并发
		// 访问同一 URL 放大 403 风控。回绕后目标可能撞回刚失败曲目
		//（failedTracks 成员）：对其发起有界预载重试（MaxDownloadAttempts 次 ×
		// DownloadRetryBackoff 退避 ≈ 数秒）是设计内接受的浪费——失败静默、
		// 不与 mpv 并发同 URL（失败曲目已停止取流），与“失败静默”策略一致。
		m2.refreshPreload()
		return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))
	}
	// 单曲重试耗尽：停止播放，等待用户操作（空格重播同曲）
	logger.Error("播放失败，重试 %d 次耗尽，停止播放: %v", maxPlayRetries, hint)
	m.ended = true
	m.refreshPreload() // ended 门控：清空预加载目标
	m, cmd := m.showToast(fmt.Sprintf("播放失败：%s，已重试 %d 次。请稍后重试或更换歌曲", hint, maxPlayRetries), toastError)
	m.state.Playing = false
	m.home = m.home.syncState(m.state)
	return m, tea.Batch(append([]tea.Cmd{cmd}, cmds...)...)
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

// waitCacheDone 监听缓存下载完成信号：done 关闭 → 发 cacheFallbackDoneMsg（捕获 gen）。
func waitCacheDone(cm *cache.Manager, trackID string, gen int, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-done
		return cacheFallbackDoneMsg{trackID: trackID, gen: gen}
	}
}

// fallbackTimeoutCmd 兜底等待限时：fallbackWaitTimeout 后发 cacheFallbackTimeoutMsg。
func fallbackTimeoutCmd(gen int) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(fallbackWaitTimeout)
		return cacheFallbackTimeoutMsg{gen: gen}
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

// failureHint 取流失败（LoadFailedError）提示：在 loadFailureHint 诊断基础上，
// 未配置 yt-dlp cookie/headers 且失败与风控相关（提示含“风控/拒绝访问”或
// file_error 含 403）时，附加 YT Music 登录（cookie）配置引导，给用户可操作方向。
// 已配置或非风控失败不加引导（避免噪音）。
func (m Model) failureHint(le *player.LoadFailedError) string {
	h := loadFailureHint(le.FileError)
	if !m.ytdlpConfigured &&
		(strings.Contains(h, "风控") || strings.Contains(h, "拒绝访问") || strings.Contains(le.FileError, "403")) {
		h += "；可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效"
	}
	return h
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
	m.refreshPreload() // 播放停止：清空预加载目标（ended 下无下一首可预载）
	m.loadingSince = time.Time{} // 已停止：无加载中提示
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
	m2, cmd := m.playQueueTrack()
	m2.refreshPreload() // 替换语义 = 队列形态变更：重新计算预加载目标
	return m2, cmd
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
// 据此移除损坏条目）；未命中 → 播网络 URL（缓存预热统一在 TrackStarted
// 触发：mpv 取流成功后才启动后台下载，避免与 mpv 内置 yt-dlp 并发访问
// 同一 URL 放大 403 风控——回归：连播未缓存下一首卡住）。
// 注意：不重置 retryCount——自动重试也走本路径，重置会让重试预算
// 永不耗尽（回归：TestLoadFailRetriesExhaustedSkipsInQueue/StopsSingle）；
// 预算在 TrackStarted/TrackEnded/手动播放入口重置。
func (m Model) beginPlay(track model.Track) (Model, tea.Cmd) {
	m.resuming = false // 任何 beginPlay = 用户新意图或重试：恢复上下文作废
	// （重试路径不会在 resuming=true 时发生——恢复中取流失败走恢复失败分支，不调度重试）
	m.playGen++ // 播放代际递增：使在途重试消息过期（用户换曲后不再重试旧曲）
	// 新加载开始：mpv 尚未开始播放（TrackStartedEvent 到达统一置 true）。
	// 放在此处（而非 URL 分支）保证 Play 成功/失败两个路径都重置。
	m.playStarted = false
	// fallback 重置：新播放代际（切本地/切歌/重播）下旧兜底状态（active/canceled）
	// 作废；在途兜底消息由 gen 校验丢弃（cacheFallbackDoneMsg 分支）。
	m.fallback = fallbackState{trackID: track.ID, gen: m.playGen}
	m.ended = false
	m.state = model.PlaybackState{Track: &track, Playing: true, Duration: track.Duration}
	m.toast = nil // 新曲目开始 = 新状态：清除活跃 toast
	m.home = m.home.resetForTrack(&track)
	m.queuePage = m.queuePage.setAITrack("", "") // 切歌：AI 展示覆盖作废
	target := track.URL
	if path, ok := m.cache.Lookup(track.ID); ok {
		target = path
		m.playingFromCache = true
		logger.Info("播放(缓存命中): %s - %s (id=%s) 文件=%s", track.Title, track.Artist, track.ID, path)
	} else {
		m.playingFromCache = false
		logger.Info("播放: %s - %s (id=%s) url=%s", track.Title, track.Artist, track.ID, track.URL)
		// 缓存预热不在 beginPlay 触发（见上方注释）：TrackStarted 统一启动
	}
	// 缓存兜底监听（仅 URL 路径）：本曲已有在途下载（preload 预载/预热）时注册监听——
	// 下载完成若 mpv 仍未开始播放（TrackStarted 未到），自动改用本地缓存文件（用户需求：
	// “下载完成后如果 mpv 还是没有播放就改用下载的文件播放”）。只监听不启动：
	// 启动下载会与 mpv 内置 yt-dlp 并发访问同一 URL 放大 403 风控（6440188 教训），
	// 下载启动统一由 TrackStarted 预热/兜底分支负责。
	var cacheDoneCmd tea.Cmd
	if done := m.cache.WaitDone(track.ID); done != nil {
		cacheDoneCmd = waitCacheDone(m.cache, track.ID, m.playGen, done)
	}
	if err := m.player.Play(target); err != nil {
		logger.Error("播放命令失败: %s - %s (id=%s): %v", track.Title, track.Artist, track.ID, err)
		m.loadingSince = time.Time{} // 加载未开始即失败：无加载中提示
		m, cmd := m.showToast("播放失败: "+err.Error(), toastError)
		m.state = model.PlaybackState{}
		m.home = m.home.syncState(m.state)
		m.queuePage = m.queuePage.sync(m.queue)
		m.notifyTrack(nil)
		return m, cmd
	}
	m.loadingSince = time.Now() // 加载起始：TrackStarted/TrackEnded/Error 清除
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
		cacheDoneCmd,
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
			logger.Warn("清除会话失败: %v", err)
		}
		return
	}
	st := session.State{
		Queue:    m.queue.Snapshot(),
		Position: m.state.Position,
		Ended:    m.ended,
	}
	if err := m.session.Save(st); err != nil {
		logger.Warn("保存会话失败: %v", err)
	}
}

// resumeCmd 续播恢复：PlayPaused 静默加载当前曲目并定位（不发声；定位随
// loadfile 的 start= 选项原子完成，避免加载窗口内 seek 被 mpv 拒绝的竞态，
// 见 mpv.go PlayPaused）。命中缓存 → 播本地文件（fromCache 标记回填至
// playingFromCache，异步 LoadFailedError 时据此移除损坏条目）；未命中 →
// 播网络 URL（缓存预热在 TrackStartedEvent 后触发，见下）。
// IPC 层失败（PlayPaused 命令被拒）与缓存文件无关，不删条目。
// 缓存预热统一在 TrackStartedEvent 后触发（与 beginPlay 一致）：PlayPaused
// 的 IPC 成功只代表命令被接受，mpv 取流成功（file-loaded）后才启动后台
// 下载——避免与 mpv 内置 yt-dlp 并发访问同一 URL 放大 403 风控。
func resumeCmd(m Model) tea.Cmd {
	track := m.resume.track
	pos := m.resume.pos
	return func() tea.Msg {
		target := track.URL
		fromCache := false
		if path, ok := m.cache.Lookup(track.ID); ok {
			target = path
			fromCache = true
		}
		logger.Info("续播恢复: %s - %s (id=%s) pos=%.1f fromCache=%v", track.Title, track.Artist, track.ID, pos, fromCache)
		if err := m.player.PlayPaused(target, pos); err != nil {
			logger.Error("续播恢复失败: %v", err)
			return resumeResultMsg{err: err, fromCache: fromCache}
		}
		return resumeResultMsg{fromCache: fromCache}
	}
}

func fetchLyricsCmd(c lyrics.Fetcher, track model.Track) tea.Cmd {
	return func() tea.Msg {
		// 40s 总预算：AI 识别（子预算 25s，大模型首 token 慢——实测
		// qwen3.7-plus 11.4s，高峰期 20s+）+ 严格重查 + 确定性兜底。
		// 曾用 10s：AI 请求在等待期被掐断，识别永不成功（回归：
		// ai.jsonl 无记录）。
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		res, err := c.Fetch(ctx, track)
		return lyricsResultMsg{trackID: track.ID, lyrics: res.Lyrics, title: res.Title, artist: res.Artist, err: err}
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

// handleMprisReq 消费一条 MPRIS 控制器请求：与对应 UI 键位同一编排路径。
// 所有分支都必须回写 reply（D-Bus goroutine 同步等待）并重新订阅请求流
// （cmd 链不丢，同 TrackEnded 分支约束）。
func (m Model) handleMprisReq(req mprisReq) (Model, tea.Cmd) {
	// 防御：无控制器桥（测试手搭 Model 未走 NewModel）时回包并忽略请求
	if m.mprisCtrl == nil {
		req.reply <- nil
		return m, nil
	}
	switch req.kind {
	case reqNext:
		if tr, ok := m.queue.Next(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload() // 切歌后重算预加载目标（preloader 为指针，副本共享）
			req.reply <- nil
			return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
		}
		req.reply <- queue.ErrEmpty
		return m, subscribeMprisReqs(m.mprisCtrl.reqs)
	case reqPrev:
		if tr, ok := m.queue.Prev(); ok {
			m.retryCount = 0
			m.queueSkip = false
			m.current = pageHome
			m2, cmd := m.beginPlay(tr)
			m2.refreshPreload()
			req.reply <- nil
			return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
		}
		req.reply <- queue.ErrEmpty
		return m, subscribeMprisReqs(m.mprisCtrl.reqs)
	case reqSetMode:
		// 必须先回包再切模式：D-Bus 侧 prop.Set 持锁等待 reply，applyMode 内的
		// notifyModeChanged→SyncMode 要抢同一把 prop 锁——先回包让 Set 返回
		// 释放锁，避免循环等待死锁（reviewer 实证复现，回归测试见 mpris 包）。
		req.reply <- nil
		m2, cmd := m.applyMode(req.mode)
		return m2, tea.Batch(cmd, subscribeMprisReqs(m.mprisCtrl.reqs))
	}
	req.reply <- nil
	return m, subscribeMprisReqs(m.mprisCtrl.reqs)
}

// applyMode 绝对模式切换（MPRIS 写 LoopStatus/Shuffle 与 UI 三态循环共用）：
// SetMode + 同步 mpv 单曲循环 + 重算预加载 + 重建队列视图 + 通知 MPRIS 同步。
// 同模式切换为 no-op：不通知（避免 MPRIS 无谓 PropertiesChanged，与
// notifyModeChanged 注释约定一致）；SetLoop 失败仅 toast 不阻断（模式已切换，
// 与 s 键原行为一致）。
func (m Model) applyMode(mode queue.Mode) (Model, tea.Cmd) {
	// 同模式 no-op：不 SetLoop、不通知（与 queue.SetMode 同模式返回一致）
	if m.queue.Mode() == mode {
		return m, nil
	}
	prev := m.queue.Mode()
	m.queue.SetMode(mode)
	// 模式影响预加载门控（RepeatOne 跳过预载）：切换后立即重算目标
	m.refreshPreload()
	if m.queue.Mode() != prev {
		m.notifyModeChanged()
	}
	if err := m.player.SetLoop(mode == queue.RepeatOne); err != nil {
		m, cmd := m.showToast("设置循环失败: "+err.Error(), toastError)
		return m.syncQueueViews(), cmd
	}
	return m.syncQueueViews(), nil
}

// cycleMode 三态循环切换播放模式：Sequential→Shuffle→RepeatOne→Sequential。
// 首页模式按钮（toggleModeMsg）与队列页 s 键（queueModeMsg）共用。
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
	return m.applyMode(next)
}

// notifyModeChanged 通知外部消费者（MPRIS）播放模式已变更；nil 安全。
// 调用方保证仅在模式实际变化后调用（applyMode 同模式切换不通知）。
func (m Model) notifyModeChanged() {
	if m.mprisCtrl != nil && m.mprisCtrl.onModeChanged != nil {
		m.mprisCtrl.onModeChanged(m.queue.Mode())
	}
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
		// 缓存兜底等待期间用户暂停 = 取消兜底（下载完成不再切本地，避免打扰用户）：
		// 转停止态（ended），保留原错误提示，空格可重播（restartSameTrack）。
		if m.fallback.active {
			logger.Warn("用户暂停，取消缓存兜底等待")
			m.fallback.active = false
			m.fallback.canceled = true
			m.ended = true
			m.refreshPreload() // ended 门控：清空预加载目标（与其他停止路径一致，审查 P2）
			m.loadingSince = time.Time{}
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			m, cmd := m.showToast("已取消缓存兜底："+m.fallback.hint, toastError)
			return m, tea.Batch(cmd)
		}
		return m, func() tea.Msg {
			return playerOpResultMsg{err: m.player.Pause()}
		}
	}
	logger.Debug("用户继续播放")
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
	logger.Debug("seek 到 %.1fs", target)
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

func emitTrackInsertNext(track model.Track) tea.Cmd {
	return func() tea.Msg { return trackInsertNextMsg{track: track} }
}

func emitQueuePlay(index int) tea.Cmd {
	return func() tea.Msg { return queuePlayMsg{index: index} }
}

func emitQueueDelete(index int) tea.Cmd {
	return func() tea.Msg { return queueDeleteMsg{index: index} }
}

func emitQueueMove(from, to int) tea.Cmd {
	return func() tea.Msg { return queueMoveMsg{from: from, to: to} }
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

// typingText 返回是否有输入框处于聚焦（搜索关键词/播放列表命名/队列历史
// 页 / 过滤）：聚焦时字符类全局键（空格/a/q）让位给输入框。
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

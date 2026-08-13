// Package ui 实现 bubbletea 三页面（首页/搜索/历史）与全局事件路由。
package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
	"music-tui/search"
	"music-tui/session"
)

// page 页面枚举。
type page int

const (
	pageHome page = iota
	pageSearch
	pageHistory
	pageQueue
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

// resumeResultMsg 续播恢复结果（PlayPaused + Seek 成败）。
type resumeResultMsg struct {
	err error
}

// resumeInfo 待恢复的会话信息（NewModel 从 session 填充，Init 消费）。
type resumeInfo struct {
	track model.Track
	pos   float64
}

// saveInterval 播放中自动保存会话的节流间隔。
const saveInterval = 5 * time.Second

// 播放失败自动重试：取流失败（如 YouTube 403 风控）多为瞬态错误，
// 重试 = 重新 loadfile = 重新取流拿新签名 URL，大概率恢复。
const maxPlayRetries = 2           // 每首曲目最多自动重试次数
var retryBackoff = 2 * time.Second // 重试间隔（包级变量：测试可调小以缩短等待）

// Model 顶层模型：持有共享播放状态、播放队列与四个页面，负责全局按键、
// 页面切换、服务调用与结果路由。
type Model struct {
	player  player.Player
	lyrics  *lyrics.Client
	cover   *cover.Fetcher
	history *history.Store
	queue   *queue.Queue
	session *session.Store

	state     model.PlaybackState
	current   page
	lastError string
	ended     bool // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）
	// queueSkip 标记删除当前曲导致的指针解耦：mpv 仍播放被删曲目，
	// 但队列指针已顺延。下次 TrackEnded 应播放顺延曲目（不推进），
	// 避免跳过顺延曲目（回归：TestDeleteCurrentThenTrackEndedPlaysSlidTrack）。
	queueSkip bool

	// 播放失败自动重试状态
	retryCount int // 当前曲目已自动重试次数（新曲加载成功/结束/手动播放时重置）
	playGen    int // 播放代际计数器：每次 beginPlay 自增；过期重试消息（用户已换曲）丢弃

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

	onTrack func(*model.Track) // 外部消费者（MPRIS）感知当前曲目；nil 安全
}

// NewModel 组装 UI。p/s 为接口（可注入 fake 测试），l/c/h/sess 为具体服务，
// onTrack 在播放状态变化时同步回调当前曲目（nil 表示无曲目；可为 nil）。
// 若 sess 存在已保存会话（队列 + 进度），同步恢复队列与播放状态（暂停态），
// mpv 的静默加载由 Init 返回的 resumeCmd 完成。
func NewModel(p player.Player, s search.SearchAdapter, l *lyrics.Client, c *cover.Fetcher, h *history.Store, sess *session.Store, onTrack func(*model.Track)) Model {
	m := Model{
		player:      p,
		lyrics:      l,
		cover:       c,
		history:     h,
		queue:       queue.New(),
		session:     sess,
		onTrack:     onTrack,
		current:     pageHome,
		home:        newHomeModel(p),
		searchPage:  newSearchModel(s),
		historyPage: newHistoryModel(),
		queuePage:   newQueueModel(),
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
	return m
}

// Init 启动两个常驻 cmd：播放器事件监听 + spinner 全局 tick；
// 存在已保存会话时追加续播恢复 cmd（PlayPaused 静默加载 + Seek 定位）。
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
		mode := queue.Sequential
		if m.queue.Mode() == queue.Sequential {
			mode = queue.Shuffle
		}
		m.queue.SetMode(mode)
		return m.syncQueueViews(), nil

	case playResultMsg:
		// 预留分支：若未来把 player.Play 改回异步 cmd，此分支处理其失败结果。
		// 当前 startPlay 同步调用 Play，本分支不会触发。
		if msg.err != nil {
			m.lastError = "播放失败: " + msg.err.Error()
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
		}
		return m, nil

	case playerOpResultMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
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
			//（避免“正在自动重试”横幅悬挂）。
			m.ended = true
			m.lastError = "播放失败：队列已清空，已停止自动重试"
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, waitForPlayerEvents(m.player)
		}
		m2, cmd := m.playQueueTrack()
		return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))

	case resumeResultMsg:
		if msg.err != nil {
			// 恢复失败：清空内存中的恢复队列（当前曲播放不了）；磁盘会话
			// 保留——下次启动重试（mpv 瞬时故障可恢复），用户播放新曲或
			// 退出时自然覆盖/清除。
			m.resuming = false // 恢复上下文作废
			m.lastError = "恢复播放失败: " + msg.err.Error()
			m.state = model.PlaybackState{}
			m.home = m.home.syncState(m.state)
			m.queue = queue.New()
			m.queueSkip = false
			m.notifyTrack(nil)
			return m.syncQueueViews(), nil
		}
		// 恢复成功：暂停态也加载歌词/封面展示。
		// 注意：不在成功分支清除 resuming——PlayPaused/Seek 的 IPC 成功只代表
		// 命令被接受，mpv 异步取流失败（end-file error → LoadFailedError）随后
		// 才到；resuming 须保持到 TrackStartedEvent（加载真成功）或
		// LoadFailedError（真失败）或 beginPlay（用户新意图）。
		if m.state.Track == nil {
			return m, nil
		}
		return m, tea.Batch(
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
		m.lastError = ""
		if err := m.history.Remove(msg.id, msg.source); err != nil {
			m.lastError = "删除历史失败: " + err.Error()
		}
		return m.refreshHistory(), nil

	case clearHistoryMsg:
		m.lastError = ""
		if err := m.history.Clear(); err != nil {
			m.lastError = "清空历史失败: " + err.Error()
		}
		return m.refreshHistory(), nil

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
		// 顶部 Tab 栏占 1 行，页面高度相应减 1
		m.home = m.home.setSize(msg.Width, msg.Height-1)
		m.searchPage = m.searchPage.setSize(msg.Width, msg.Height-1)
		m.historyPage = m.historyPage.setSize(msg.Width, msg.Height-1)
		m.queuePage = m.queuePage.setSize(msg.Width, msg.Height-1)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			return m.switchPage("tab"), nil
		case "1", "2", "3", "4":
			// 数字键始终切页。注：计划原代码在搜索输入框聚焦时把数字让给输入框，
			// 但 TestTabSwitchesPages 要求搜索页聚焦时按 3/1 仍能切页，故取消例外。
			return m.switchPage(msg.String()), nil
		case " ":
			// 搜索输入框聚焦时空格是输入字符。
			// bubbletea 把空格解析为 KeySpace 类型（真实终端解析会带 Runes，
			// 但测试构造的 KeySpace 无 Runes）；textinput 按 msg.Runes 插字符，
			// 统一转成 KeyRunes(' ') 保证插入。
			if m.current == pageSearch && m.searchPage.typing() {
				return m.delegate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
			}
			return m.togglePlay()
		case "q", "ctrl+c":
			// 注意：搜索输入框聚焦时 ctrl+c 会被 textinput 吞掉
			// （bubbles v1.0.0 textinput 无 ctrl+c 绑定，按键被消费且不转发），
			// 此时无法用 ctrl+c 退出，只能按 q。
			if m.current == pageSearch && m.searchPage.typing() {
				return m.delegate(msg)
			}
			m.saveSession() // 退出前持久化会话（队列 + 进度）
			return m, tea.Quit
		}
		return m.delegate(msg)
	}

	return m.delegate(msg)
}

// View 渲染当前页面，底部附全局错误横幅。
func (m Model) View() string {
	var body string
	switch m.current {
	case pageHome:
		body = m.home.view()
	case pageSearch:
		body = m.searchPage.view()
	case pageHistory:
		body = m.historyPage.view()
	case pageQueue:
		body = m.queuePage.view()
	}
	if m.lastError != "" {
		body += "\n\n" + lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Render("⚠ "+m.lastError)
	}
	return m.tabBar() + "\n" + body
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
		m.retryCount = 0   // 新曲加载成功，重试预算重置
		m.resuming = false // 恢复加载成功：进入正常播放态，恢复上下文作废
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
			// 续播恢复（PlayPaused 静默加载）期间撞取流失败：不自动重试——
			// 恢复上下文已作废（重试会走 beginPlay→Play()：发声、从 0:00、
			// 非暂停，静默丢弃恢复语义），保留“恢复播放失败”语义：清空内存
			// 队列 + 横幅带 hint 诊断；磁盘会话保留，下次启动重试。
			if m.resuming {
				m.resuming = false
				m.lastError = "恢复播放失败: " + loadFailureHint(le.FileError)
				m.state = model.PlaybackState{}
				m.home = m.home.syncState(m.state)
				m.queue = queue.New()
				m.queueSkip = false
				m.notifyTrack(nil)
				return m.syncQueueViews(), nil
			}
			if m.retryCount < maxPlayRetries {
				m.retryCount++
				hint := loadFailureHint(le.FileError)
				m.lastError = fmt.Sprintf("播放失败：%s，正在自动重试（%d/%d）…", hint, m.retryCount, maxPlayRetries)
				m.state.Playing = false
				m.home = m.home.syncState(m.state)
				return m, tea.Batch(waitForPlayerEvents(m.player), retryPlayCmd(m.playGen))
			}
			hint := loadFailureHint(le.FileError)
			if m.queueSkip {
				// 重试耗尽且存在删除解耦标记：镜像 TrackEnded 的兜底逻辑——
				// 指针已顺延，播放顺延曲目（当前位）而非 Next()，避免跳过头
				// （回归：TestLoadFailExhaustedSkipRespectsQueueSkip）。
				m.queueSkip = false
				if tr, ok := m.queue.Current(); ok {
					m2, cmd := m.skipFailedTrack(tr, hint)
					return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))
				}
				if tr, ok := m.queue.Next(); ok {
					m2, cmd := m.skipFailedTrack(tr, hint)
					return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))
				}
			} else if tr, ok := m.queue.Next(); ok {
				// 重试耗尽：跳过失败曲目继续连播（横幅保留告知用户哪首失败）。
				m2, cmd := m.skipFailedTrack(tr, hint)
				return m2, tea.Batch(cmd, waitForPlayerEvents(m.player))
			}
			// 单曲重试耗尽：停止播放，等待用户操作（空格重播同曲）
			m.ended = true
			m.lastError = fmt.Sprintf("播放失败：%s，已重试 %d 次。请稍后重试或更换歌曲", hint, maxPlayRetries)
			m.state.Playing = false
			m.home = m.home.syncState(m.state)
			return m, tea.Batch(cmds...)
		}
		m.retryCount = 0
		m.ended = true
		m.lastError = ev.Err.Error()
		m.state.Playing = false
		m.home = m.home.syncState(m.state)
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
	m2, cmd := m.beginPlay(tr)
	failedTitle := "当前歌曲"
	if failed != nil {
		failedTitle = failed.Title
	}
	m2.lastError = fmt.Sprintf("「%s」播放失败：%s，已重试 %d 次，跳过继续播放", failedTitle, hint, maxPlayRetries)
	return m2, cmd
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
// 状态重置为空回到"未在播放"空态 + 错误横幅。
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
// Play 失败时跳过全部异步 cmd，状态重置为空回到"未在播放"空态 + 错误横幅。
// 注意：不重置 retryCount——自动重试也走本路径，重置会让重试预算
// 永不耗尽（回归：TestLoadFailRetriesExhaustedSkipsInQueue/StopsSingle）；
// 预算在 TrackStarted/TrackEnded/手动播放入口重置。
func (m Model) beginPlay(track model.Track) (Model, tea.Cmd) {
	m.resuming = false // 任何 beginPlay = 用户新意图或重试：恢复上下文作废
	// （重试路径不会在 resuming=true 时发生——恢复中取流失败走恢复失败分支，不调度重试）
	m.playGen++ // 播放代际递增：使在途重试消息过期（用户换曲后不再重试旧曲）
	m.ended = false
	m.state = model.PlaybackState{Track: &track, Playing: true, Duration: track.Duration}
	m.lastError = ""
	m.home = m.home.resetForTrack(&track)
	if err := m.player.Play(track.URL); err != nil {
		m.lastError = "播放失败: " + err.Error()
		m.state = model.PlaybackState{}
		m.home = m.home.syncState(m.state)
		m.queuePage = m.queuePage.sync(m.queue)
		m.notifyTrack(nil)
		return m, nil
	}
	m.notifyTrack(&track)
	return m.syncQueueViews(), tea.Batch(
		fetchLyricsCmd(m.lyrics, track),
		fetchCoverCmd(m.cover, track),
		addHistoryCmd(m.history, track),
	)
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

// resumeCmd 续播恢复：PlayPaused 静默加载当前曲目（不发声）→ Seek 定位。
func resumeCmd(m Model) tea.Cmd {
	track := m.resume.track
	pos := m.resume.pos
	return func() tea.Msg {
		if err := m.player.PlayPaused(track.URL); err != nil {
			return resumeResultMsg{err: err}
		}
		if err := m.player.Seek(pos); err != nil {
			return resumeResultMsg{err: err}
		}
		return resumeResultMsg{}
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

// switchPage 处理 Tab（循环）与 1/2/3/4（直达）。
func (m Model) switchPage(key string) Model {
	switch key {
	case "1":
		m.current = pageHome
	case "2":
		m.current = pageSearch
	case "3":
		m.current = pageHistory
	case "4":
		m.current = pageQueue
	default: // tab
		m.current = page((int(m.current) + 1) % 4)
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
	case pageSearch:
		var cmd tea.Cmd
		m.searchPage, cmd = m.searchPage.Update(msg)
		return m, cmd
	case pageHistory:
		var cmd tea.Cmd
		m.historyPage, cmd = m.historyPage.Update(msg)
		return m, cmd
	case pageQueue:
		var cmd tea.Cmd
		m.queuePage, cmd = m.queuePage.Update(msg)
		return m, cmd
	}
	return m, nil
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

// Package ui 实现 bubbletea 三页面（首页/搜索/历史）与全局事件路由。
package ui

import (
	"context"
	"log"
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

// Model 顶层模型：持有共享播放状态、播放队列与四个页面，负责全局按键、
// 页面切换、服务调用与结果路由。
type Model struct {
	player  player.Player
	lyrics  *lyrics.Client
	cover   *cover.Fetcher
	history *history.Store
	queue   *queue.Queue

	state     model.PlaybackState
	current   page
	lastError string
	ended     bool // 当前歌曲是否已播放结束/出错（空格语义：重播同曲而非 Resume）
	// queueSkip 标记删除当前曲导致的指针解耦：mpv 仍播放被删曲目，
	// 但队列指针已顺延。下次 TrackEnded 应播放顺延曲目（不推进），
	// 避免跳过顺延曲目（回归：TestDeleteCurrentThenTrackEndedPlaysSlidTrack）。
	queueSkip bool

	home        homeModel
	searchPage  searchModel
	historyPage historyModel
	queuePage   queueModel
}

// NewModel 组装 UI。p/s 为接口（可注入 fake 测试），l/c/h 为具体服务。
func NewModel(p player.Player, s search.SearchAdapter, l *lyrics.Client, c *cover.Fetcher, h *history.Store) Model {
	m := Model{
		player:      p,
		lyrics:      l,
		cover:       c,
		history:     h,
		queue:       queue.New(),
		current:     pageHome,
		home:        newHomeModel(p),
		searchPage:  newSearchModel(s),
		historyPage: newHistoryModel(),
		queuePage:   newQueueModel(),
	}
	m.historyPage = m.historyPage.setEntries(h.Entries())
	return m
}

// Init 启动两个常驻 cmd：播放器事件监听 + spinner 全局 tick。
// （不用包级 spinner.Tick：bubbles v1.0.0 的包级 Tick 无延时，会形成忙循环。）
func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForPlayerEvents(m.player), spinnerTick)
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
		m.home = m.home.setSize(msg.Width, msg.Height)
		m.searchPage = m.searchPage.setSize(msg.Width, msg.Height)
		m.historyPage = m.historyPage.setSize(msg.Width, msg.Height)
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
	return body
}

// onPlayerEvent 更新共享播放状态并同步首页。
func (m Model) onPlayerEvent(msg playerEventMsg) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{waitForPlayerEvents(m.player)}
	switch ev := msg.ev.(type) {
	case player.ProgressEvent:
		m.state.Position = ev.Position
		m.state.Duration = ev.Duration
		m.home = m.home.syncState(m.state)
	case player.StateEvent:
		m.state.Playing = ev.Playing
		m.home = m.home.syncState(m.state)
	case player.TrackStartedEvent:
		m.ended = false
		// 仅在拿到真实时长时覆盖：Duration=0 表示 observe 与 Get 兜底
		// 均失败（直播/特殊流），此时保留搜索元数据提供的时长，避免被抹零。
		if ev.Duration > 0 {
			m.state.Duration = ev.Duration
		}
		m.home = m.home.syncState(m.state)
	case player.TrackEndedEvent:
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
func (m Model) beginPlay(track model.Track) (Model, tea.Cmd) {
	m.ended = false
	m.state = model.PlaybackState{Track: &track, Playing: true, Duration: track.Duration}
	m.lastError = ""
	m.home = m.home.resetForTrack(&track)
	if err := m.player.Play(track.URL); err != nil {
		m.lastError = "播放失败: " + err.Error()
		m.state = model.PlaybackState{}
		m.home = m.home.syncState(m.state)
		m.queuePage = m.queuePage.sync(m.queue)
		return m, nil
	}
	return m.syncQueueViews(), tea.Batch(
		fetchLyricsCmd(m.lyrics, track),
		fetchCoverCmd(m.cover, track),
		addHistoryCmd(m.history, track),
	)
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

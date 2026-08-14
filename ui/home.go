package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/blacktop/go-termimg"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// 封面区尺寸（字符单元格）。
const (
	coverW = 30
	coverH = 17
)

// ---- 控制消息（root 消费；root 接线在后续任务，本文件只定义类型与 emit） ----

// prevTrackMsg 上一首请求：root 消费（queue.Prev + beginPlay）。
type prevTrackMsg struct{}

// nextTrackMsg 下一首请求：root 消费（queue.Next + beginPlay）。
type nextTrackMsg struct{}

// togglePlayMsg 播放/暂停请求：root 复用现有 togglePlay 处理。
type togglePlayMsg struct{}

// toggleModeMsg 播放模式三态循环请求（Sequential→Shuffle→RepeatOne→Sequential）：
// root 消费（queue.SetMode + player.SetLoop 同步）。
type toggleModeMsg struct{}

// emitPrevTrack 产生上一首消息（首页 , 键 / ⏮ 按钮）。
func emitPrevTrack() tea.Cmd {
	return func() tea.Msg { return prevTrackMsg{} }
}

// emitNextTrack 产生下一首消息（首页 . 键 / ⏭ 按钮）。
func emitNextTrack() tea.Cmd {
	return func() tea.Msg { return nextTrackMsg{} }
}

// emitTogglePlay 产生播放/暂停消息（首页 ⏯ 按钮）。
func emitTogglePlay() tea.Cmd {
	return func() tea.Msg { return togglePlayMsg{} }
}

// emitToggleMode 产生模式切换消息（首页模式按钮）。
func emitToggleMode() tea.Cmd {
	return func() tea.Msg { return toggleModeMsg{} }
}

// ---- 底部按钮行常量 ----

// 按钮行（页面内 Y == height-1）各按钮的 X 区间（点击命中区间；
// 图标渲染位置 = 区间起点）：⏮[0,3) ⏯[4,7) ⏭[8,11) 模式[13,16)。
const (
	btnPrevStart   = 0
	btnToggleStart = 4
	btnNextStart   = 8
	btnModeStart   = 13
	btnHitWidth    = 3 // 每个按钮的点击宽度
)

// hitBtn 判断点击列 x 是否落在起点 start、宽 btnHitWidth 的按钮区间内。
func hitBtn(x, start int) bool {
	return x >= start && x < start+btnHitWidth
}

// lyricsState 歌词展示状态（四种态）。
type lyricsState int

const (
	lyricsLoading lyricsState = iota // 歌词加载中
	lyricsSynced                     // 同步歌词（高亮滚动）
	lyricsPlain                      // 纯文本歌词（整页展示）
	lyricsNone                       // 无歌词
)

// coverRenderer 封面渲染接口（*termimg.ImageWidget 实现；测试可注入
// 渲染失败的 fake，验证首次 Render 失败后不再每帧重试）。
type coverRenderer interface {
	Render() (string, error)
}

// homeModel 首页：封面 + 歌词 + 底部进度条行与按钮行。
// 播放状态由 root 通过 syncState 推入，页面自身不持有服务。
type homeModel struct {
	player player.Player

	width, height int

	state model.PlaybackState

	queuePos   int // 当前曲目在队列中的 1 基位置；0 = 无当前曲目
	queueTotal int // 队列总长；0 = 无队列信息（隐藏展示）
	queueMode  queue.Mode

	spinner   spinner.Model
	lyricView viewport.Model

	lyricsState lyricsState
	lyrics      *lyrics.Lyrics
	currentLine int // 当前高亮行下标；-1 = 无高亮

	coverWidget      coverRenderer
	coverRenderCache string // 封面渲染缓存：setCover 时渲染一次；setSize/resetForTrack 时失效
	coverFailed      bool   // 封面渲染失败标记：置位后 coverView 不再每帧重试（仅 setSize 时重置重试一次）
	coverFallback    bool   // 降级链全部失败 → 占位框
	coverTrackID     string // 当前封面所属歌曲 ID
}

func newHomeModel(p player.Player) homeModel {
	return homeModel{
		player:      p,
		spinner:     spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("63")))),
		lyricView:   viewport.New(0, 0),
		lyricsState: lyricsNone,
		currentLine: -1,
	}
}

// Init 首页无独立 cmd（spinner tick 由 root 统一驱动）。
func (m homeModel) Init() tea.Cmd { return nil }

// Update 处理首页局部按键（←/→ seek、, 上一首、. 下一首）与鼠标
// （进度条点击 seek、按钮行点击）；全局按键（空格等）由 root 处理。
// 鼠标坐标换算：屏幕坐标 - 1 = 页面坐标（Tab 栏占 1 行，root.onMouse
// 在 Y!=0 时已把事件 delegate 到本页）。
func (m homeModel) Update(msg tea.Msg) (homeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "left":
			if m.state.Track == nil {
				return m, nil
			}
			target := m.state.Position - 5
			if target < 0 {
				target = 0
			}
			// 乐观更新本地进度，使连续 seek 基于新位置（player 事件随后会校准）
			m.state.Position = target
			return m, seekCmd(m.player, target)
		case "right":
			if m.state.Track == nil {
				return m, nil
			}
			target := m.state.Position + 5
			if m.state.Duration > 0 && target > m.state.Duration {
				target = m.state.Duration
			}
			m.state.Position = target
			return m, seekCmd(m.player, target)
		case ",":
			if m.state.Track == nil {
				return m, nil
			}
			return m, emitPrevTrack()
		case ".":
			if m.state.Track == nil {
				return m, nil
			}
			return m, emitNextTrack()
		}
	case tea.MouseMsg:
		// 屏幕坐标 → 页面坐标（Tab 栏占 1 行）
		pageY := msg.Y - 1
		pressLeft := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
		switch {
		case pageY == m.height-2 && pressLeft:
			// 进度条行：点击列 ∈ [0, barW) → seek 到对应百分比位置
			if m.state.Track == nil {
				return m, nil
			}
			barW := m.progressBarWidth()
			if msg.X >= 0 && msg.X < barW {
				return m, seekCmd(m.player, progressClickPercent(msg.X, barW)*m.state.Duration)
			}
			return m, nil
		case pageY == m.height-1 && pressLeft:
			// 按钮行：按 X 区间触发对应动作
			if m.state.Track == nil {
				return m, nil
			}
			switch {
			case hitBtn(msg.X, btnPrevStart):
				return m, emitPrevTrack()
			case hitBtn(msg.X, btnToggleStart):
				return m, emitTogglePlay()
			case hitBtn(msg.X, btnNextStart):
				return m, emitNextTrack()
			case hitBtn(msg.X, btnModeStart):
				return m, emitToggleMode()
			}
			return m, nil
		}
	}
	return m, nil
}

// tick 由 root 在每个 spinner.TickMsg 时调用，推进本页 spinner。
func (m homeModel) tick(msg tea.Msg) homeModel {
	m.spinner, _ = m.spinner.Update(msg)
	return m
}

// resetForTrack 切换到新歌曲：清空歌词/封面并置为加载中。
func (m homeModel) resetForTrack(track *model.Track) homeModel {
	m.state = model.PlaybackState{Track: track, Playing: true, Duration: track.Duration}
	m.lyrics = nil
	m.lyricsState = lyricsLoading
	m.currentLine = -1
	m.lyricView.SetContent("")
	m.coverWidget = nil
	m.coverRenderCache = ""
	m.coverFailed = false
	m.coverFallback = false
	m.coverTrackID = track.ID
	return m
}

// syncState 同步播放状态并推进歌词高亮（每次 player 事件后调用）。
func (m homeModel) syncState(state model.PlaybackState) homeModel {
	m.state = state
	if state.Track == nil {
		return m
	}
	// clamp 异常进度值
	if m.state.Position < 0 {
		m.state.Position = 0
	}
	if m.state.Duration < 0 {
		m.state.Duration = 0
	}
	if m.state.Duration > 0 && m.state.Position > m.state.Duration {
		m.state.Position = m.state.Duration
	}
	// 歌词高亮：二分查找当前行，行变化时才重渲染（而非每帧）
	if m.lyricsState == lyricsSynced && m.lyrics != nil {
		idx, _ := m.lyrics.LineAt(m.state.Position)
		if idx != m.currentLine {
			m.currentLine = idx
			m.rebuildLyrics()
			if idx >= 0 {
				m.scrollLyricsTo(idx)
			}
		}
	}
	return m
}

// setQueueInfo 更新队列位置与模式展示（root 在队列变化后调用）。
func (m homeModel) setQueueInfo(pos, total int, mode queue.Mode) homeModel {
	m.queuePos = pos
	m.queueTotal = total
	m.queueMode = mode
	return m
}

// setLyrics 应用歌词结果（root 已校验 trackID 匹配）。
func (m homeModel) setLyrics(err error, ly *lyrics.Lyrics) homeModel {
	if m.state.Track == nil {
		return m
	}
	if err != nil || ly == nil {
		m.lyricsState = lyricsNone
		return m
	}
	switch {
	case len(ly.Lines) > 0:
		m.lyrics = ly
		m.lyricsState = lyricsSynced
		m.currentLine = -1
		m.rebuildLyrics()
	case ly.Plain != "":
		m.lyrics = ly
		m.lyricsState = lyricsPlain
		m.lyricView.SetContent(ly.Plain)
	default:
		m.lyricsState = lyricsNone
	}
	return m
}

// setCover 应用封面结果（root 已校验 trackID 匹配）。
func (m homeModel) setCover(trackID, path string, err error) homeModel {
	if m.state.Track == nil {
		return m
	}
	if err != nil {
		m.coverFallback = true
		return m
	}
	w, err := termimg.NewImageWidgetFromFile(path)
	if err != nil {
		m.coverFallback = true
		return m
	}
	w.SetSize(coverW, coverH)
	if os.Getenv("TMUX") != "" {
		// tmux 下强制字符模式并跳过终端特性探测：tmux 对 kitty/sixel 的像素
		// 尺寸查询（CSI 16t）不响应，且 go-termimg 的探测在 tmux 下每个查询
		// 超时 100ms（多次查询合计 ~500ms 同步冻结 UI）。TERMIMG_BYPASS_DETECTION
		// 是库原生支持的环境变量（detect.go getBypassedFeatures），设为
		// halfblocks 后不查询终端、直接字符模式渲染。
		// 回归：tmux 中恢复会话后按键全部失效（go-termimg 无超时读 goroutine
		// 劫持 /dev/tty 输入，32b4b3b 已在库内修复）。
		_ = os.Setenv("TERMIMG_BYPASS_DETECTION", "halfblocks")
		w.SetProtocol(termimg.Halfblocks)
	} else {
		w.SetProtocol(termimg.Auto)
	}
	m.coverWidget = w
	m.coverFallback = false
	m.coverTrackID = trackID
	// 创建后立即渲染并缓存：go-termimg 渲染涉及图片加载/缩放，代价高，
	// 不能在 view 每帧重复调用；首次渲染失败置 coverFailed，禁止 coverView
	// 每帧重试（仅 setSize 时重置允许重试一次）。
	m.coverFailed = false
	if s, err := w.Render(); err == nil && s != "" {
		m.coverRenderCache = s
	} else {
		m.coverFailed = true
	}
	return m
}

// setSize 响应窗口尺寸变化。
func (m homeModel) setSize(width, height int) homeModel {
	m.width, m.height = width, height
	m.coverRenderCache = "" // 渲染输出可能依赖终端尺寸，尺寸变化后失效重渲
	// 尺寸变化后重试一次封面渲染：重置 coverFailed 允许重试（含此前失败
	// 的场景），成功回填缓存，失败重新置位。setSize 的返回值会被赋回模型，
	// 写盘持久——view 侧（值接收者）的写入会丢失，故渲染不能放在 coverView。
	m.coverFailed = false
	if m.coverWidget != nil {
		if s, err := m.coverWidget.Render(); err == nil && s != "" {
			m.coverRenderCache = s
		} else {
			m.coverFailed = true
		}
	}
	// 歌词 viewport 尺寸 = 中间区歌词列尺寸：宽 = width-coverW-4（gap 2 + 边距 2），
	// 高 = height-2（底部两行之外的中间区高度）
	m.lyricView.Width = m.lyricsColumnWidth()
	lyricH := height - 2
	if lyricH < 3 {
		lyricH = 3
	}
	m.lyricView.Height = lyricH
	return m
}

// rebuildLyrics 用当前高亮行重渲染歌词内容。
func (m *homeModel) rebuildLyrics() {
	if m.lyrics == nil {
		return
	}
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	var sb strings.Builder
	for i, line := range m.lyrics.Lines {
		text := line.Text
		if i == m.currentLine {
			text = active.Render(text)
		}
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	m.lyricView.SetContent(strings.TrimSuffix(sb.String(), "\n"))
}

// scrollLyricsTo 让当前行保持在歌词区垂直居中附近。
func (m *homeModel) scrollLyricsTo(idx int) {
	if m.lyricView.Height <= 0 {
		return
	}
	offset := idx - m.lyricView.Height/2
	if offset < 0 {
		offset = 0
	}
	m.lyricView.SetYOffset(offset)
}

// view 渲染首页（全屏撑满：输出恰好 m.height 行）。
// 无曲目空态：全屏居中提示；有曲目：中间区（封面+歌词）+ 底部进度条行
// + 底部按钮行。
func (m homeModel) view() string {
	if m.state.Track == nil {
		hint := lipgloss.NewStyle().
			Faint(true).
			Render("🎵 未在播放\n\n按 Tab 或 2 前往搜索页，输入关键词开始搜索")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, hint)
	}
	return m.middleView() + "\n" + m.progressRowView() + "\n" + m.controlBarView()
}

// middleHeight 中间区高度（页面高 - 底部两行）。
func (m homeModel) middleHeight() int {
	h := m.height - 2
	if h < 1 {
		h = 1
	}
	return h
}

// lyricsColumnWidth 歌词列宽：页面宽 - 封面宽 - gap 2 - 边距 2。
func (m homeModel) lyricsColumnWidth() int {
	w := m.width - coverW - 4
	if w < 10 {
		w = 10
	}
	return w
}

// middleView 中间区（占 height-2 行）：封面列与歌词列水平并排（gap 2），
// 整体在页面宽度内水平居中；封面与歌词内容各自在列内垂直居中。
func (m homeModel) middleView() string {
	midH := m.middleHeight()
	lyricsW := m.lyricsColumnWidth()
	coverCol := lipgloss.Place(coverW, midH, lipgloss.Center, lipgloss.Center, m.coverView())
	lyricsCol := lipgloss.Place(lyricsW, midH, lipgloss.Center, lipgloss.Center, m.lyricsColumnView())
	block := lipgloss.JoinHorizontal(lipgloss.Top, coverCol, "  ", lyricsCol)
	return lipgloss.Place(m.width, midH, lipgloss.Center, lipgloss.Center, block)
}

// lyricsColumnView 按四种歌词态渲染歌词列内容（居中由外层 Place 处理；
// synced/plain 走 viewport：行数溢出时保持滚动 + scrollLyricsTo 当前行居中，
// 内容少时 viewport.View() 输出固定高、顶部对齐）。
func (m homeModel) lyricsColumnView() string {
	switch m.lyricsState {
	case lyricsLoading:
		return lipgloss.NewStyle().Faint(true).Render(m.spinner.View() + " 歌词加载中…")
	case lyricsNone:
		return lipgloss.NewStyle().Faint(true).Render("暂无歌词")
	case lyricsSynced, lyricsPlain:
		return m.lyricView.View()
	}
	return ""
}

// progressBarWidth 进度条可见宽（与渲染一致；点击命中区间 [0, barW)）。
func (m homeModel) progressBarWidth() int {
	timeStr := formatDuration(m.state.Position) + " / " + formatDuration(m.state.Duration)
	w := m.width - ansi.StringWidth(timeStr) - 2
	if w < 1 {
		w = 1
	}
	return w
}

// progressRowView 底部行 1（页面内 Y == height-2）：线条渐变进度条 + 时间串。
func (m homeModel) progressRowView() string {
	timeStr := formatDuration(m.state.Position) + " / " + formatDuration(m.state.Duration)
	return lineProgressBar(m.progressBarWidth(), m.percent()) + " " + timeStr
}

// controlBarView 底部行 2（页面内 Y == height-1）：控制按钮 + 曲目信息 + 队列位置。
// 按钮图标渲染在 X 区间起点：⏮(0) ⏯(4) ⏭(8) 模式图标(13)，与点击区间一致。
// 标题超宽截断（lipgloss Width）；无队列信息时省略 "3/12 · 模式" 段。
func (m homeModel) controlBarView() string {
	t := m.state.Track
	controls := "⏮   ⏯   ⏭    " + modeIcon(m.queueMode) + "  | "
	right := ""
	if m.queueTotal > 0 {
		right = fmt.Sprintf(" | %d/%d · %s", m.queuePos, m.queueTotal, modeName(m.queueMode))
	}
	avail := m.width - ansi.StringWidth(controls) - ansi.StringWidth(right)
	if avail < 1 {
		avail = 1
	}
	title := lipgloss.NewStyle().Width(avail).Render(t.Title + " - " + t.Artist)
	return controls + title + right
}

// modeIcon 三态播放模式图标。
func modeIcon(m queue.Mode) string {
	switch m {
	case queue.Shuffle:
		return "🔀"
	case queue.RepeatOne:
		return "🔂"
	default:
		return "🔁"
	}
}

// coverView 渲染封面；无封面（失败/加载中）时显示占位框。
// 纯读取：渲染只发生在 setCover（首次）与 setSize（尺寸变化后重试一次），
// 成功回填 coverRenderCache，失败置 coverFailed；coverView 绝不触发 Render，
// 避免每帧重复 16MiB 解码+缩放（值接收者写入不持久，渲染写回必须在
// setCover/setSize 这类结果被赋回模型的路径上完成）。
func (m homeModel) coverView() string {
	if m.coverWidget != nil && !m.coverFailed && m.coverRenderCache != "" {
		return m.coverRenderCache
	}
	return lipgloss.NewStyle().
		Width(coverW).
		Height(coverH).
		Align(lipgloss.Center, lipgloss.Center).
		Border(lipgloss.RoundedBorder()).
		Faint(true).
		Render("🎵\nNo Cover")
}

// percent 计算进度百分比并 clamp 到 [0,1]。
func (m homeModel) percent() float64 {
	if m.state.Duration <= 0 {
		return 0
	}
	p := m.state.Position / m.state.Duration
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

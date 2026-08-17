package ui

import (
	"fmt"
	"image"
	"os"
	"strings"

	_ "image/jpeg"
	_ "image/png"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"music-tui/coverrender"
	"music-tui/logger"
	"music-tui/lyrics"
	"music-tui/lyricshm"
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

// ---- 底部按钮行布局 ----

// 按钮行三栏布局（渲染与鼠标命中共用）：左 = 歌曲信息（截断），
// 中 = 控制按钮 |< >|（屏幕水平居中），右 = 播放模式 + 队列位置（右对齐）。
//
// 中栏用确定宽度字符（ASCII + 中文）而非 ⏮⏯⏭🔁 等 Ambiguous 宽度字符：
// 后者 ansi 按 1 宽、CJK 终端按 2 宽渲染，布局与命中必然偏移
// （曾用 ⏮⏯⏭，命中区间与视觉位置在 CJK 终端错位）。中栏串固定 10 列：
//
//	"|<  " + play(2) + "  >|"，play = "||"（暂停）或 "> "（播放）。
type controlBarLayout struct {
	centerStart int // 中栏起点列（|< 图标列）
	rightStart  int // 右栏起点列（模式文本列）
}

const (
	centerBarW   = 10 // 中栏固定渲染宽（全部确定宽度字符）
	btnPrevRel   = 0  // |< 相对中栏起点
	btnToggleRel = 4  // || / >  （2 宽）
	btnNextRel   = 8  // >|
	btnHitWidth  = 3  // 命中区 = 图标 2 宽 + 1 容差
	leftMinW     = 10 // 左栏最小宽（窄窗口弹性退化时保留）
)

// controlBarLayout 计算按钮行三栏列区间（渲染与命中同源，防漂移）。
// 中栏优先在屏幕水平居中（操作键始终位于屏幕正中，不随标题/模式文本
// 宽度漂移）；窗口过窄、中栏与两侧（左栏最小宽/右栏）间距不足时，
// 退化为在左右栏之间弹性居中（极窄窗口仍不与右栏重叠）。
func (m homeModel) controlBarLayout(width int) controlBarLayout {
	centerW := centerBarW
	rightW := ansi.StringWidth(m.modeRightText())
	// 右栏右对齐（右缘留 2 列）：起点 = width - rightW - 2
	rightStart := width - rightW - 2
	if rightStart < 0 {
		rightStart = 0
	}
	// 中栏优先屏幕水平居中
	centerStart := (width - centerW) / 2
	const minGap = 2 // 中栏与左/右栏的最小间距
	leftMin := leftMinW + minGap
	if centerStart < leftMin {
		centerStart = leftMin
	}
	// 中栏右缘 + 间距放不下右栏时：退化为在 [leftMin, rightStart-minGap) 内
	// 弹性居中（原“左右栏之间居中”语义，极窄窗口仍不重叠）。
	if centerStart+centerW+minGap > rightStart {
		midStart := leftMin
		midEnd := rightStart - minGap
		centerStart = midStart + (midEnd-midStart-centerW)/2
		if centerStart < midStart {
			centerStart = midStart
		}
	}
	return controlBarLayout{centerStart: centerStart, rightStart: rightStart}
}

// modeRightText 右栏文本：模式名（中文，宽度确定）+ 队列位置。
func (m homeModel) modeRightText() string {
	s := modeName(m.queueMode)
	if m.queueTotal > 0 {
		s += "  " + fmt.Sprintf("%d/%d", m.queuePos, m.queueTotal)
	}
	return s
}

// hitBtn 判断点击列 x 是否落在起点 start、宽 btnHitWidth 的按钮区间内。
func hitBtn(x, start int) bool {
	return x >= start && x < start+btnHitWidth
}

// lyricsState 歌词展示状态（三种态）。
type lyricsState int

const (
	lyricsLoading lyricsState = iota // 歌词加载中
	lyricsSynced                     // 同步歌词（高亮滚动）
	lyricsNone                       // 无歌词
)

// homeModel 首页：封面 + 歌词 + 底部进度条行与按钮行。
// 播放状态由 root 通过 syncState 推入，页面自身不持有服务。
type homeModel struct {
	player player.Player

	width, height int

	state model.PlaybackState

	queuePos int // 当前曲目在队列中的 1 基位置；0 = 无当前曲目（不隐藏：
	// queueTotal>0 时仍渲染 "0/N · 模式"，见 controlBarView）
	queueTotal int // 队列总长；0 = 无队列信息（隐藏展示）
	queueMode  queue.Mode

	spinner   spinner.Model
	lyricView viewport.Model

	// loading 当前曲目加载中（root 派生：spinner tick 时切歌 2s 未收到
	// TrackStarted 置位；进度行显示"加载中…"替代进度条）。
	loading bool

	lyricsState lyricsState
	lyrics      *lyrics.Lyrics
	currentLine int // 当前高亮行下标;-1 = 无高亮

	// lyricFile 歌词行实时写入器(nil = 不启用;root 经 NewModel 注入)。
	lyricFile *lyricshm.Writer

	// aiTitle/aiArtist AI 识别出的清洗后歌名/歌手（展示覆盖）：非空时
	// 控制栏等展示位用它替代原始 YouTube 标题；切歌时清空。
	aiTitle  string
	aiArtist string

	coverRenderCache string   // 封面渲染缓存：setCover 时渲染一次（固定 30×17，与终端尺寸无关）
	coverFallback    bool     // 封面加载失败 → 占位框
	coverMode        uint8    // 当前封面渲染模式（0=半块/1=kitty/2=sixel；与 coverrender.Mode 对应）

	// sixel 外带覆盖状态：布局只放半块色块，六边形 DCS 在 view() 内按屏幕绝对坐标
	// 直接写 stdout（像素驻留覆于文本之上，见 ui/coveroverlay.go）。指针共享：
	// view() 是值接收者，状态变更必须落在共享指针上才能跨渲染循环持久。
	sixelPayload string        // 全帧 DCS 载荷（空 = 无六像素材质）
	sixelSt      *sixelState   // 写出状态（token/位置）：view() 内变更，指针可见

	// 中间区渲染缓存（P1-2）：中间区内容仅随封面/歌词/尺寸变化，播放中进度
	// 推进每帧直接复用（省去 3 个全屏 lipgloss.Place + 逐行宽度计算，渲染 CPU
	// 热点）。在内容变化点（setCover/setLyrics/syncState 行切换/setSize/滚轮）
	// 重建；歌词加载中 spinner 每帧动画，读取时跳过缓存。
	middleCache  string
	middleCacheW int
	middleCacheH int
}

func newHomeModel(p player.Player) homeModel {
	return homeModel{
		player:      p,
		spinner:     spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("63")))),
		lyricView:   viewport.New(0, 0),
		lyricsState: lyricsNone,
		currentLine: -1,
		sixelSt:      &sixelState{},
	}
}

// Init 首页无独立 cmd（spinner tick 由 root 统一驱动）。
func (m homeModel) Init() tea.Cmd { return nil }

// Update 处理首页局部按键（←/→ seek、, 上一首、. 下一首）与鼠标
// （进度条点击 seek、按钮行点击）；全局按键（空格等）由 root 处理。
// 鼠标坐标换算：屏幕坐标 - 3 = 页面坐标（顶部空行 + Tab 栏 + 分隔线占 3 行，
// root.onMouse 在 Y!=1 时已把事件 delegate 到本页）。
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
		case "m":
			// 模式三态循环（与队列页 s 键语义一致）：模式是全局队列属性，
			// 不依赖当前播放，无曲目时同样可切换。
			return m, emitToggleMode()
		}
	case tea.MouseMsg:
		// 屏幕坐标 → 页面坐标：顶部 3 行（空行 + Tab 栏标签 + 分隔线），页面从屏幕行 3 起。
		// （回归：曾按 1 行 Tab 换算（-1），进度条/按钮点击整体偏移 1 行不命中；
		//  顶部留空后必须同步 -3，否则点击整体偏移 1 行。）
		pageY := msg.Y - 3
		// 滚轮：歌词视口手动滚动（仅歌词列区域：X ≥ 封面列+gap，Y 在中间区）。
		// 播放推进时 scrollLyricsTo 会重新把当前行居中（自动跟随优先）。
		if tea.MouseEvent(msg).IsWheel() {
			if m.lyricsState == lyricsSynced {
				if pageY >= 0 && pageY < m.height-2 && msg.X >= coverW+2 {
					if msg.Button == tea.MouseButtonWheelUp {
						m.lyricView.SetYOffset(m.lyricView.YOffset - 3)
					} else if msg.Button == tea.MouseButtonWheelDown {
						m.lyricView.SetYOffset(m.lyricView.YOffset + 3)
					}
					m = m.rebuildMiddleCache() // 滚动改变视口输出：中间区缓存重建
				}
			}
			return m, nil
		}
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
			// 按钮行：左信息区不响应；中栏三键 + 右栏模式按钮。
			if m.state.Track == nil {
				return m, nil
			}
			lay := m.controlBarLayout(m.width)
			switch {
			case hitBtn(msg.X, lay.centerStart+btnPrevRel):
				return m, emitPrevTrack()
			case hitBtn(msg.X, lay.centerStart+btnToggleRel):
				return m, emitTogglePlay()
			case hitBtn(msg.X, lay.centerStart+btnNextRel):
				return m, emitNextTrack()
			case msg.X >= lay.rightStart && msg.X < lay.rightStart+ansi.StringWidth(m.modeRightText())+2:
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
	m.coverRenderCache = ""
	m.coverFallback = false
	m = m.clearSixel() // 切歌：清除已绘制的六像素，等待新封面
	m.middleCache = "" // 内容全部作废：中间区缓存失效（loading 态读取时也跳过）
	m.aiTitle, m.aiArtist = "", ""
	// 切歌:文件写入新曲目歌名(歌词加载中/无歌词期间 OBS 等展示歌名)。
	// 注意 trackLabel 在 aiTitle 清空后取原始标题+歌手。
	if m.lyricFile != nil {
		m.lyricFile.WriteLine(m.trackLabel())
	}
	return m
}

// setAITrack 应用 AI 识别的清洗后歌名/歌手(展示覆盖,root 在歌词结果
// 到达时调用)。
func (m homeModel) setAITrack(title, artist string) homeModel {
	m.aiTitle, m.aiArtist = title, artist
	// 歌词未显示时,更新歌词文件中的歌名(AI 识别结果更准确);
	// 歌词已同步时文件里是歌词行,不覆盖。
	if m.lyricFile != nil && m.lyricsState != lyricsSynced {
		m.lyricFile.WriteLine(m.trackLabel())
	}
	return m
}

// trackLabel 当前曲目标题标签：AI 识别结果优先，回落原始标题；
// artist 为空时省略分隔符。
func (m homeModel) trackLabel() string {
	t := m.state.Track
	if t == nil {
		return ""
	}
	title, artist := t.Title, t.Artist
	if m.aiTitle != "" {
		title, artist = m.aiTitle, m.aiArtist
	}
	if artist == "" {
		return title
	}
	return title + " - " + artist
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
	// 歌词高亮:二分查找当前行,行变化时才重渲染(而非每帧)
	if m.lyricsState == lyricsSynced && m.lyrics != nil {
		idx, _ := m.lyrics.LineAt(m.state.Position)
		if idx != m.currentLine {
			m.currentLine = idx
			if idx >= 0 && m.lyricFile != nil {
				m.lyricFile.WriteLine(m.lyrics.Lines[idx].Text)
			}
			m.rebuildLyrics()
			if idx >= 0 {
				m.scrollLyricsTo(idx)
			}
			// 行切换：歌词内容/滚动位置变化，中间区缓存重建
			//（注意必须在 scrollLyricsTo 之后：滚动改变视口输出）。
			return m.rebuildMiddleCache()
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
		return m.rebuildMiddleCache()
	}
	if len(ly.Lines) > 0 {
		m.lyrics = ly
		m.lyricsState = lyricsSynced
		m.currentLine = -1
		// 歌词到达后按动态公式设置视口高度（padding 模型，不随行数收缩）。
		m.lyricView.Height = m.lyricsHeight()
		m.rebuildLyrics()
	} else {
		m.lyricsState = lyricsNone
	}
	return m.rebuildMiddleCache()
}

// currentLyricText 返回当前高亮歌词行文本（同步歌词态且有高亮行时）；
// 无歌词/加载中/无高亮行返回空串。供 root 状态栏中间段展示当前歌词行。
func (m homeModel) currentLyricText() string {
	if m.lyricsState != lyricsSynced || m.lyrics == nil {
		return ""
	}
	if m.currentLine < 0 || m.currentLine >= len(m.lyrics.Lines) {
		return ""
	}
	return m.lyrics.Lines[m.currentLine].Text
}

// setCover 应用封面结果（root 已校验 trackID 匹配）。
// 渲染三模式（coverrender.DetectMode）：
//   - kitty：coverRenderCache = 行内协议序列（APC 零宽、17×30 网格，直通终端），
//     布局文本流内即可显示图像（见 coverrender 包注释）；
//   - sixel：coverRenderCache = 半块自绘色块（布局底座），六边形 DCS 由
//     view() 按屏幕绝对坐标外带写出（像素覆于半块之上，不占文本流）；
//   - halfblocks：coverRenderCache = 半块自绘（任何终端可用）。
func (m homeModel) setCover(trackID, path string, err error) homeModel {
	if m.state.Track == nil {
		return m
	}
	if err != nil {
		m.clearSixel() // 回退：清除已绘制的六像素
		m.coverFallback = true
		return m.rebuildMiddleCache()
	}
	f, err := os.Open(path)
	if err != nil {
		m.clearSixel()
		m.coverFallback = true
		return m.rebuildMiddleCache()
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		m.clearSixel()
		m.coverFallback = true
		return m.rebuildMiddleCache()
	}
	mode := coverrender.DetectMode()
	m.coverMode = uint8(mode)
	logger.Debug("封面探测: TERM=%q TERM_PROGRAM=%q TMUX=%q KITTY_WINDOW_ID=%q → 模式 %v",
		os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), os.Getenv("TMUX"), os.Getenv("KITTY_WINDOW_ID"), mode)
	logger.Debug("封面渲染模式: %v (track=%s path=%s)", mode, trackID, path)
	switch mode {
	case coverrender.ModeKitty:
		// 行内 kitty 序列：APC 零宽、占位符网格 17×30，直通终端显示
		cellW, cellH := coverrender.FontCellSize()
		s := coverrender.Kitty(img, coverW, coverH, cellW, cellH)
		logger.Debug("kitty 序列生成: %d 字节 %d 行 (cell %dx%d)", len(s), strings.Count(s, "\n")+1, cellW, cellH)
		if s == "" {
			m.coverFallback = true
			return m.rebuildMiddleCache()
		}
		m.clearSixel()
		m.coverRenderCache = s
		m.coverFallback = false
		return m.rebuildMiddleCache()
	case coverrender.ModeSixel:
		// 布局底座 = 空白 30×17 格（不画任何文本/SGR——六色图像覆盖其上，
		// 文本底座会把图像区域整块涂掉/干扰显示）；DCS 由 view() 外带写出
		cellW, cellH := coverrender.FontCellSize()
		m.sixelPayload = coverrender.Sixel(img, coverW, coverH, cellW, cellH)
		logger.Debug("sixel 载荷生成: %d 字节 (cell %dx%d, 前缀 %q)",
			len(m.sixelPayload), cellW, cellH, m.sixelPayload[:8])
		if st := m.sixelSt; st != nil {
			st.token = "" // 强制下次 view() 重写（含清旧景）
			st.drawn = false
		}
		s := blankCoverGrid(coverW, coverH)
		m.coverRenderCache = s
		m.coverFallback = false
		return m.rebuildMiddleCache()
	default: // halfblocks 回退
		m.clearSixel()
		s := coverrender.HalfBlocks(img, coverW, coverH)
		if s == "" {
			m.coverFallback = true
			return m.rebuildMiddleCache()
		}
		m.coverRenderCache = s
		m.coverFallback = false
		return m.rebuildMiddleCache()
	}
}

// setSize 响应窗口尺寸变化。
func (m homeModel) setSize(width, height int) homeModel {
	m.width, m.height = width, height
	// 封面字符画固定 30×17、与终端尺寸无关：setSize 不清缓存不重渲。
	// 歌词 viewport 尺寸 = 中间区歌词列尺寸：宽 = width-coverW-4（gap 2 + 边距 2），
	// 高 = 动态视口行数 min(21, 中间区高−上下各 2 行留白)（见 lyricsHeight），
	// 窄窗口下自动收缩。
	m.lyricView.Width = m.lyricsColumnWidth()
	m.lyricView.Height = m.lyricsHeight()
	// 视口高度可能已变化（留白/上限动态计算），先重建 padding 内容再重算
	// 滚动偏移：此前基于未知尺寸（Height=1）的 scrollLyricsTo 会留下越界
	// YOffset，导致歌词首行被吞（回归：TestHomeLyricsCenteredWhenFew）。
	if m.lyricsState == lyricsSynced && m.lyrics != nil {
		m.rebuildLyrics()
		if m.currentLine >= 0 {
			m.scrollLyricsTo(m.currentLine)
		}
	}
	// 尺寸变化：中间区几何/裁剪全部随之变化，缓存必须重建
	return m.rebuildMiddleCache()
}

// lyricsHeight 歌词视口高度：动态行数 min(21, 中间区高−上下各 2 行留白)，
// 至少 1 行（见 lyricViewportHeight）。synced 态视口恒为 H（padding 模型，
// 不随歌词行数收缩）。
func (m homeModel) lyricsHeight() int {
	return lyricViewportHeight(m.middleHeight())
}

// lyricActiveStyle 当前歌词行高亮样式（首页歌词区与状态栏中间段共用）：
// 加粗 + 粉色 212。
var lyricActiveStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

// rebuildLyrics 用当前高亮行重渲染歌词内容：内容 = H/2 行空白 + 歌词行
// + H/2 行空白（padding 模型，配合 scrollLyricsTo 使当前行恒在视口中央；
// H 变化后必须重调本函数，padding 行数随 H/2 变化）。
func (m *homeModel) rebuildLyrics() {
	if m.lyrics == nil {
		return
	}
	pad := m.lyricView.Height / 2
	var sb strings.Builder
	sb.WriteString(strings.Repeat("\n", pad))
	for i, line := range m.lyrics.Lines {
		text := line.Text
		if i == m.currentLine {
			text = lyricActiveStyle.Render(text)
		}
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Repeat("\n", pad))
	m.lyricView.SetContent(strings.TrimSuffix(sb.String(), "\n"))
}

// scrollLyricsTo 让当前行保持在歌词区视口中央（padding 模型：YOffset = 当前行）。
// 开头首行在中央（上方整片空白）、结尾末行停中央（下方可空白）；行数少时
// 同样滚动 N−1 行，首末行都在中央。
func (m *homeModel) scrollLyricsTo(idx int) {
	if m.lyricView.Height <= 0 || m.lyrics == nil {
		return
	}
	m.lyricView.SetYOffset(lyricScrollOffset(idx, len(m.lyrics.Lines)))
}

// view 渲染首页（全屏撑满：输出恰好 m.height 行）。
// 无曲目空态：全屏居中提示；有曲目：中间区（封面+歌词）+ 底部进度条行
// + 底部按钮行。
func (m homeModel) view() string {
	// 六像覆盖层：在布局文本流之外把封面图像画到屏幕绝对坐标上
	// （sixel 模式；kitty 走行内序列、半块走纯文本，均无需此步）。
	m.ensureSixel()
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
// 读取渲染缓存：中间区内容仅随封面/歌词/尺寸变化，播放中进度推进（5fps）
// 每帧直接复用缓存（省去 3 个全屏 Place + 逐行宽度计算）；缓存缺失时现场
// 渲染兜底（不应发生：所有内容变化点都会重建）。
func (m homeModel) middleView() string {
	if m.middleCache != "" && m.middleCacheW == m.width && m.middleCacheH == m.middleHeight() &&
		m.lyricsState != lyricsLoading { // 加载中 spinner 每帧动画，不缓存
		return m.middleCache
	}
	return m.renderMiddleView()
}

// renderMiddleView 渲染中间区（封面列 + 歌词列 + 整体居中）。
func (m homeModel) renderMiddleView() string {
	midH := m.middleHeight()
	lyricsW := m.lyricsColumnWidth()
	coverCol := lipgloss.Place(coverW, midH, lipgloss.Center, lipgloss.Center, m.coverView())
	lyricsCol := lipgloss.Place(lyricsW, midH, lipgloss.Center, lipgloss.Center, m.lyricsColumnView())
	block := lipgloss.JoinHorizontal(lipgloss.Top, coverCol, "  ", lyricsCol)
	return lipgloss.Place(m.width, midH, lipgloss.Center, lipgloss.Center, block)
}

// rebuildMiddleCache 在中间区内容变化点重建渲染缓存（返回携带新缓存的模型）。
// 尺寸未初始化（width/height<=0）时清空缓存（渲染无意义）。
func (m homeModel) rebuildMiddleCache() homeModel {
	if m.width <= 0 || m.height <= 0 {
		m.middleCache = ""
		return m
	}
	m.middleCache = m.renderMiddleView()
	m.middleCacheW = m.width
	m.middleCacheH = m.middleHeight()
	return m
}

// lyricsColumnView 按三种歌词态渲染歌词列内容（居中由外层 Place 处理；
// synced 走 viewport：padding 模型下内容恒 ≥ H，View() 恒输出 H 行，
// scrollLyricsTo 使当前行恒在视口中央）。
func (m homeModel) lyricsColumnView() string {
	switch m.lyricsState {
	case lyricsLoading:
		// 提示文本同样以屏幕中心居中（centerLyrics 补尾空格到列宽，
		// 外层 Place 不再重新水平居中）。
		return m.centerLyrics(lipgloss.NewStyle().Faint(true).Render(m.spinner.View() + " 歌词加载中…"))
	case lyricsNone:
		return m.centerLyrics(lipgloss.NewStyle().Faint(true).Render("暂无歌词"))
	case lyricsSynced:
		// 歌词文本以屏幕中心为基准水平居中：viewport 输出每行已 pad 到
		// 列宽（左对齐），重新计算前导空格使文本中心 ≈ 屏幕中心。
		// （歌词列内居中会让文本整体偏右 ~15 列：歌词列起点 = 封面右缘
		//  + gap = 32，列内居中 → 文本中心 = 32 + 列宽/2 = 76 ≠ 屏幕中心 60。
		//  注：viewport 的 Style.Align 不生效——bubbles viewport.View 会把
		//  Style 的 Width/Height Unset 后再 Render。）
		content := m.lyricView.View()
		// AI 增强路径来源标识：歌词块上方一行小字（不参与视口滚动数学，
		// 不影响 scrollLyricsTo 当前行居中）。
		// if m.lyrics != nil && m.lyrics.Source == lyrics.LyricsSourceAI {
		// 	tag := lipgloss.NewStyle().Faint(true).Render("〔AI 匹配〕")
		// 	return m.centerLyrics(tag + "\n" + content)
		// }
		return m.centerLyrics(content)
	}
	return ""
}

// centerLyrics 把每行文本水平居中到屏幕中心（歌词列起点 = coverW+2），
// 并补尾空格到歌词列宽——外层 Place(lyricsW, ...) 对满宽行不再重新水平
// 居中（否则短行/提示文本会被推回歌词列中心，回归：暂无歌词偏右）。
// viewport 填充的行尾空格先剔除，避免宽度计算失真；超宽行保持原样。
func (m homeModel) centerLyrics(s string) string {
	lyricsW := m.lyricsColumnWidth()
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		trimmed := strings.TrimRight(ln, " ")
		vis := ansi.StringWidth(trimmed)
		// 歌词在封面右侧剩余空间（歌词列）内居中：中心 = 列起点 + 列宽/2。
		// （回归：曾以屏幕中心为基准，歌词偏左、封面右侧大片空白；
		//  超宽行 pad 为负时左对齐——列内也放不下时无居中可言。）
		pad := (lyricsW - vis) / 2
		if pad < 0 {
			pad = 0
		}
		tail := lyricsW - pad - vis
		if tail < 0 {
			tail = 0
		}
		lines[i] = strings.Repeat(" ", pad) + trimmed + strings.Repeat(" ", tail)
	}
	return strings.Join(lines, "\n")
}

// progressBarWidth 进度条可见宽（与渲染一致；点击命中区间 [0, barW)）。
// 行布局 = 进度条(barW) + 1 空格 + 时间串(timeW)，可见宽必须恰为页面宽
// （此前 barW = width-timeW-2，整行比页面窄 1 列——回归：进度条行可见宽断言）。
func (m homeModel) progressBarWidth() int {
	timeStr := formatDuration(m.state.Position) + " / " + formatDuration(m.state.Duration)
	w := m.width - ansi.StringWidth(timeStr) - 1
	if w < 1 {
		w = 1
	}
	return w
}

// progressRowView 底部行 1（页面内 Y == height-2）：线条渐变进度条 + 时间串。
// 加载中（切歌 2s 未就绪）时以提示替代进度条——取流悬挂卡住可感知。
func (m homeModel) progressRowView() string {
	if m.loading {
		return "  ⏳ 加载中…"
	}
	timeStr := formatDuration(m.state.Position) + " / " + formatDuration(m.state.Duration)
	return lineProgressBar(m.progressBarWidth(), m.percent()) + " " + timeStr
}

// controlBarView 底部行 2（页面内 Y == height-1）：控制按钮 + 曲目信息 + 队列位置。
// 按钮图标渲染在 X 区间起点：⏮(0) ⏯(4) ⏭(8) 模式图标(13)，与点击区间一致。
// 标题超宽时按可见宽截断（lipgloss 的 Width/MaxWidth 是折行不是截断，窄窗口
// 会把按钮行撑成多行推出布局——回归：TestHomeViewNarrowWindow；ansi.Truncate
// 按显示宽度截断且不破坏 ANSI 序列/宽字符）；无队列信息时省略 "3/12 · 模式" 段。
func (m homeModel) controlBarView() string {
	t := m.state.Track
	if t == nil {
		return ""
	}
	width := m.width
	play := "> " // 播放（播放中显示暂停图标）
	if m.state.Playing {
		play = "||"
	}
	center := "|<  " + play + "  >|"
	right := m.modeRightText()
	rightW := ansi.StringWidth(right)
	lay := m.controlBarLayout(width)
	leftW := lay.centerStart - 2 // 左栏可用宽：中栏屏幕居中后，左缘到中栏间距 2
	if leftW < leftMinW {
		leftW = leftMinW
	}
	left := ansi.Truncate(m.trackLabel(), leftW, "…")
	// 补位按 left 实际显示宽计算（而非 leftW 上限）：标题短于左栏宽/截断符
	// 使 left 不足 leftW 时，差额不补齐会导致中栏/右栏整体贴左偏移——
	// 渲染与命中区间（controlBarLayout）错位，且右侧大片空白。
	leftPad := lay.centerStart - ansi.StringWidth(left) - 2
	if leftPad < 0 {
		leftPad = 0
	}
	// 中栏→右栏间距同样取自 layout（rightStart - 中栏终点）：右栏右对齐
	// 到 rightStart（右缘留 2 列），gap 随窗口宽度变化；若按固定 2+边距
	// 计算会与命中区间错位 2 列（回归：右栏渲染右移、行尾无留白）。
	gapRight := lay.rightStart - (lay.centerStart + centerBarW)
	if gapRight < 0 {
		gapRight = 0
	}
	padRight := width - lay.rightStart - rightW
	if padRight < 0 {
		padRight = 0
	}
	return left + strings.Repeat(" ", 2+leftPad) + center + strings.Repeat(" ", gapRight) + right + strings.Repeat(" ", padRight)
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
// 纯读取：渲染只发生在 setCover（解码 + 自绘字符画，一次完成），
// 缓存固定 30×17 与终端尺寸无关；coverView 绝不触发渲染。
// 窄窗口（中间区高 < 封面行数）：lipgloss.Place 不截断超高内容，会把进度条/
// 按钮行推出可视区（回归：TestHomeViewNarrowWindow），故按行裁剪到中间区高。
// 对整块渲染串按行切分是安全的：halfblocks 渲染每行自含 ANSI 序列，无跨行
// 转义；占位框裁剪会失去底边框，窄窗口下可接受。窗口尺寸未初始化
// （height==0，尚未收到 WindowSizeMsg）时不裁剪，保留完整封面。
func (m homeModel) coverView() string {
	var s string
	if !m.coverFallback && m.coverRenderCache != "" {
		s = m.coverRenderCache
	} else {
		s = lipgloss.NewStyle().
			Width(coverW).
			Height(coverH).
			Align(lipgloss.Center, lipgloss.Center).
			Border(lipgloss.RoundedBorder()).
			Faint(true).
			Render("🎵\nNo Cover")
	}
	if m.height > 0 {
		if midH := m.middleHeight(); midH < strings.Count(s, "\n")+1 {
			lines := strings.Split(s, "\n")
			s = strings.Join(lines[:midH], "\n")
		}
	}
	return s
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

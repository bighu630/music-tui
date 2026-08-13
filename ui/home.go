package ui

import (
	"strings"

	"github.com/blacktop/go-termimg"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/lyrics"
	"music-tui/model"
	"music-tui/player"
)

// 封面区尺寸（字符单元格）。
const (
	coverW = 30
	coverH = 17
	topH   = coverH + 2 // 顶部区域总高，供歌词区高度计算
)

// lyricsState 歌词展示状态（四种态）。
type lyricsState int

const (
	lyricsLoading lyricsState = iota // 歌词加载中
	lyricsSynced                     // 同步歌词（高亮滚动）
	lyricsPlain                      // 纯文本歌词（整页展示）
	lyricsNone                       // 无歌词
)

// homeModel 首页：封面 + 歌曲信息 + 进度条 + 播放控制 + 同步歌词。
// 播放状态由 root 通过 syncState 推入，页面自身不持有服务。
type homeModel struct {
	player player.Player

	width, height int

	state model.PlaybackState

	progress  progress.Model
	spinner   spinner.Model
	lyricView viewport.Model

	lyricsState lyricsState
	lyrics      *lyrics.Lyrics
	currentLine int // 当前高亮行下标；-1 = 无高亮

	coverWidget   *termimg.ImageWidget
	coverFallback bool   // 降级链全部失败 → 占位框
	coverTrackID  string // 当前封面所属歌曲 ID
}

func newHomeModel(p player.Player) homeModel {
	return homeModel{
		player:      p,
		progress:    progress.New(progress.WithDefaultGradient(), progress.WithWidth(40)),
		spinner:     spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("63")))),
		lyricView:   viewport.New(0, 0),
		lyricsState: lyricsNone,
		currentLine: -1,
	}
}

// Init 首页无独立 cmd（spinner tick 由 root 统一驱动）。
func (m homeModel) Init() tea.Cmd { return nil }

// Update 处理首页局部按键（←/→ seek）；全局按键由 root 处理。
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
	w.SetSize(coverW, coverH).SetProtocol(termimg.Auto)
	m.coverWidget = w
	m.coverFallback = false
	m.coverTrackID = trackID
	return m
}

// setSize 响应窗口尺寸变化。
func (m homeModel) setSize(width, height int) homeModel {
	m.width, m.height = width, height
	m.lyricView.Width = width
	lyricH := height - topH - 2
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

// view 渲染首页。
func (m homeModel) view() string {
	if m.state.Track == nil {
		return lipgloss.NewStyle().
			Padding(2, 0).
			Faint(true).
			Render("🎵 未在播放\n\n按 Tab 或 2 前往搜索页，输入关键词开始搜索")
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, m.coverView(), "  ", m.trackInfoView())
	return lipgloss.JoinVertical(lipgloss.Left, top, "\n", m.lyricsView())
}

// coverView 渲染封面；无封面（失败/加载中）时显示占位框。
func (m homeModel) coverView() string {
	if m.coverWidget != nil {
		if s, err := m.coverWidget.Render(); err == nil && s != "" {
			return s
		}
	}
	return lipgloss.NewStyle().
		Width(coverW).
		Height(coverH).
		Align(lipgloss.Center, lipgloss.Center).
		Border(lipgloss.RoundedBorder()).
		Faint(true).
		Render("🎵\nNo Cover")
}

// trackInfoView 渲染歌曲信息 + 进度条 + 播放状态。
func (m homeModel) trackInfoView() string {
	t := m.state.Track
	infoW := m.width - coverW - 6
	if infoW < 20 {
		infoW = 20
	}
	title := lipgloss.NewStyle().Bold(true).Width(infoW).Render(t.Title)
	meta := lipgloss.NewStyle().Faint(true).Render(t.Artist + " · " + t.Source)
	bar := m.progress.ViewAs(m.percent())
	pos := formatDuration(m.state.Position) + " / " + formatDuration(m.state.Duration)
	status := "⏸ 已暂停"
	if m.state.Playing {
		status = "⏵ 播放中"
	}
	return strings.Join([]string{title, meta, bar + "  " + pos, status}, "\n")
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

// lyricsView 按四种歌词态渲染歌词区。
func (m homeModel) lyricsView() string {
	style := lipgloss.NewStyle().Faint(true)
	switch m.lyricsState {
	case lyricsLoading:
		return style.Render(m.spinner.View() + " 歌词加载中…")
	case lyricsNone:
		return style.Render("暂无歌词")
	case lyricsSynced, lyricsPlain:
		return m.lyricView.View()
	}
	return ""
}

package ui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/model"
	"music-tui/search"
)

// searchState 搜索页状态。
type searchState int

const (
	searchIdle    searchState = iota // 未搜索
	searchLoading                    // 搜索中
	searchDone                       // 有结果/错误/空（已失焦输入框）
)

// searchResultsMsg 搜索结果（search 页 cmd 产出，root 路由回 search 页）。
type searchResultsMsg struct {
	tracks []model.Track
	err    error
}

// trackItem 适配 list.Item：标题 - 作者 · 时长（单行）。
type trackItem struct {
	track model.Track
}

func (i trackItem) Title() string {
	return formatTrackLine(i.track.Title, i.track.Artist, formatDuration(i.track.Duration))
}
func (i trackItem) Description() string { return "" }
func (i trackItem) FilterValue() string { return i.track.Title + " " + i.track.Artist }

// searchModel 搜索页：输入框 + 结果列表 + 加载/空/错误态。
type searchModel struct {
	adapter search.SearchAdapter

	input   textinput.Model
	list    list.Model
	spinner spinner.Model

	state   searchState
	err     string
	results []model.Track

	width, height int
}

func newSearchModel(adapter search.SearchAdapter) searchModel {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false // 单行条目（标题 - 作者 · 时长）
	l := list.New(nil, delegate, 80, 24)
	l.Title = "搜索结果"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)

	ti := textinput.New()
	ti.Placeholder = "输入关键词，Enter 搜索"
	ti.CharLimit = 120
	ti.Focus()

	return searchModel{
		adapter: adapter,
		input:   ti,
		list:    l,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("63")))),
		state:   searchIdle,
	}
}

// typing 返回输入框是否聚焦（聚焦时空格/q/p 让位给输入；数字 1-5
// 仍由 root 全局切页，其余数字字符由输入框消费）。
func (s searchModel) typing() bool { return s.input.Focused() }

// selectedTrack 返回当前选中的搜索结果（供全局 a 键添加到播放列表）；
// 未完成搜索/无结果/无选中项时返回 false。
func (s searchModel) selectedTrack() (model.Track, bool) {
	if s.state != searchDone || len(s.results) == 0 {
		return model.Track{}, false
	}
	if item, ok := s.list.SelectedItem().(trackItem); ok {
		return item.track, true
	}
	return model.Track{}, false
}

// Update 处理搜索页局部按键。
func (s searchModel) Update(msg tea.Msg) (searchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if s.state == searchLoading {
				return s, nil
			}
			if s.typing() {
				query := strings.TrimSpace(s.input.Value())
				if query == "" {
					return s, nil
				}
				s.state = searchLoading
				s.err = ""
				return s, s.doSearch(query)
			}
			// 列表选中项 → 播放
			if s.state == searchDone && len(s.results) > 0 {
				if item, ok := s.list.SelectedItem().(trackItem); ok {
					return s, emitTrackSelected(item.track)
				}
			}
			return s, nil
		case "p":
			// 输入框聚焦时 p 是输入字符（走下方 typing 分支插入）
			if s.typing() {
				break
			}
			// 列表选中项 → 播放（与 Enter 同义，替换语义）
			if s.state == searchDone && len(s.results) > 0 {
				if item, ok := s.list.SelectedItem().(trackItem); ok {
					return s, emitTrackSelected(item.track)
				}
			}
			return s, nil
		case "esc":
			if !s.typing() {
				// 从结果/空结果态退回输入框：清空结果列表，回到干净的未搜索态
				// （输入框文字保留，可直接 Enter 重新搜索）。
				s.state = searchIdle
				s.err = ""
				s.results = nil
				s.list.SetItems(nil)
				return s, s.input.Focus()
			}
			return s, nil
		}
	}
	if s.typing() {
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return s, cmd
}

// tick 由 root 在 spinner.TickMsg 时调用。
func (s searchModel) tick(msg tea.Msg) searchModel {
	s.spinner, _ = s.spinner.Update(msg)
	return s
}

// doSearch 异步执行搜索（适配器自带 10s 超时，外层 15s 兜底）。
func (s searchModel) doSearch(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tracks, err := s.adapter.Search(ctx, query)
		return searchResultsMsg{tracks: tracks, err: err}
	}
}

// withResults 应用搜索结果（root 路由；无论结果如何都让输入框失焦，
// 使 ↑↓ 操作列表、空格/数字等恢复全局语义）。
func (s searchModel) withResults(msg searchResultsMsg) searchModel {
	s.state = searchDone
	s.err = ""
	if msg.err != nil {
		s.err = msg.err.Error()
		s.results = nil
		s.input.Focus()
		return s
	}
	s.results = msg.tracks
	s.input.Blur()
	items := make([]list.Item, 0, len(msg.tracks))
	for _, tr := range msg.tracks {
		items = append(items, trackItem{track: tr})
	}
	s.list.SetItems(items)
	return s
}

// setSize 响应窗口尺寸变化。
func (s searchModel) setSize(width, height int) searchModel {
	s.width, s.height = width, height
	s.input.Width = width - 6
	if s.input.Width < 10 {
		s.input.Width = 10
	}
	s.list.SetSize(width, height-5)
	return s
}

// view 渲染搜索页。
func (s searchModel) view() string {
	var sb strings.Builder
	sb.WriteString(s.input.View())
	sb.WriteString("\n\n")
	switch s.state {
	// case searchIdle:
	// 	sb.WriteString(lipgloss.NewStyle().Faint(true).Render("输入关键词后按 Enter 搜索"))
	case searchLoading:
		sb.WriteString(s.spinner.View() + " 搜索中…")
	case searchDone:
		switch {
		case s.err != "":
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("搜索失败: " + s.err + "（再次 Enter 重试）"))
		case len(s.results) == 0:
			sb.WriteString(lipgloss.NewStyle().Faint(true).Render("无结果"))
		default:
			sb.WriteString(s.list.View())
			sb.WriteString("\n" + lipgloss.NewStyle().Faint(true).Render("↑↓ 选择 · Enter/p 播放 · a 添加到…"))
		}
	}
	return sb.String()
}

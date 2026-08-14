package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/model"
	"music-tui/queue"
)

// queuePlayMsg 队列页请求跳转播放指定下标的曲目（跳转语义：保留队列，
// 仅把当前指针移到该曲目并播放）。
type queuePlayMsg struct {
	index int
}

// queueDeleteMsg 队列页请求删除指定下标。
type queueDeleteMsg struct {
	index int
}

// queueClearMsg 队列页请求清空队列。
type queueClearMsg struct{}

// queueModeMsg 队列页请求三态循环切换播放模式（Sequential→Shuffle→RepeatOne）。
// 与首页模式按钮消息 toggleModeMsg 共用 root.cycleMode。
type queueModeMsg struct{}

// queueItem 适配 list.Item：当前标记 + 序号 + 标题 + 歌手 · 时长。
type queueItem struct {
	track   model.Track
	idx     int  // 队列内下标（1 基展示）
	current bool // 是否为当前播放曲目
}

func (i queueItem) Title() string {
	prefix := "  "
	if i.current {
		prefix = "▶ "
	}
	title := fmt.Sprintf("%s%2d. %s", prefix, i.idx+1, i.track.Title)
	if i.current {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render(title)
	}
	return title
}

func (i queueItem) Description() string {
	return i.track.Artist + " · " + formatDuration(i.track.Duration)
}

func (i queueItem) FilterValue() string { return i.track.Title + " " + i.track.Artist }

// queueModel 队列页：展示播放队列（当前曲目高亮 + 序号），支持
// 跳转播放/删除/清空/切换顺序随机。数据由 root 经 sync 推入，
// 页面自身不持有服务。
type queueModel struct {
	list    list.Model
	items   []model.Track
	current int // 当前曲目下标；-1 = 无
	mode    queue.Mode

	width, height int
}

func newQueueModel() queueModel {
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, 80, 24)
	l.Title = "播放队列"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	return queueModel{list: l, current: -1}
}

// Update 处理队列页按键：Enter 跳转播放、d 删除、c 清空、s 切换模式；
// 其余按键交给列表（↑↓ 选择等）。
func (q queueModel) Update(msg tea.Msg) (queueModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if item, ok := q.list.SelectedItem().(queueItem); ok {
				return q, emitQueuePlay(item.idx)
			}
			return q, nil
		case "d":
			if item, ok := q.list.SelectedItem().(queueItem); ok {
				return q, emitQueueDelete(item.idx)
			}
			return q, nil
		case "c":
			return q, emitQueueClear()
		case "s":
			return q, emitQueueMode()
		}
	}
	var cmd tea.Cmd
	q.list, cmd = q.list.Update(msg)
	return q, cmd
}

// sync 用队列最新状态刷新页面（root 在队列变化后调用）。
// 选中项按曲目 ID 尽量保持（列表收缩导致选中项消失时回到顶部）。
func (q queueModel) sync(qu *queue.Queue) queueModel {
	q.items = qu.Tracks()
	q.current = qu.CurrentIndex()
	q.mode = qu.Mode()
	q.list.Title = fmt.Sprintf("播放队列 (%d)", qu.Len())
	items := make([]list.Item, 0, len(q.items))
	for i, tr := range q.items {
		items = append(items, queueItem{track: tr, idx: i, current: i == q.current})
	}
	keep := ""
	keepIdx := q.list.GlobalIndex()
	if it, ok := q.list.SelectedItem().(queueItem); ok {
		keep = it.track.ID
	}
	q.list.SetItems(items)
	if keep != "" {
		for i, tr := range q.items {
			if tr.ID == keep {
				q.list.Select(i)
				return q
			}
		}
	}
	// 选中项已被删除（keep 未命中）：clamp 到邻近项，避免光标越界
	// 导致 Enter/d 静默失效（回归：TestQueueDeleteKeepsSelectionValid）。
	if keepIdx >= len(items) {
		keepIdx = len(items) - 1
	}
	if keepIdx >= 0 {
		q.list.Select(keepIdx)
	}
	return q
}

// setSize 响应窗口尺寸变化。
func (q queueModel) setSize(width, height int) queueModel {
	q.width, q.height = width, height
	q.list.SetSize(width, height-3)
	return q
}

// view 渲染队列页；空队列显示空态提示。
func (q queueModel) view() string {
	if len(q.items) == 0 {
		return lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("队列为空\n\n搜索页选中结果后按 a 加入队列，Enter 立即播放")
	}
	return q.list.View() + "\n" +
		lipgloss.NewStyle().Faint(true).Render(q.modeLabel()+" · Enter 跳转播放 · d 删除 · c 清空 · s 切换模式")
}

// modeLabel 队列页底部的模式名（完整三态文案：列表循环/随机播放/单曲循环）。
func (q queueModel) modeLabel() string {
	switch q.mode {
	case queue.Shuffle:
		return "随机播放"
	case queue.RepeatOne:
		return "单曲循环"
	default:
		return "列表循环"
	}
}

// modeName 返回模式的短名（首页队列信息区与按钮行共用，如 "3/12 · 顺序"；
// 与 home.go 原 modeShortName 统一，文案保持 顺序/随机/单曲循环）。
func modeName(m queue.Mode) string {
	switch m {
	case queue.Shuffle:
		return "随机"
	case queue.RepeatOne:
		return "单曲循环"
	default:
		return "顺序"
	}
}

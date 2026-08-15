package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
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
	line := fmt.Sprintf("%s%2d. %s", prefix, i.idx+1,
		formatTrackLine(i.track.Title, i.track.Artist, formatDuration(i.track.Duration)))
	if i.current {
		line = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render(line)
	}
	return line
}

func (i queueItem) Description() string { return "" }

func (i queueItem) FilterValue() string { return i.track.Title + " " + i.track.Artist }

// queueModel 队列页：展示播放队列（当前曲目高亮 + 序号），支持
// 跳转播放/删除/清空/切换顺序随机，/ 开启过滤（标题/歌手实时子串匹配）。
// 数据由 root 经 sync 推入，页面自身不持有服务。
type queueModel struct {
	list    list.Model
	items   []model.Track
	current int // 当前曲目下标；-1 = 无
	mode    queue.Mode

	// aiTitle/aiArtist AI 识别出的清洗后歌名/歌手：非空时队列页当前项
	// 显示它（root 在歌词结果到达时 setAITrack，切歌时经 sync 不清除、
	// 由 root 在 beginPlay 时清空）。
	aiTitle  string
	aiArtist string

	// filtering/filterInput / 过滤态：filtering 表示过滤生效（输入框
	// 确认失焦后仍保持），filterInput 为过滤词输入框（聚焦时实时过滤）。
	filtering   bool
	filterInput textinput.Model

	width, height int
}

func newQueueModel() queueModel {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false // 单行条目（▶ 序号. 标题 - 作者 · 时长）
	l := list.New(nil, delegate, 80, 24)
	l.Title = "播放队列"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	ti := textinput.New()
	ti.CharLimit = 120
	return queueModel{list: l, current: -1, filterInput: ti}
}

// typing 返回过滤输入框是否聚焦（root 让字符类全局键空格/a/q 让位；与搜索页同名方法一致）。
func (q queueModel) typing() bool { return q.filtering && q.filterInput.Focused() }

// Update 处理队列页按键：/ 开关过滤、Enter/p 跳转播放、d 删除、c 清空、
// s 切换模式；过滤输入框聚焦时字符键进过滤词（实时过滤），Enter 确认
// 失焦、Esc 退出恢复全量；其余按键交给列表（↑↓ 选择等）。
func (q queueModel) Update(msg tea.Msg) (queueModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "/":
			// 打开或重聚焦过滤输入框；已聚焦时 "/" 作为普通字符输入
			if !q.filtering {
				q.filtering = true
				q.filterInput.Focus()
				return q, nil
			}
			if !q.filterInput.Focused() {
				q.filterInput.Focus()
				return q, nil
			}
		case "esc":
			// 任何过滤态 Esc：退出过滤，清空关键词恢复完整列表
			if q.filtering {
				q.filtering = false
				q.filterInput.Blur()
				q.filterInput.SetValue("")
				return q.applyFilter(), nil
			}
		}
		if q.filtering && q.filterInput.Focused() {
			switch msg.String() {
			case "enter":
				// 确认过滤：失焦、过滤保持生效
				q.filterInput.Blur()
				return q, nil
			case "up", "down":
				// 聚焦时方向键仍操作列表（textinput 不消费方向键）
				var cmd tea.Cmd
				q.list, cmd = q.list.Update(msg)
				return q, cmd
			default:
				var cmd tea.Cmd
				q.filterInput, cmd = q.filterInput.Update(msg)
				return q.applyFilter(), cmd
			}
		}
		switch msg.String() {
		case "enter", "p":
			// p 与 Enter 同义：跳转播放选中曲目（保留队列）
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

// itemAt 构造第 i 项：当前曲目应用 AI 清洗标题（若有）。
func (q queueModel) itemAt(i int) queueItem {
	tr := q.items[i]
	if i == q.current && q.aiTitle != "" {
		tr.Title, tr.Artist = q.aiTitle, q.aiArtist
	}
	return queueItem{track: tr, idx: i, current: i == q.current}
}

// applyFilter 按当前过滤词重算列表展示（过滤中只显示命中条目，queueItem.idx
// 保留原始队列下标，播放/删除直接可用；未过滤时显示全量）。选中项尽量按
// 曲目 ID 保持，被过滤掉/已删除则 clamp 到可见末尾（回归：TestQueueDeleteKeepsSelectionValid）。
func (q queueModel) applyFilter() queueModel {
	keep := ""
	if it, ok := q.list.SelectedItem().(queueItem); ok {
		keep = it.track.ID
	}
	visible := make([]list.Item, 0, len(q.items))
	kw := q.filterInput.Value()
	for i := range q.items {
		it := q.itemAt(i)
		if q.filtering && !filterMatches(kw, it.FilterValue()) {
			continue
		}
		visible = append(visible, it)
	}
	q.list.SetItems(visible)
	if keep != "" {
		for i, it := range visible {
			if it.(queueItem).track.ID == keep {
				q.list.Select(i)
				return q
			}
		}
	}
	if len(visible) > 0 {
		if idx := q.list.Index(); idx >= len(visible) {
			q.list.Select(len(visible) - 1)
		}
	}
	return q
}

// setAITrack 应用 AI 识别结果（展示覆盖，root 在歌词结果到达时调用）。
func (q queueModel) setAITrack(title, artist string) queueModel {
	q.aiTitle, q.aiArtist = title, artist
	return q
}

// sync 用队列最新状态刷新页面（root 在队列变化后调用）。数据刷新后重放过滤
// （过滤词不变时命中集随队列变化，如删除后计数一致）。
func (q queueModel) sync(qu *queue.Queue) queueModel {
	q.items = qu.Tracks()
	q.current = qu.CurrentIndex()
	q.mode = qu.Mode()
	q.list.Title = fmt.Sprintf("播放队列 (%d)", qu.Len())
	return q.applyFilter()
}

// setSize 响应窗口尺寸变化。
func (q queueModel) setSize(width, height int) queueModel {
	q.width, q.height = width, height
	q.list.SetSize(width, height-3)
	q.filterInput.Width = width - 14
	if q.filterInput.Width < 10 {
		q.filterInput.Width = 10
	}
	return q
}

// view 渲染队列页：过滤开启时顶部为过滤行（"过滤: [输入框] (n/m)"），
// 提示行随过滤状态切换；空队列且未过滤时显示空态提示。提示行恒在页面
// 内容区最后一行。
func (q queueModel) view() string {
	if q.filtering {
		hint := "列表循环 · 输入过滤 · Enter 确认 · Esc 取消"
		if !q.filterInput.Focused() {
			hint = "列表循环 · Enter/p 跳转播放 · d 删除 · c 清空 · s 切换模式 · Esc 退出过滤"
		}
		count := fmt.Sprintf("(%d/%d)", len(q.list.VisibleItems()), len(q.items))
		filterLine := "过滤: " + q.filterInput.View() + " " + lipgloss.NewStyle().Faint(true).Render(count)
		return bottomHint(q.height, filterLine+"\n"+q.list.View(), hint)
	}
	hint := q.modeLabel() + " · Enter/p 跳转播放 · d 删除 · c 清空 · s 切换模式"
	if len(q.items) == 0 {
		content := lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("队列为空\n\n搜索页选中结果后按 a 添加到队列，Enter 立即播放")
		return bottomHint(q.height, content, hint)
	}
	return bottomHint(q.height, q.list.View(), hint)
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

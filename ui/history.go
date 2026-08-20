package ui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"music-tui/history"
	"music-tui/model"
)

// deleteEntryMsg 历史页请求删除单条记录（root 执行）。
type deleteEntryMsg struct {
	id     string
	source string
}

// clearHistoryMsg 历史页请求清空（root 执行）。
type clearHistoryMsg struct{}

// historyItem 适配 list.Item：标题 + 歌手 · 播放时间。
type historyItem struct {
	entry history.Entry
}

func (i historyItem) Title() string {
	return formatTrackLine(i.entry.Track.Title, i.entry.Track.Artist, formatPlayedAt(i.entry.PlayedAt, time.Now()))
}
func (i historyItem) Description() string { return "" }
func (i historyItem) FilterValue() string { return i.entry.Track.Title + " " + i.entry.Track.Artist }

// historyModel 历史页：列表 + 重播/删除/清空。数据由 root 经 setEntries 推入。
// / 过滤：过滤后条目复用 historyItem 结构（携带完整 entry，重播/删除零改动）。
type historyModel struct {
	list    list.Model
	entries []history.Entry

	filtering   bool
	filterInput textinput.Model

	width, height int
}

func newHistoryModel() historyModel {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false // 单行条目（标题 - 作者 · 播放时间）
	l := list.New(nil, delegate, 80, 24)
	l.Title = "最近播放"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	ti := textinput.New()
	ti.CharLimit = 120
	return historyModel{list: l, filterInput: ti}
}

// typing 返回过滤输入框是否聚焦（root 让字符类全局键空格/a/q 让位）。
func (h historyModel) typing() bool { return h.filtering && h.filterInput.Focused() }

// Update 处理历史页按键。
func (h historyModel) Update(msg tea.Msg) (historyModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "/":
			// 打开或重聚焦过滤输入框；已聚焦时 "/" 作为普通字符输入
			if !h.filtering {
				h.filtering = true
				h.filterInput.Focus()
				return h, nil
			}
			if !h.filterInput.Focused() {
				h.filterInput.Focus()
				return h, nil
			}
		case "esc":
			// 任何过滤态 Esc：退出过滤，清空关键词恢复完整列表
			if h.filtering {
				h.filtering = false
				h.filterInput.Blur()
				h.filterInput.SetValue("")
				return h.applyFilter(), nil
			}
		}
		if h.filtering && h.filterInput.Focused() {
			switch msg.String() {
			case "enter":
				// 确认过滤：失焦、过滤保持生效
				h.filterInput.Blur()
				return h, nil
			case "up", "down":
				// 聚焦时方向键仍操作列表（textinput 不消费方向键）
				var cmd tea.Cmd
				h.list, cmd = h.list.Update(msg)
				return h, cmd
			default:
				var cmd tea.Cmd
				h.filterInput, cmd = h.filterInput.Update(msg)
				return h.applyFilter(), cmd
			}
		}
		switch msg.String() {
		case "enter", "p":
			// p 与 Enter 同义：选中记录 → 播放（替换语义）
			if item, ok := h.list.SelectedItem().(historyItem); ok {
				return h, emitTrackSelected(item.entry.Track)
			}
			return h, nil
		case "d":
			if item, ok := h.list.SelectedItem().(historyItem); ok {
				return h, emitDeleteEntry(item.entry.Track.ID, item.entry.Track.Source)
			}
			return h, nil
		case "c":
			return h, emitClearHistory()
		}
	}
	var cmd tea.Cmd
	h.list, cmd = h.list.Update(msg)
	return h, cmd
}

// applyFilter 按当前过滤词重算列表展示（过滤中只显示命中条目，historyItem
// 携带完整 entry，重播/删除直接可用；未过滤时显示全量）。选中项尽量按
// 曲目 ID 保持，被过滤掉/已删除则 clamp 到可见末尾。
func (h historyModel) applyFilter() historyModel {
	keep := ""
	if it, ok := h.list.SelectedItem().(historyItem); ok {
		keep = it.entry.Track.ID
	}
	visible := make([]list.Item, 0, len(h.entries))
	kw := h.filterInput.Value()
	for _, e := range h.entries {
		it := historyItem{entry: e}
		if h.filtering && !filterMatches(kw, it.FilterValue()) {
			continue
		}
		visible = append(visible, it)
	}
	h.list.SetItems(visible)
	if keep != "" {
		for i, it := range visible {
			if it.(historyItem).entry.Track.ID == keep {
				h.list.Select(i)
				return h
			}
		}
	}
	if len(visible) > 0 {
		if idx := h.list.Index(); idx >= len(visible) {
			h.list.Select(len(visible) - 1)
		}
	}
	return h
}

// setEntries 更新列表数据（root 在加载/删除/清空后调用）。数据刷新后重放过滤。
func (h historyModel) setEntries(entries []history.Entry) historyModel {
	h.entries = entries
	h.list.Title = fmt.Sprintf("最近播放 (%d/%d)", len(entries), history.MaxEntries)
	return h.applyFilter()
}

// selectedTrack 返回当前选中的历史记录（供全局 a 键添加到播放列表）。
func (h historyModel) selectedTrack() (model.Track, bool) {
	if item, ok := h.list.SelectedItem().(historyItem); ok {
		return item.entry.Track, true
	}
	return model.Track{}, false
}

// setSize 响应窗口尺寸变化。
func (h historyModel) setSize(width, height int) historyModel {
	h.width, h.height = width, height
	h.list.SetSize(width, height-3)
	h.filterInput.SetWidth(width - 18) // 过滤行前缀 "过滤: "(6 列) + 计数 "(n/m)"(≤10 列)
	if h.filterInput.Width() < 10 {
		h.filterInput.SetWidth(10)
	}
	return h
}

// view 渲染历史页：过滤开启时顶部为过滤行，提示行随过滤状态切换；
// 空历史且未过滤时显示空态提示。提示行恒在页面内容区最后一行。
func (h historyModel) view() string {
	if h.filtering {
		hint := "输入过滤 · Enter 确认 · Esc 取消"
		if !h.filterInput.Focused() {
			hint = "Enter/p 重播 · d 删除 · c 清空 · a 添加到… · Esc 退出过滤"
		}
		count := fmt.Sprintf("(%d/%d)", len(h.list.VisibleItems()), len(h.entries))
		filterLine := "过滤: " + h.filterInput.View() + " " + lipgloss.NewStyle().Faint(true).Render(count)
		return bottomHint(h.height, filterLine+"\n"+h.list.View(), hint)
	}
	hint := "Enter/p 重播 · d 删除 · c 清空 · a 添加到…"
	if len(h.entries) == 0 {
		content := lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("暂无播放历史\n\n去搜索页播放一首歌吧")
		return bottomHint(h.height, content, hint)
	}
	return bottomHint(h.height, h.list.View(), hint)
}

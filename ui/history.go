package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/history"
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

func (i historyItem) Title() string { return i.entry.Track.Title }
func (i historyItem) Description() string {
	return i.entry.Track.Artist + " · " + formatPlayedAt(i.entry.PlayedAt, time.Now())
}
func (i historyItem) FilterValue() string { return i.entry.Track.Title + " " + i.entry.Track.Artist }

// historyModel 历史页：列表 + 重播/删除/清空。数据由 root 经 setEntries 推入。
type historyModel struct {
	list    list.Model
	entries []history.Entry

	width, height int
}

func newHistoryModel() historyModel {
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, 80, 24)
	l.Title = "最近播放"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	return historyModel{list: l}
}

// Update 处理历史页按键。
func (h historyModel) Update(msg tea.Msg) (historyModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
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

// setEntries 更新列表数据（root 在加载/删除/清空后调用）。
func (h historyModel) setEntries(entries []history.Entry) historyModel {
	h.entries = entries
	h.list.Title = fmt.Sprintf("最近播放 (%d/%d)", len(entries), history.MaxEntries)
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, historyItem{entry: e})
	}
	h.list.SetItems(items)
	return h
}

// setSize 响应窗口尺寸变化。
func (h historyModel) setSize(width, height int) historyModel {
	h.width, h.height = width, height
	h.list.SetSize(width, height-3)
	return h
}

// view 渲染历史页。
func (h historyModel) view() string {
	if len(h.entries) == 0 {
		return lipgloss.NewStyle().
			Padding(1, 0).
			Faint(true).
			Render("暂无播放历史\n\n去搜索页播放一首歌吧")
	}
	return h.list.View() + "\n" +
		lipgloss.NewStyle().Faint(true).Render("Enter 重播 · d 删除 · c 清空")
}

// 播放列表页（两级视图：概览 ↔ 详情）与全局 p 键"添加到播放列表"选择器。
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/model"
	"music-tui/playlists"
)

// ---- 消息（root 执行 store 操作） ----

// plLoadMsg 播放列表详情 Enter：把整个列表替换进队列，从选中曲开始播放。
type plLoadMsg struct {
	name  string
	index int
}

// plCreateMsg 命名输入 Enter（新建）。
type plCreateMsg struct{ name string }

// plRenameMsg 命名输入 Enter（重命名）。
type plRenameMsg struct{ oldName, newName string }

// plDeleteMsg 概览 d：删除选中列表。
type plDeleteMsg struct{ name string }

// plRemoveTrackMsg 详情 d：从列表移除第 index 首（0 基）。
type plRemoveTrackMsg struct {
	name  string
	index int
}

func emitPlLoad(name string, index int) tea.Cmd {
	return func() tea.Msg { return plLoadMsg{name: name, index: index} }
}

func emitPlCreate(name string) tea.Cmd {
	return func() tea.Msg { return plCreateMsg{name: name} }
}

func emitPlRename(oldName, newName string) tea.Cmd {
	return func() tea.Msg { return plRenameMsg{oldName: oldName, newName: newName} }
}

func emitPlDelete(name string) tea.Cmd {
	return func() tea.Msg { return plDeleteMsg{name: name} }
}

func emitPlRemoveTrack(name string, index int) tea.Cmd {
	return func() tea.Msg { return plRemoveTrackMsg{name: name, index: index} }
}

// ---- 列表条目 ----

// overviewItem 概览条目：列表名 + 歌曲数 · 创建时间。
type overviewItem struct {
	list playlists.List
}

func (i overviewItem) Title() string { return i.list.Name }
func (i overviewItem) Description() string {
	return fmt.Sprintf("%d 首 · %s", len(i.list.Tracks), formatListCreated(i.list.CreatedAt))
}
func (i overviewItem) FilterValue() string { return i.list.Name }

// formatListCreated 列表创建时间："MM-DD 创建"。
func formatListCreated(t time.Time) string { return t.Format("01-02") + " 创建" }

// plTrackItem 详情条目：序号 + 标题 + 歌手 · 时长（序号样式与队列页一致）。
type plTrackItem struct {
	track model.Track
	idx   int // 列表内下标（0 基）
}

func (i plTrackItem) Title() string { return fmt.Sprintf("%2d. %s", i.idx+1, i.track.Title) }
func (i plTrackItem) Description() string {
	return i.track.Artist + " · " + formatDuration(i.track.Duration)
}
func (i plTrackItem) FilterValue() string { return i.track.Title + " " + i.track.Artist }

// ---- 播放列表页 ----

// plMode 播放列表页视图模式。
type plMode int

const (
	plOverview plMode = iota // 概览：全部列表
	plDetail                 // 详情：某列表的歌曲
	plNaming                 // 命名输入（新建/重命名）
)

// playlistModel 播放列表页：概览 ↔ 详情 两级列表，命名输入用于新建/重命名。
// 数据由 root 经 setLists 推入，页面自身不持有服务。
type playlistModel struct {
	overview list.Model
	detail   list.Model
	input    textinput.Model
	lists    []playlists.List

	mode      plMode
	curName   string // detail 模式当前列表名
	namingOld string // 命名输入预填的旧名（重命名；空 = 新建）

	width, height int
}

func newPlaylistModel() playlistModel {
	delegate := list.NewDefaultDelegate()
	ov := list.New(nil, delegate, 80, 24)
	ov.Title = "播放列表"
	ov.SetShowHelp(false)
	ov.SetFilteringEnabled(false)
	ov.SetShowStatusBar(false)
	dt := list.New(nil, delegate, 80, 24)
	dt.Title = ""
	dt.SetShowHelp(false)
	dt.SetFilteringEnabled(false)
	dt.SetShowStatusBar(false)
	ti := textinput.New()
	ti.Placeholder = "输入列表名，Enter 确认"
	ti.CharLimit = 60
	return playlistModel{overview: ov, detail: dt, input: ti}
}

// Update 处理播放列表页按键。
// overview：Enter 进入详情、n 新建、r 重命名、d 删除；
// detail：Enter 整列表播放、a 加入队列、d 移除、Esc/← 返回概览；
// 命名输入：Enter 提交、Esc 取消；其余按键交给对应 list/textinput。
func (p playlistModel) Update(msg tea.Msg) (playlistModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch p.mode {
		case plNaming:
			switch msg.String() {
			case "enter":
				return p, p.submitNaming()
			case "esc":
				return p.exitNaming(), nil
			}
		case plDetail:
			switch msg.String() {
			case "enter":
				if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
					return p, emitPlLoad(p.curName, item.idx)
				}
				return p, nil
			case "d":
				if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
					return p, emitPlRemoveTrack(p.curName, item.idx)
				}
				return p, nil
			case "a":
				if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
					return p, emitTrackAppend(item.track)
				}
				return p, nil
			case "esc", "left":
				p.mode = plOverview
				return p, nil
			}
		default: // plOverview
			switch msg.String() {
			case "enter":
				if item, ok := p.overview.SelectedItem().(overviewItem); ok {
					return p.enterDetail(item.list), nil
				}
				return p, nil
			case "n":
				return p.beginNaming(""), nil
			case "r":
				if item, ok := p.overview.SelectedItem().(overviewItem); ok {
					return p.beginNaming(item.list.Name), nil
				}
				return p, nil
			case "d":
				if item, ok := p.overview.SelectedItem().(overviewItem); ok {
					return p, emitPlDelete(item.list.Name)
				}
				return p, nil
			}
		}
	}
	var cmd tea.Cmd
	switch p.mode {
	case plNaming:
		p.input, cmd = p.input.Update(msg)
	case plDetail:
		p.detail, cmd = p.detail.Update(msg)
	default:
		p.overview, cmd = p.overview.Update(msg)
	}
	return p, cmd
}

// setLists 用最新数据刷新页面（root 在 store 变化后调用）。
// 概览选中项按列表名尽量保持（列表收缩导致选中项消失时 clamp 到邻近项，
// 参考 queue.sync 的保持逻辑）；detail 模式同步当前列表内容，
// 当前列表已不存在（被删除/重命名）时回到概览。
func (p playlistModel) setLists(lists []playlists.List) playlistModel {
	p.lists = lists
	p.overview.Title = fmt.Sprintf("播放列表 (%d)", len(lists))
	items := make([]list.Item, 0, len(lists))
	for _, l := range lists {
		items = append(items, overviewItem{list: l})
	}
	keep := ""
	keepIdx := p.overview.GlobalIndex()
	if it, ok := p.overview.SelectedItem().(overviewItem); ok {
		keep = it.list.Name
	}
	p.overview.SetItems(items)
	restored := false
	if keep != "" {
		for i, l := range lists {
			if l.Name == keep {
				p.overview.Select(i)
				restored = true
				break
			}
		}
	}
	if !restored {
		// 选中项已被删除（keep 未命中）：clamp 到邻近项，避免光标越界
		if keepIdx >= len(items) {
			keepIdx = len(items) - 1
		}
		if keepIdx >= 0 {
			p.overview.Select(keepIdx)
		}
	}
	if p.mode == plDetail {
		var ok bool
		p, ok = p.refreshDetail()
		if !ok {
			p.mode = plOverview // 当前列表已被删除/重命名
		}
	}
	return p
}

// refreshDetail 用 p.lists 刷新详情列表（含选中项 clamp）；
// 列表不存在返回 (p, false)。
func (p playlistModel) refreshDetail() (playlistModel, bool) {
	for _, l := range p.lists {
		if l.Name == p.curName {
			p.detail.Title = l.Name
			items := make([]list.Item, 0, len(l.Tracks))
			for i, tr := range l.Tracks {
				items = append(items, plTrackItem{track: tr, idx: i})
			}
			keepIdx := p.detail.GlobalIndex()
			p.detail.SetItems(items)
			if keepIdx >= len(items) {
				keepIdx = len(items) - 1
			}
			if keepIdx >= 0 {
				p.detail.Select(keepIdx)
			}
			return p, true
		}
	}
	return p, false
}

// enterDetail 进入指定列表的详情视图。
func (p playlistModel) enterDetail(l playlists.List) playlistModel {
	p.mode = plDetail
	p.curName = l.Name
	p.detail.Title = l.Name
	items := make([]list.Item, 0, len(l.Tracks))
	for i, tr := range l.Tracks {
		items = append(items, plTrackItem{track: tr, idx: i})
	}
	p.detail.SetItems(items)
	if len(items) > 0 {
		p.detail.Select(0)
	}
	return p
}

// beginNaming 进入命名输入（old 非空 = 重命名，预填旧名）。
func (p playlistModel) beginNaming(old string) playlistModel {
	p.mode = plNaming
	p.namingOld = old
	p.input.SetValue(old)
	p.input.CursorEnd()
	p.input.Focus()
	return p
}

// exitNaming 退出命名输入回到概览。
func (p playlistModel) exitNaming() playlistModel {
	if p.mode == plNaming {
		p.mode = plOverview
		p.input.Blur()
	}
	return p
}

// submitNaming 提交命名输入：新建（namingOld 为空）或重命名。
func (p playlistModel) submitNaming() tea.Cmd {
	name := strings.TrimSpace(p.input.Value())
	if p.namingOld != "" {
		return emitPlRename(p.namingOld, name)
	}
	return emitPlCreate(name)
}

// typing 返回命名输入框是否聚焦（root 让字符类全局键 p/空格/q 让位）。
func (p playlistModel) typing() bool { return p.mode == plNaming }

// selectedTrack 详情模式且有选中项时返回（供全局 p 键添加到播放列表）。
func (p playlistModel) selectedTrack() (model.Track, bool) {
	if p.mode != plDetail {
		return model.Track{}, false
	}
	if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
		return item.track, true
	}
	return model.Track{}, false
}

// setSize 响应窗口尺寸变化。
func (p playlistModel) setSize(width, height int) playlistModel {
	p.width, p.height = width, height
	p.overview.SetSize(width, height-3)
	p.detail.SetSize(width, height-3)
	p.input.Width = width - 6
	if p.input.Width < 10 {
		p.input.Width = 10
	}
	return p
}

// view 渲染播放列表页（概览/详情/命名输入三态，底部快捷键提示 faint）。
func (p playlistModel) view() string {
	switch p.mode {
	case plNaming:
		return p.overview.View() + "\n\n" + p.input.View()
	case plDetail:
		if len(p.detail.Items()) == 0 {
			return lipgloss.NewStyle().
				Padding(1, 0).
				Faint(true).
				Render("列表为空\n\n在搜索/历史页选中歌曲后按 p 添加到播放列表")
		}
		return p.detail.View() + "\n" +
			lipgloss.NewStyle().Faint(true).Render("Enter 从选中曲播放整个列表 · a 加入队列 · d 移除 · Esc 返回")
	default:
		if len(p.lists) == 0 {
			return lipgloss.NewStyle().
				Padding(1, 0).
				Faint(true).
				Render("暂无播放列表\n\n按 n 新建播放列表")
		}
		return p.overview.View() + "\n" +
			lipgloss.NewStyle().Faint(true).Render("Enter 查看 · n 新建 · r 重命名 · d 删除")
	}
}

// ---- 全局 p 键选择器 ----

// plPickerModel 按 p 弹出的"添加到播放列表"选择器：
// 列表名 + 末尾固定"＋ 新建列表"项；Enter 直接加入，或进入命名输入新建列表。
// 选择器直接持有 store 完成加入/创建，关闭时把成功提示交给 root 展示。
type plPickerModel struct {
	pl    *playlists.Store
	list  list.Model
	input textinput.Model
	track model.Track

	naming bool   // 命名输入模式（选中"＋ 新建列表"后）
	err    string // 操作错误（红字展示，不关闭选择器、不清除输入）
	notice string // 成功提示（关闭时交给 root）
	closed bool   // 完成/取消：root 检查后关闭并刷新列表页

	width, height int
}

func newPlPicker(pl *playlists.Store, track model.Track) *plPickerModel {
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, 80, 24)
	l.Title = "添加到播放列表"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	ti := textinput.New()
	ti.Placeholder = "新列表名，Enter 创建并加入"
	ti.CharLimit = 60
	p := &plPickerModel{pl: pl, list: l, input: ti, track: track}
	p.refreshItems()
	return p
}

// refreshItems 从 store 重建列表项（末尾固定"＋ 新建列表"）。
func (p *plPickerModel) refreshItems() {
	lists := p.pl.Lists()
	items := make([]list.Item, 0, len(lists)+1)
	for _, l := range lists {
		items = append(items, pickerListItem{name: l.Name})
	}
	items = append(items, pickerNewItem{})
	p.list.SetItems(items)
}

// pickerListItem 选择器里的列表条目。
type pickerListItem struct{ name string }

func (i pickerListItem) Title() string       { return i.name }
func (i pickerListItem) Description() string { return "" }
func (i pickerListItem) FilterValue() string { return i.name }

// pickerNewItem 选择器末尾的"新建列表"入口（粉色加粗区分）。
type pickerNewItem struct{}

func (pickerNewItem) Title() string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render("＋ 新建列表")
}
func (pickerNewItem) Description() string { return "" }
func (pickerNewItem) FilterValue() string { return "新建列表" }

// Update 处理选择器按键。
// 列表选中项 Enter：加入该列表 → 关闭；"＋ 新建列表" Enter：进入命名输入；
// 命名输入 Enter：创建列表并加入当前曲目 → 关闭（失败红字展示，不清除输入）；
// Esc：命名输入返回选择，选择态直接关闭。
func (p plPickerModel) Update(msg tea.Msg) (plPickerModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		if p.naming {
			switch msg.String() {
			case "enter":
				name := strings.TrimSpace(p.input.Value())
				if name == "" {
					p.err = "列表名不能为空"
					return p, nil
				}
				if _, err := p.pl.Create(name); err != nil {
					p.err = err.Error()
					return p, nil
				}
				if err := p.pl.AddTrack(name, p.track); err != nil {
					p.err = err.Error()
					return p, nil
				}
				p.closed = true
				p.notice = "已添加到「" + name + "」"
				return p, nil
			case "esc":
				p.naming = false
				p.input.Blur()
				p.err = ""
				return p, nil
			}
			var cmd tea.Cmd
			p.input, cmd = p.input.Update(msg)
			return p, cmd
		}
		switch msg.String() {
		case "enter":
			switch it := p.list.SelectedItem().(type) {
			case pickerListItem:
				if err := p.pl.AddTrack(it.name, p.track); err != nil {
					p.err = err.Error()
					return p, nil
				}
				p.closed = true
				p.notice = "已添加到「" + it.name + "」"
				return p, nil
			case pickerNewItem:
				p.naming = true
				p.input.SetValue("")
				p.input.Focus()
				p.err = ""
				return p, nil
			}
			return p, nil
		case "esc":
			p.closed = true
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, cmd
}

// setSize 响应窗口尺寸变化。
func (p plPickerModel) setSize(width, height int) plPickerModel {
	p.width, p.height = width, height
	p.list.SetSize(width, height-3)
	p.input.Width = width - 6
	if p.input.Width < 10 {
		p.input.Width = 10
	}
	return p
}

// view 渲染选择器（全屏替换页面内容；命名输入模式附加输入框，错误红字）。
func (p plPickerModel) view() string {
	body := p.list.View()
	if p.naming {
		body += "\n\n" + p.input.View()
	}
	if p.err != "" {
		body += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("⚠ "+p.err)
	}
	body += "\n" + lipgloss.NewStyle().Faint(true).Render("↑↓ 选择 · Enter 确认 · Esc 取消")
	return body
}

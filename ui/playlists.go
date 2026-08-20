// 播放列表页（两级视图：概览 ↔ 详情）与全局 a 键“添加到”选择器。
package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"music-tui/model"
	"music-tui/playlists"
	"music-tui/ytm"
)

// ---- 消息（root 执行 store 操作） ----

// plLoadMsg 播放列表详情 Enter：把整个列表替换进队列，从选中曲开始播放。
type plLoadMsg struct {
	name  string
	index int
}

// plCreateMsg 命名输入 Enter（新建）。
type plCreateMsg struct{ name string }

// plLocalAddMsg 本地路径输入 Enter（root 校验目录并自动新建「本地-<目录名>」列表导入）。
type plLocalAddMsg struct {
	path string
}

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

func emitPlLocalAdd(path string) tea.Cmd {
	return func() tea.Msg { return plLocalAddMsg{path: path} }
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

// ---- YT Music 同步消息（页面 emit，root 编排） ----

// ytLoginMsg 登录设置提交：浏览器方式（cookies.txt/粘贴走专用消息）。
type ytLoginMsg struct {
	cfg ytm.LoginConfig
}

// ytLoginFileMsg cookies.txt 路径提交。
type ytLoginFileMsg struct {
	path string
}

// ytLoginPasteMsg 粘贴 Cookie 字符串提交。
type ytLoginPasteMsg struct {
	text string
}

// ytLogoutMsg 退出登录。
type ytLogoutMsg struct{}

// ytSyncAllMsg 概览 y：同步全部歌单。
type ytSyncAllMsg struct{}

// ytImportMsg 概览 u：导入歌单 URL。
type ytImportMsg struct {
	url string
}

// ytRefreshMsg 详情 r：刷新当前列表（仅 YT 同步列表）。
type ytRefreshMsg struct {
	listName string
}

func emitYtLogin(cfg ytm.LoginConfig) tea.Cmd {
	return func() tea.Msg { return ytLoginMsg{cfg: cfg} }
}

func emitYtLoginFile(path string) tea.Cmd {
	return func() tea.Msg { return ytLoginFileMsg{path: path} }
}

func emitYtLoginPaste(text string) tea.Cmd {
	return func() tea.Msg { return ytLoginPasteMsg{text: text} }
}

func emitYtLogout() tea.Cmd {
	return func() tea.Msg { return ytLogoutMsg{} }
}

func emitYtSyncAll() tea.Cmd {
	return func() tea.Msg { return ytSyncAllMsg{} }
}

func emitYtImport(url string) tea.Cmd {
	return func() tea.Msg { return ytImportMsg{url: url} }
}

func emitYtRefresh(listName string) tea.Cmd {
	return func() tea.Msg { return ytRefreshMsg{listName: listName} }
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

func (i plTrackItem) Title() string {
	return fmt.Sprintf("%2d. %s", i.idx+1,
		formatTrackLine(i.track.Title, i.track.Artist, formatTrackDuration(i.track.Duration)))
}
func (i plTrackItem) Description() string { return "" }
func (i plTrackItem) FilterValue() string { return i.track.Title + " " + i.track.Artist }

// ---- 播放列表页 ----

// plMode 播放列表页视图模式。
type plMode int

const (
	plOverview  plMode = iota // 概览：全部列表
	plDetail                  // 详情：某列表的歌曲
	plNaming                  // 命名输入（新建/重命名）
	plSyncSetup               // YT Music 登录设置（含二级浏览器选择与输入子层）
	plURLImport               // YT Music URL 导入输入框
	plLocalAdd                // 本地路径导入输入框（l 键）
)

// setupSub 是登录设置视图的子状态。
type setupSub int

const (
	setupMain         setupSub = iota // 登录方式菜单
	setupBrowser                      // 浏览器二级选择
	setupCookiesInput                 // cookies.txt 路径输入
	setupPasteInput                   // 粘贴 Cookie 字符串输入
)

// setupKind 是登录设置菜单项类型。
type setupKind int

const (
	setupKindBrowser setupKind = iota
	setupKindCookies
	setupKindPaste
	setupKindLogout
)

// setupItem 登录设置主菜单项。
type setupItem struct {
	label string
	desc  string
	kind  setupKind
}

func (i setupItem) Title() string       { return i.label }
func (i setupItem) Description() string { return i.desc }
func (i setupItem) FilterValue() string { return i.label }

// setupItems 返回登录方式菜单（含末尾"退出登录"项）。
func setupItems() []list.Item {
	return []list.Item{
		setupItem{label: "浏览器读取", desc: "支持 Chrome / Chromium / Brave / Edge / Vivaldi / Opera", kind: setupKindBrowser},
		setupItem{label: "cookies.txt 文件路径", desc: "填写浏览器导出的 cookies.txt 完整路径", kind: setupKindCookies},
		setupItem{label: "粘贴 Cookie 字符串", desc: "粘贴 Cookie header（name=value; ...）", kind: setupKindPaste},
		setupItem{label: "退出登录", desc: "清除已保存的 YT Music 登录状态", kind: setupKindLogout},
	}
}

// browserItem 浏览器二级选择项。
type browserItem struct {
	info ytm.BrowserInfo
}

func (i browserItem) Title() string { return i.info.Label }
func (i browserItem) Description() string {
	return "自动导出浏览器 cookie（Windows 请改用 cookies.txt）"
}
func (i browserItem) FilterValue() string { return i.info.Label }

// playlistModel 播放列表页：概览 ↔ 详情 两级列表，命名输入用于新建/重命名，
// 另有 YT Music 登录设置与 URL 导入两种输入模式。
// 数据由 root 经 setLists/setYTSyncStatus/setYTSyncs 推入，页面自身不持有服务。
type playlistModel struct {
	overview list.Model
	detail   list.Model
	setup    list.Model
	input    textinput.Model
	lists    []playlists.List

	mode      plMode
	curName   string // detail 模式当前列表名
	namingOld string // 命名输入预填的旧名（重命名；空 = 新建）
	setupSub  setupSub

	// YT Music 状态（root 推入；页面不直接持有 ytm 服务）
	ytLogin     ytm.LoginConfig
	ytSyncing   bool
	ytInvalid   bool            // 最近一次 VerifyLogin 失败（验证失败降级展示；未验证/成功为 false）
	ytSyncNames map[string]bool // 本地列表名 → 是否 YT 同步列表（详情 r 刷新提示）

	width, height int
}

func newPlaylistModel() playlistModel {
	ovDelegate := list.NewDefaultDelegate()
	dtDelegate := list.NewDefaultDelegate()
	dtDelegate.ShowDescription = false // 详情歌曲条目单行（标题 - 作者 · 时长）
	stDelegate := list.NewDefaultDelegate()
	ov := list.New(nil, ovDelegate, 80, 24)
	ov.Title = "播放列表"
	ov.SetShowHelp(false)
	ov.SetFilteringEnabled(false)
	ov.SetShowStatusBar(false)
	dt := list.New(nil, dtDelegate, 80, 24)
	dt.Title = ""
	dt.SetShowHelp(false)
	dt.SetFilteringEnabled(false)
	dt.SetShowStatusBar(false)
	st := list.New(nil, stDelegate, 80, 24)
	st.Title = ""
	st.SetShowHelp(false)
	st.SetFilteringEnabled(false)
	st.SetShowStatusBar(false)
	ti := textinput.New()
	ti.Placeholder = "输入列表名，Enter 确认"
	ti.CharLimit = 4096 // 共享输入框：URL 导入/粘贴 Cookie 需要长输入（列表名仅占小头）
	return playlistModel{overview: ov, detail: dt, setup: st, input: ti, ytSyncNames: map[string]bool{}}
}

// Update 处理播放列表页按键。
// overview：Enter 进入详情、n 新建、r 重命名、d 删除、p 播放选中列表、
//
//	s 登录设置、y 同步全部、u URL 导入（s/y/u 仅概览响应，详情不响应）；
//
// detail：Enter/p 整列表播放、d 移除、r 刷新（YT 同步列表）、
//
//	Esc/← 返回概览；
//
// 命名输入：Enter 提交、Esc 取消（字符键含 a/p 均让位输入框）；
// URL 导入：Enter 提交（空值忽略）、Esc 返回概览；
// 本地路径导入：Enter 提交（空值忽略，提交后留在输入框——失败由 root toast
//
//	提示可改路径重试，成功由 root 退出输入）、Esc 返回概览；
//
// 登录设置：主菜单/浏览器二级列表 Enter 确认、Esc 返回上一层，
//
//	输入子层 Enter 提交、Esc 返回菜单；
//
// 其余按键交给对应 list/textinput。
func (p playlistModel) Update(msg tea.Msg) (playlistModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch p.mode {
		case plNaming:
			switch msg.String() {
			case "enter":
				return p, p.submitNaming()
			case "esc":
				return p.exitNaming(), nil
			}
		case plSyncSetup:
			// Enter/Esc 按子状态处理；其余按键落在下方对应组件的 Update。
			switch p.setupSub {
			case setupMain:
				switch msg.String() {
				case "enter":
					it, ok := p.setup.SelectedItem().(setupItem)
					if !ok {
						return p, nil
					}
					switch it.kind {
					case setupKindBrowser:
						return p.enterSetupBrowser(), nil
					case setupKindCookies:
						return p.beginSetupInput(setupCookiesInput, "输入 cookies.txt 完整路径"), nil
					case setupKindPaste:
						return p.beginSetupInput(setupPasteInput, "粘贴 Cookie 字符串（name=value; ...）"), nil
					case setupKindLogout:
						return p.exitSyncSetup(), emitYtLogout()
					}
					return p, nil
				case "esc":
					return p.exitSyncSetup(), nil
				}
			case setupBrowser:
				switch msg.String() {
				case "enter":
					it, ok := p.setup.SelectedItem().(browserItem)
					if !ok {
						return p, nil
					}
					cfg := ytm.LoginConfig{Method: ytm.MethodBrowser, Browser: it.info.Name}
					return p.exitSyncSetup(), emitYtLogin(cfg)
				case "esc":
					return p.exitSetupBrowser(), nil
				}
			default: // setupCookiesInput / setupPasteInput
				switch msg.String() {
				case "enter":
					val := strings.TrimSpace(p.input.Value())
					if val == "" {
						return p, nil // 空值忽略（留在输入层）
					}
					if p.setupSub == setupCookiesInput {
						return p.exitSyncSetup(), emitYtLoginFile(val)
					}
					return p.exitSyncSetup(), emitYtLoginPaste(val)
				case "esc":
					return p.exitSetupInput(), nil
				}
			}
		case plURLImport:
			switch msg.String() {
			case "enter":
				url := strings.TrimSpace(p.input.Value())
				if url == "" {
					return p, nil // 空值忽略
				}
				return p.exitURLImport(), emitYtImport(url)
			case "esc":
				return p.exitURLImport(), nil
			}
		case plLocalAdd:
			switch msg.String() {
			case "enter":
				path := strings.TrimSpace(p.input.Value())
				if path == "" {
					return p, nil // 空值忽略（留在输入框）
				}
				// 提交后留在输入框：失败（root toastError）可直接改路径重试；
				// 成功由 root 的 plLocalAddMsg 分支调用 exitLocalAdd 退出。
				return p, emitPlLocalAdd(path)
			case "esc":
				return p.exitLocalAdd(), nil
			}
		case plDetail:
			switch msg.String() {
			case "enter", "p":
				// p 与 Enter 同义：整列表替换进队列，从选中曲开始播放
				if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
					return p, emitPlLoad(p.curName, item.idx)
				}
				return p, nil
			case "d":
				if item, ok := p.detail.SelectedItem().(plTrackItem); ok {
					return p, emitPlRemoveTrack(p.curName, item.idx)
				}
				return p, nil
			case "r":
				// 刷新当前列表：是否同步列表由 root 校验（非同步列表红字提示）
				return p, emitYtRefresh(p.curName)
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
			case "p":
				// 播放选中列表（从第一首开始）
				if item, ok := p.overview.SelectedItem().(overviewItem); ok {
					return p, emitPlLoad(item.list.Name, 0)
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
			case "s":
				return p.enterSyncSetup(), nil
			case "y":
				return p, emitYtSyncAll()
			case "u":
				return p.enterURLImport(), nil
			case "l":
				return p.beginLocalAdd(), nil
			}
		}
	}
	var cmd tea.Cmd
	switch p.mode {
	case plNaming, plURLImport, plLocalAdd:
		p.input, cmd = p.input.Update(msg)
	case plSyncSetup:
		if p.setupSub == setupCookiesInput || p.setupSub == setupPasteInput {
			p.input, cmd = p.input.Update(msg)
		} else {
			p.setup, cmd = p.setup.Update(msg)
		}
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
// 显式恢复占位文案：输入框被 YT 登录设置/URL 导入复用时改过 Placeholder。
func (p playlistModel) beginNaming(old string) playlistModel {
	p.mode = plNaming
	p.namingOld = old
	p.input.SetValue(old)
	p.input.Placeholder = "输入列表名，Enter 确认"
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

// enterSyncSetup 进入登录设置主菜单。
func (p playlistModel) enterSyncSetup() playlistModel {
	p.mode = plSyncSetup
	p.setupSub = setupMain
	p.setup.SetItems(setupItems())
	p.setup.Select(0)
	return p
}

// enterSetupBrowser 进入浏览器二级选择。
func (p playlistModel) enterSetupBrowser() playlistModel {
	p.setupSub = setupBrowser
	items := make([]list.Item, 0, len(ytm.SupportedBrowsers))
	for _, b := range ytm.SupportedBrowsers {
		items = append(items, browserItem{info: b})
	}
	p.setup.SetItems(items)
	p.setup.Select(0)
	return p
}

// exitSetupBrowser 从浏览器选择返回主菜单。
func (p playlistModel) exitSetupBrowser() playlistModel {
	if p.setupSub == setupBrowser {
		p.setupSub = setupMain
		p.setup.SetItems(setupItems())
		p.setup.Select(0)
	}
	return p
}

// beginSetupInput 进入登录输入子层（cookies.txt 路径 / 粘贴 Cookie）。
func (p playlistModel) beginSetupInput(sub setupSub, placeholder string) playlistModel {
	p.setupSub = sub
	p.input.SetValue("")
	p.input.Placeholder = placeholder
	p.input.CursorEnd()
	p.input.Focus()
	return p
}

// exitSetupInput 输入子层 Esc：返回主菜单。
func (p playlistModel) exitSetupInput() playlistModel {
	if p.setupSub == setupCookiesInput || p.setupSub == setupPasteInput {
		p.setupSub = setupMain
		p.input.Blur()
	}
	return p
}

// exitSyncSetup 退出登录设置回概览。
func (p playlistModel) exitSyncSetup() playlistModel {
	if p.mode == plSyncSetup {
		p.mode = plOverview
		p.setupSub = setupMain
		p.input.Blur()
	}
	return p
}

// enterURLImport 进入 URL 导入输入框。
func (p playlistModel) enterURLImport() playlistModel {
	p.mode = plURLImport
	p.input.SetValue("")
	p.input.Placeholder = "粘贴 YouTube Music 歌单链接，Enter 导入"
	p.input.CursorEnd()
	p.input.Focus()
	return p
}

// exitURLImport 退出 URL 导入回概览。
func (p playlistModel) exitURLImport() playlistModel {
	if p.mode == plURLImport {
		p.mode = plOverview
		p.input.Blur()
	}
	return p
}

// beginLocalAdd 进入本地路径导入输入框（l 键）。
func (p playlistModel) beginLocalAdd() playlistModel {
	p.mode = plLocalAdd
	p.input.SetValue("")
	p.input.Placeholder = "输入本地音乐目录路径，Enter 扫描"
	p.input.CursorEnd()
	p.input.Focus()
	return p
}

// exitLocalAdd 退出本地路径导入回概览。
func (p playlistModel) exitLocalAdd() playlistModel {
	if p.mode == plLocalAdd {
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

// typing 返回是否有输入框聚焦（root 让字符类全局键 p/空格/q 让位）。
// 命名输入、URL 导入、本地路径导入、登录设置的 cookies/粘贴输入子层均算聚焦。
func (p playlistModel) typing() bool {
	switch p.mode {
	case plNaming, plURLImport, plLocalAdd:
		return true
	case plSyncSetup:
		return p.setupSub == setupCookiesInput || p.setupSub == setupPasteInput
	}
	return false
}

// setYTSyncStatus 推入 YT 登录状态、同步中标记与验证失败标记（root 调用）。
func (p playlistModel) setYTSyncStatus(login ytm.LoginConfig, syncing, invalid bool) playlistModel {
	p.ytLogin = login
	p.ytSyncing = syncing
	p.ytInvalid = invalid
	return p
}

// setYTSyncs 推入 YT 同步列表名集合（root 调用；详情 r 刷新提示用）。
func (p playlistModel) setYTSyncs(entries []ytm.SyncEntry) playlistModel {
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.ListName] = true
	}
	p.ytSyncNames = names
	return p
}

// selectedTrack 详情模式且有选中项时返回（供全局 a 键添加到播放列表）。
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
	p.setup.SetSize(width, height-3)
	p.input.SetWidth(width - 6)
	if p.input.Width() < 10 {
		p.input.SetWidth(10)
	}
	return p
}

// view 渲染播放列表页（概览/详情/命名输入/登录设置/URL 导入/本地路径导入六态，
// 底部快捷键提示 faint；概览顶部渲染 YT Music 状态区，空列表时也显示）。
func (p playlistModel) view() string {
	switch p.mode {
	case plNaming:
		return p.overview.View() + "\n\n" + p.input.View()
	case plURLImport:
		return p.overview.View() + "\n\n" + p.input.View() + "\n" +
			lipgloss.NewStyle().Faint(true).Render("Enter 导入 · Esc 返回")
	case plLocalAdd:
		return p.overview.View() + "\n\n" + p.input.View() + "\n" +
			lipgloss.NewStyle().Faint(true).Render("Enter 扫描 · Esc 返回")
	case plSyncSetup:
		return p.setupView()
	case plDetail:
		hint := "Enter/p 从选中曲播放整个列表 · d 移除 · Esc 返回"
		if p.ytSyncNames[p.curName] {
			hint += " · r 刷新"
		}
		if len(p.detail.Items()) == 0 {
			content := lipgloss.NewStyle().
				Padding(1, 0).
				Faint(true).
				Render("列表为空\n\n在搜索/历史页选中歌曲后按 a 添加到播放列表")
			return bottomHint(p.height, content, hint)
		}
		return bottomHint(p.height, p.detail.View(), hint)
	default:
		hint := "Enter 查看 · p 播放 · n 新建 · r 重命名 · d 删除 · s 登录设置 · y 同步全部 · u 导入 · l 本地"
		content := p.ytStatusBlock()
		if len(p.lists) == 0 {
			content += "\n" + lipgloss.NewStyle().
				Padding(1, 0).
				Faint(true).
				Render("暂无播放列表\n\n按 n 新建播放列表")
			return bottomHint(p.height, content, hint)
		}
		return bottomHint(p.height, content+"\n"+p.overview.View(), hint)
	}
}

// ytStatusBlock 渲染概览顶部 YT Music 状态区（列表上方；空列表时也显示）。
// 未登录：YT Music · 未登录（faint：s 登录设置 · u 导入歌单链接）
// 已登录：YT Music · 已登录（faint：y 同步全部 · s 设置 · u 导入）
// 验证失败：YT Music · 已登录（验证失败）（配置仍在，但登录已确认不可用）
// 同步中：YT Music · 同步中…（无提示行）
func (p playlistModel) ytStatusBlock() string {
	status := "YT Music · 未登录"
	hint := "s 登录设置 · u 导入歌单链接"
	if p.ytSyncing {
		status = "YT Music · 同步中…"
		hint = ""
	} else if p.ytLogin.Method != ytm.MethodNone {
		status = "YT Music · 已登录"
		if p.ytInvalid {
			status = "YT Music · 已登录（验证失败）"
		}
		hint = "y 同步全部 · s 设置 · u 导入"
	}
	out := lipgloss.NewStyle().Bold(true).Render(status)
	if hint != "" {
		out += "\n" + lipgloss.NewStyle().Faint(true).Render(hint)
	}
	return out
}

// setupView 渲染登录设置视图：标题 + 当前状态行 + 菜单/浏览器列表
// （输入子层附加输入框）。setup 列表高度按子状态动态调整，
// 保证提示行恒在页面内容区最后一行（主菜单 h-4、输入子层 h-6）。
func (p playlistModel) setupView() string {
	title := "YT Music 登录设置"
	if p.setupSub == setupBrowser {
		title = "选择浏览器"
	}
	status := "未登录"
	if p.ytLogin.Method != ytm.MethodNone {
		status = "已登录 · " + p.ytLoginMethodLabel() + " · " + p.ytLogin.UpdatedAt.Format("01-02 15:04")
		if p.ytInvalid {
			status = "已登录（验证失败） · " + p.ytLoginMethodLabel() + " · " + p.ytLogin.UpdatedAt.Format("01-02 15:04")
		}
	}
	head := lipgloss.NewStyle().Bold(true).Render(title) + "\n" +
		lipgloss.NewStyle().Faint(true).Render("当前状态："+status)
	extra := 0
	if p.setupSub == setupCookiesInput || p.setupSub == setupPasteInput {
		extra = 2
	}
	// 列表高度按子状态动态计算（主菜单 h-4、输入子层 h-6）。setupView 是值
	// 接收者，SetSize 只改副本——但每次渲染都重算，实际生效高度恒等于渲染
	// 用高度，故安全。未收到 WindowSizeMsg（p.height=0）或窗口极小
	// （p.height < 4+extra）时保持初始高度，避免负高度压坏列表。
	if p.height >= 4+extra {
		p.setup.SetSize(p.width, p.height-4-extra)
	}
	body := p.setup.View()
	if extra > 0 {
		body += "\n\n" + p.input.View()
	}
	return bottomHint(p.height, head+"\n\n"+body, "↑↓ 选择 · Enter 确认 · Esc 返回")
}

// ytLoginMethodLabel 返回当前登录方式的展示名（浏览器方式显示浏览器名）。
func (p playlistModel) ytLoginMethodLabel() string {
	if p.ytLogin.Method == ytm.MethodBrowser {
		for _, b := range ytm.SupportedBrowsers {
			if b.Name == p.ytLogin.Browser {
				return b.Label
			}
		}
	}
	return p.ytLogin.Method.String()
}

// ---- 全局 a 键选择器 ----

// plPickerModel 按 a 弹出的"添加到"选择器：
// 首项固定"当前播放队列"（Enter 直接追加到队尾）+ 各列表名 + 末尾固定
// "＋ 新建列表"项；Enter 直接加入，或进入命名输入新建列表。
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
	l.Title = "添加到"
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

// refreshItems 从 store 重建列表项：首项固定"▶ 下一首播放"（默认选中），
// 第二项"▶ 当前播放队列"（追加到队尾），中间各播放列表，末尾固定"＋ 新建列表"。
func (p *plPickerModel) refreshItems() {
	lists := p.pl.Lists()
	items := make([]list.Item, 0, len(lists)+3)
	items = append(items, pickerQueueNextItem{})
	items = append(items, pickerQueueItem{})
	for _, l := range lists {
		items = append(items, pickerListItem{name: l.Name})
	}
	items = append(items, pickerNewItem{})
	p.list.SetItems(items)
}

// pickerQueueNextItem 选择器首项：插入到当前曲之后（下一首播放，固定第一项，默认选中）。
// 样式加粗与末尾粉色新建项区分。
type pickerQueueNextItem struct{}

func (pickerQueueNextItem) Title() string {
	return lipgloss.NewStyle().Bold(true).Render("▶ 下一首播放")
}
func (pickerQueueNextItem) Description() string { return "插入到当前曲之后" }
func (pickerQueueNextItem) FilterValue() string { return "下一首播放" }

// pickerQueueItem 选择器第二项：追加到当前播放队列（固定第二项）。
// 样式加粗与末尾粉色新建项区分。
type pickerQueueItem struct{}

func (pickerQueueItem) Title() string {
	return lipgloss.NewStyle().Bold(true).Render("▶ 当前播放队列")
}
func (pickerQueueItem) Description() string { return "追加到队尾" }
func (pickerQueueItem) FilterValue() string { return "当前播放队列" }

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
	if msg, ok := msg.(tea.KeyPressMsg); ok {
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
			case pickerQueueNextItem:
				// 插入到当前曲之后（下一首播放）：走全局 trackInsertNextMsg，
				// 不设 notice（插入无成功 toast，与追加一致）。
				p.closed = true
				return p, emitTrackInsertNext(p.track)
			case pickerQueueItem:
				// 追加到当前播放队列：走全局 trackAppendMsg（root 已有处理），
				// 不设 notice（追加无成功 toast，与搜索/历史页直加队列一致）。
				p.closed = true
				return p, emitTrackAppend(p.track)
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
	p.input.SetWidth(width - 6)
	if p.input.Width() < 10 {
		p.input.SetWidth(10)
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

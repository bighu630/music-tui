package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/model"
	"music-tui/player"
)

// ---- 播放列表页：空态与两级视图 ----

// 空态渲染：无任何列表时显示提示与新建引导。
func TestPlaylistsEmptyView(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.current != pagePlaylists {
		t.Fatalf("按 3 后 current = %v, want pagePlaylists", m.current)
	}
	got := m.plPage.view()
	if !strings.Contains(got, "暂无播放列表") || !strings.Contains(got, "按 n 新建播放列表") {
		t.Errorf("空态 view 应提示新建, got %q", got)
	}
}

// 空列表详情：进入空列表显示"列表为空"引导。
func TestPlaylistDetailEmptyState(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("空歌单"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 进入详情
	if m.plPage.mode != plDetail {
		t.Fatalf("mode = %v, want plDetail", m.plPage.mode)
	}
	got := m.plPage.view()
	if !strings.Contains(got, "列表为空") || !strings.Contains(got, "按 p 添加到播放列表") {
		t.Errorf("空列表详情应提示, got %q", got)
	}
}

// 概览条目：列表名 + "N 首 · MM-DD 创建"。
func TestPlaylistOverviewItem(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("歌单A"); err != nil {
		t.Fatal(err)
	}
	m.pl.AddTrack("歌单A", testTrack("t1"))
	m.plPage = m.plPage.setLists(m.pl.Lists())
	item, ok := m.plPage.overview.SelectedItem().(overviewItem)
	if !ok {
		t.Fatal("概览无选中项")
	}
	if item.Title() != "歌单A" {
		t.Errorf("Title = %q", item.Title())
	}
	if !strings.Contains(item.Description(), "1 首") || !strings.Contains(item.Description(), "创建") {
		t.Errorf("Description = %q, want 含“1 首”与创建时间", item.Description())
	}
}

// n 新建：命名输入 → Enter → plCreateMsg → store 持久化，命名模式退出。
func TestPlaylistCreateFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !m.plPage.typing() {
		t.Fatal("按 n 后应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("我的歌单")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var pc plCreateMsg
	for _, msg := range execCmds(cmd) {
		if cm, ok := msg.(plCreateMsg); ok {
			pc = cm
		}
	}
	if pc.name != "我的歌单" {
		t.Fatalf("plCreateMsg.name = %q, want 我的歌单", pc.name)
	}
	m, _ = update(m, pc)
	lists := m.pl.Lists()
	if len(lists) != 1 || lists[0].Name != "我的歌单" {
		t.Fatalf("store 列表 = %+v, want 1 个「我的歌单」", lists)
	}
	if m.plPage.typing() {
		t.Error("创建成功后应退出命名输入")
	}
	if m.plPage.overview.Title != "播放列表 (1)" {
		t.Errorf("概览标题 = %q, want 播放列表 (1)", m.plPage.overview.Title)
	}
}

// r 重命名：预填旧名，修改后 Enter → plRenameMsg → store 生效。
func TestPlaylistRenameFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("旧名字"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !m.plPage.typing() {
		t.Fatal("按 r 后应进入命名输入模式")
	}
	if got := m.plPage.input.Value(); got != "旧名字" {
		t.Fatalf("重命名应预填旧名, input = %q", got)
	}
	// 删掉旧名后输入新名
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("新名字")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var pr plRenameMsg
	for _, msg := range execCmds(cmd) {
		if rm, ok := msg.(plRenameMsg); ok {
			pr = rm
		}
	}
	if pr.oldName != "旧名字" || pr.newName != "新名字" {
		t.Fatalf("plRenameMsg = %+v, want 旧名字→新名字", pr)
	}
	m, _ = update(m, pr)
	lists := m.pl.Lists()
	if len(lists) != 1 || lists[0].Name != "新名字" {
		t.Fatalf("重命名后 store = %+v", lists)
	}
	if m.plPage.typing() {
		t.Error("重命名成功后应退出命名输入")
	}
}

// 概览 d 删除：plDeleteMsg → store 删除，空态回归。
func TestPlaylistDeleteFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("要删的"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var pd plDeleteMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(plDeleteMsg); ok {
			pd = dm
		}
	}
	if pd.name != "要删的" {
		t.Fatalf("plDeleteMsg.name = %q", pd.name)
	}
	m, _ = update(m, pd)
	if len(m.pl.Lists()) != 0 {
		t.Errorf("删除后 store 应空, %+v", m.pl.Lists())
	}
	if got := m.plPage.view(); !strings.Contains(got, "暂无播放列表") {
		t.Errorf("删除后应回空态, got %q", got)
	}
}

// setLists 选中项按名称保持（内容变化不丢光标）。
func TestPlaylistSetListsKeepsSelection(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("A"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pl.Create("B"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中 B
	if it, ok := m.plPage.overview.SelectedItem().(overviewItem); !ok || it.list.Name != "B" {
		t.Fatalf("选中 = %+v, want B", it)
	}
	// 给 A 加歌触发 setLists：选中项应保持在 B
	if err := m.pl.AddTrack("A", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	if it, ok := m.plPage.overview.SelectedItem().(overviewItem); !ok || it.list.Name != "B" {
		t.Errorf("内容变化后选中应保持 B, got %+v", it)
	}
}

// 详情页：a 加入队列（trackAppendMsg 回灌后 queue.Len 验证）、d 移除歌曲。
func TestPlaylistDetailRemoveAndAppend(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("歌单A"); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("歌单A", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("歌单A", testTrack("t2")); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 进入详情
	if m.plPage.mode != plDetail {
		t.Fatalf("mode = %v, want plDetail", m.plPage.mode)
	}
	if got := m.plPage.view(); !strings.Contains(got, "测试歌曲 t1") {
		t.Errorf("详情应显示歌曲, got %q", got)
	}

	// a 加入队列（默认选中 t1）
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	var ta trackAppendMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(trackAppendMsg); ok {
			ta = am
		}
	}
	if ta.track.ID != "t1" {
		t.Fatalf("trackAppendMsg.track = %s, want t1", ta.track.ID)
	}
	m, _ = update(m, ta)
	if m.queue.Len() != 1 || fp.playCount() != 0 {
		t.Errorf("a 加入队列应只入队不播放: Len=%d playCount=%d", m.queue.Len(), fp.playCount())
	}

	// d 移除选中歌曲（t1）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	var pr plRemoveTrackMsg
	for _, msg := range execCmds(cmd) {
		if rm, ok := msg.(plRemoveTrackMsg); ok {
			pr = rm
		}
	}
	if pr.name != "歌单A" || pr.index != 0 {
		t.Fatalf("plRemoveTrackMsg = %+v, want 歌单A/0", pr)
	}
	m, _ = update(m, pr)
	trs := m.pl.Tracks("歌单A")
	if len(trs) != 1 || trs[0].ID != "t2" {
		t.Fatalf("移除后列表 = %+v, want [t2]", trs)
	}
	// 详情视图同步刷新（仍显示 t2）
	got := m.plPage.view()
	if !strings.Contains(got, "测试歌曲 t2") || strings.Contains(got, "测试歌曲 t1") {
		t.Errorf("移除后详情未同步, got %q", got)
	}
}

// 详情 Enter → plLoadMsg：整列表替换队列，从选中曲开始播放，回首页。
func TestPlaylistDetailEnterPlaysList(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("歌单A"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := m.pl.AddTrack("歌单A", testTrack(id)); err != nil {
			t.Fatal(err)
		}
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 进入详情
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})  // 选中 t2（下标 1）
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var pl plLoadMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(plLoadMsg); ok {
			pl = lm
		}
	}
	if pl.name != "歌单A" || pl.index != 1 {
		t.Fatalf("plLoadMsg = %+v, want 歌单A/1", pl)
	}
	m, _ = update(m, pl)
	if got := idsOf(m.queue.Tracks()); len(got) != 3 || got[0] != "t1" || got[1] != "t2" || got[2] != "t3" {
		t.Fatalf("队列 = %v, want [t1 t2 t3]", got)
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("play = %d %q, want 1 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" || !m.state.Playing {
		t.Errorf("state = %+v, want t2 播放中", m.state)
	}
}

// 命名输入聚焦时 p/空格 是输入字符（root 让位，同搜索输入框模式）。
func TestNamingInputConsumesGlobalKeys(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}) // p 应输入而非打开选择器
	if m.plPicker != nil {
		t.Fatal("命名输入聚焦时 p 不应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // 空格应输入而非切换播放
	if got := m.plPage.input.Value(); got != "p " {
		t.Errorf("input = %q, want %q", got, "p ")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}) // q 应输入而非退出程序
	if got := m.plPage.input.Value(); got != "p q" {
		t.Errorf("input = %q, want %q", got, "p q")
	}
	if m.plPicker != nil {
		t.Fatal("q 不应打开选择器")
	}
	if !m.plPage.typing() {
		t.Error("q 输入后应仍在命名输入模式")
	}
}

// ---- 全局 p 键选择器 ----

// 搜索页选中歌曲按 p：picker 出现 → Enter 选择列表 → AddTrack 生效 →
// notice 显示 → picker 关闭 → 列表页刷新。
func TestPickTrackFromSearchAddsToPlaylist(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, fp, fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker == nil {
		t.Fatal("按 p 应打开选择器")
	}
	if !strings.Contains(stripANSI(m.plPicker.view()), "添加到播放列表") {
		t.Errorf("选择器标题缺失: %q", m.plPicker.view())
	}
	if m.plPicker.naming {
		t.Fatal("选择器初始不应在命名输入模式")
	}

	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("选择器加入不应产生 cmd, got %v", cmd)
	}
	if m.plPicker != nil {
		t.Fatal("Enter 后选择器应关闭")
	}
	trs := m.pl.Tracks("收藏")
	if len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("AddTrack 未生效: %+v", trs)
	}
	if m.notice != "已添加到「收藏」" {
		t.Errorf("notice = %q, want 已添加到「收藏」", m.notice)
	}
	if fp.playCount() != 0 {
		t.Errorf("加入播放列表不应触发播放, playCount = %d", fp.playCount())
	}
	// 列表页已刷新（picker 关闭后 setLists）
	if m.plPage.overview.Title != "播放列表 (1)" {
		t.Errorf("picker 关闭后列表页未刷新, Title = %q", m.plPage.overview.Title)
	}
	// View 渲染绿色成功横幅
	if !strings.Contains(m.View(), lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✔ 已添加到「收藏」")) {
		t.Error("View 应渲染绿色成功横幅")
	}
}

// TestPickerOpenStillProcessesPlayerEvents 选择器打开时 playerEventMsg 照常处理：
// picker 只是输入模态（拦截按键/鼠标），不拦截播放事件——TrackEnded 仍自动连播。
func TestPickerOpenStillProcessesPlayerEvents(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa, nil)
	// 队列 2 首曲：播放第一首 + 追加第二首
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t1").URL {
		t.Fatalf("前置播放失败: playCount=%d lastPlayed=%q", fp.playCount(), fp.lastPlayed())
	}
	// 搜索页流程选中歌曲按 p 打开选择器
	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker == nil {
		t.Fatal("按 p 应打开选择器")
	}
	// 选择器打开时 t1 播完 → 仍应连播 t2（picker 不拦截播放事件）
	m, _ = update(m, playerEventMsg{ev: player.TrackEndedEvent{}})
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("picker 打开时连播失败: playCount=%d lastPlayed=%q, want 2 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.plPicker == nil {
		t.Fatal("playerEventMsg 处理后选择器应保持打开")
	}
	if m.queue.CurrentIndex() != 1 {
		t.Errorf("连播后 CurrentIndex = %d, want 1", m.queue.CurrentIndex())
	}
}

// 选择器“＋ 新建列表”：输入名 → Enter → 列表创建且歌曲已加入。
func TestPickerCreateNewListFlow(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker == nil {
		t.Fatal("按 p 应打开选择器")
	}
	// 无既有列表：“＋ 新建列表”是唯一项，直接 Enter
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("Enter 后应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("新歌单")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker != nil {
		t.Fatal("创建成功后选择器应关闭")
	}
	lists := m.pl.Lists()
	if len(lists) != 1 || lists[0].Name != "新歌单" {
		t.Fatalf("创建失败: %+v", lists)
	}
	trs := m.pl.Tracks("新歌单")
	if len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("新列表应含刚添加的曲目: %+v", trs)
	}
	if m.notice != "已添加到「新歌单」" {
		t.Errorf("notice = %q", m.notice)
	}
}

// 选择器新建重名列表：红字错误展示，输入保留，选择器不关闭。
func TestPickerCreateDuplicateShowsError(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 选中“＋ 新建列表”
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("收藏")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("创建失败后选择器应保持命名输入")
	}
	if m.plPicker.err == "" {
		t.Error("重名创建应显示错误")
	}
	if got := m.plPicker.input.Value(); got != "收藏" {
		t.Errorf("失败后输入应保留, got %q", got)
	}
	if len(m.pl.Lists()) != 1 {
		t.Errorf("失败不应创建列表: %+v", m.pl.Lists())
	}
}

// 选择器 Esc：命名输入内返回选择态，选择态关闭且无任何副作用。
func TestPickerEscCancels(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker == nil {
		t.Fatal("按 p 应打开选择器")
	}
	// 下移到“＋ 新建列表”→ Enter 进入命名输入
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("废名")})
	// 命名输入内 Esc：回到选择态（选择器不关闭）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPicker == nil || m.plPicker.naming {
		t.Fatal("命名输入 Esc 应返回选择态")
	}
	// 选择态 Esc：关闭选择器
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPicker != nil {
		t.Fatal("Esc 应关闭选择器")
	}
	if len(m.pl.Lists()) != 1 {
		t.Errorf("取消不应创建列表: %+v", m.pl.Lists())
	}
	if trs := m.pl.Tracks("收藏"); len(trs) != 0 {
		t.Errorf("取消不应加入曲目: %+v", trs)
	}
	if m.notice != "" {
		t.Errorf("取消不应有成功提示, notice = %q", m.notice)
	}
}

// 首页无选中歌曲：按 p 提示错误，不打开选择器。
func TestPickWithoutSelectionShowsError(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker != nil {
		t.Error("无选中歌曲不应打开选择器")
	}
	if !strings.Contains(m.lastError, "当前没有可添加的歌曲") {
		t.Errorf("lastError = %q", m.lastError)
	}
}

// 搜索输入框聚焦时 p 是输入字符（不打开选择器）。
func TestPickKeyTypesWhenSearchInputFocused(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ppp")})
	if m.plPicker != nil {
		t.Error("输入框聚焦时 p 不应打开选择器")
	}
	if got := m.searchPage.input.Value(); got != "ppp" {
		t.Errorf("input = %q, want ppp", got)
	}
}

// 历史页选中记录按 p：picker 出现并可直接加入。
func TestPickTrackFromHistory(t *testing.T) {
	fa := &fakeSearchAdapter{}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	if err := m.history.Add(testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")}) // 历史页
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker == nil {
		t.Fatal("历史页选中记录按 p 应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPicker != nil {
		t.Fatal("Enter 后选择器应关闭")
	}
	if trs := m.pl.Tracks("收藏"); len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("历史曲目未加入: %+v", trs)
	}
}

// ---- 尺寸与提示 ----

// WindowSizeMsg 应下发到播放列表页与选择器。
func TestPlaylistsPageReceivesWindowSize(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.plPage.width != 100 || m.plPage.height != 38 {
		t.Errorf("plPage 尺寸 = %dx%d, want 100x38（高度减 Tab 栏 + 分隔线 2 行）",
			m.plPage.width, m.plPage.height)
	}

	// 选择器打开时同样接收尺寸
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m2 := newTestModel(t, newFakePlayer(), fa, nil)
	m2 = searchAndPick(t, m2, fa)
	m2, _ = update(m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m2, _ = update(m2, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m2.plPicker.width != 100 || m2.plPicker.height != 38 {
		t.Errorf("picker 尺寸 = %dx%d, want 100x38", m2.plPicker.width, m2.plPicker.height)
	}
}

// 成功提示在下一次按键分发时清除。
func TestNoticeClearedOnNextKey(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.notice == "" {
		t.Fatal("前置失败: 应有成功提示")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.notice != "" {
		t.Errorf("新按键分发应清除 notice, got %q", m.notice)
	}
}

package ui

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"music-tui/model"
	"music-tui/player"
	"music-tui/ytm"
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

// ---- YT Music 同步：状态区 / 登录设置 / URL 导入 / 同步全部 / 刷新 ----

// 概览顶部状态区三态：未登录 / 已登录 / 同步中；空列表时也显示。
func TestYTStatusLineStates(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})

	// 未登录 + 空列表：状态区与空态提示同时显示
	got := stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 未登录") {
		t.Errorf("未登录状态行缺失: %q", got)
	}
	if !strings.Contains(got, "s 登录设置 · u 导入歌单链接") {
		t.Errorf("未登录提示行缺失: %q", got)
	}
	if !strings.Contains(got, "暂无播放列表") {
		t.Errorf("空列表提示应保留: %q", got)
	}

	// 已登录（直接 seed store 并同步状态）
	if _, err := env.store.SetPastedLogin("SAPISID=x"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	m = env.m
	got = stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 已登录") {
		t.Errorf("已登录状态行缺失: %q", got)
	}
	if !strings.Contains(got, "y 同步全部 · s 设置 · u 导入") {
		t.Errorf("已登录提示行缺失: %q", got)
	}

	// 同步中（三态最高优先级）
	m.plPage = m.plPage.setYTSyncStatus(env.store.Login(), true)
	got = stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 同步中…") {
		t.Errorf("同步中状态行缺失: %q", got)
	}
}

// s 登录设置：主菜单四项 → 浏览器二级列表 → Esc 逐层返回概览。
func TestYTSyncSetupBrowserFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m.plPage.mode != plSyncSetup || m.plPage.setupSub != setupMain {
		t.Fatalf("s 后 mode=%v sub=%v, want plSyncSetup/setupMain", m.plPage.mode, m.plPage.setupSub)
	}
	got := stripANSI(m.plPage.view())
	for _, want := range []string{"YT Music 登录设置", "浏览器读取", "cookies.txt 文件路径", "粘贴 Cookie 字符串", "退出登录"} {
		if !strings.Contains(got, want) {
			t.Errorf("设置菜单应含 %q: %q", want, got)
		}
	}

	// Enter 浏览器读取 → 二级浏览器列表
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPage.setupSub != setupBrowser {
		t.Fatalf("Enter 后 sub=%v, want setupBrowser", m.plPage.setupSub)
	}
	got = stripANSI(m.plPage.view())
	if !strings.Contains(got, "Google Chrome") || !strings.Contains(got, "Chromium") || !strings.Contains(got, "Opera") {
		t.Errorf("浏览器列表应含全部支持浏览器: %q", got)
	}

	// Esc → 主菜单；再 Esc → 概览
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPage.setupSub != setupMain {
		t.Fatalf("Esc 后 sub=%v, want setupMain", m.plPage.setupSub)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPage.mode != plOverview {
		t.Fatalf("Esc 后 mode=%v, want plOverview", m.plPage.mode)
	}
}

// 浏览器选择 Enter → emit ytLoginMsg{Browser} → root 保存配置并异步验证。
// （不执行 verify cmd：浏览器方式会触碰真实浏览器 cookie 配置，仅断言编排。）
func TestYTBrowserLoginEmitsAndSaves(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 浏览器读取
	if m.plPage.setupSub != setupBrowser {
		t.Fatal("应进入浏览器二级列表")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 默认选中 Google Chrome
	var lg ytLoginMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(ytLoginMsg); ok {
			lg = lm
		}
	}
	if lg.cfg.Method != ytm.MethodBrowser || lg.cfg.Browser != "chrome" {
		t.Fatalf("ytLoginMsg = %+v, want MethodBrowser/chrome", lg)
	}
	// 页面提交后退出设置回概览
	if m.plPage.mode != plOverview {
		t.Fatalf("提交后 mode=%v, want plOverview", m.plPage.mode)
	}
	m, cmd = update(m, lg)
	if m.notice != "已保存登录配置，验证中…" {
		t.Errorf("notice = %q, want 已保存登录配置，验证中…", m.notice)
	}
	if m.ytLogin.Method != ytm.MethodBrowser || m.ytLogin.Browser != "chrome" {
		t.Errorf("ytLogin = %+v, want MethodBrowser/chrome", m.ytLogin)
	}
	if cmd == nil {
		t.Error("保存配置后应返回异步验证 cmd")
	}
}

// 设置输入子层：cookies.txt 路径输入流程 → ytLoginFileMsg → 异步验证成功。
func TestYTCookiesFileLoginFlow(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	cookiesFile := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(cookiesFile,
		[]byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\ttest-sap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // 浏览器读取 → cookies.txt 文件路径
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPage.setupSub != setupCookiesInput || !m.plPage.typing() {
		t.Fatalf("应进入 cookies 路径输入: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "输入 cookies.txt 完整路径") {
		t.Error("输入框占位应为 cookies.txt 路径提示")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(cookiesFile)})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var lf ytLoginFileMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(ytLoginFileMsg); ok {
			lf = lm
		}
	}
	if lf.path != cookiesFile {
		t.Fatalf("ytLoginFileMsg.path = %q, want %q", lf.path, cookiesFile)
	}
	if m.plPage.mode != plOverview {
		t.Fatalf("提交后应回概览, mode=%v", m.plPage.mode)
	}

	// root：保存配置 → 验证中 notice → 异步验证成功
	m, cmd = update(m, lf)
	if m.notice != "已保存登录配置，验证中…" {
		t.Errorf("notice = %q", m.notice)
	}
	var vd ytVerifyDoneMsg
	for _, msg := range execCmds(cmd) {
		if vm, ok := msg.(ytVerifyDoneMsg); ok {
			vd = vm
		}
	}
	m, _ = update(m, vd)
	if m.notice != "YT Music 登录有效" {
		t.Errorf("notice = %q, want YT Music 登录有效", m.notice)
	}
	if m.ytLogin.Method != ytm.MethodCookiesFile || m.ytLogin.CookiesPath != cookiesFile {
		t.Errorf("ytLogin = %+v", m.ytLogin)
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "YT Music · 已登录") {
		t.Error("验证成功后概览应显示已登录")
	}
}

// cookies.txt 路径不可读：root 直接报错，不保存配置。
func TestYTCookiesFileUnreadable(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/nonexistent/cookies.txt")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var lf ytLoginFileMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(ytLoginFileMsg); ok {
			lf = lm
		}
	}
	m, _ = update(m, lf)
	if !strings.Contains(m.lastError, "cookies.txt 不可读") {
		t.Errorf("lastError = %q, want 含 cookies.txt 不可读", m.lastError)
	}
	if m.ytLogin.Method != ytm.MethodNone {
		t.Errorf("不可读路径不应保存配置: %+v", m.ytLogin)
	}
}

// 粘贴 Cookie 流程：输入 → ytLoginPasteMsg → 落盘 cookies 文件 + 配置 → 验证成功。
func TestYTPasteLoginFlow(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // → 粘贴 Cookie 字符串
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.plPage.setupSub != setupPasteInput || !m.plPage.typing() {
		t.Fatalf("应进入粘贴输入: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "粘贴 Cookie 字符串") {
		t.Error("输入框占位应为粘贴提示")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("SAPISID=abc; __Secure-3PAPISID=xyz")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var lp ytLoginPasteMsg
	for _, msg := range execCmds(cmd) {
		if pm, ok := msg.(ytLoginPasteMsg); ok {
			lp = pm
		}
	}
	if lp.text != "SAPISID=abc; __Secure-3PAPISID=xyz" {
		t.Fatalf("ytLoginPasteMsg.text = %q", lp.text)
	}

	m, cmd = update(m, lp)
	if m.notice != "已保存登录配置，验证中…" {
		t.Errorf("notice = %q", m.notice)
	}
	if m.ytLogin.Method != ytm.MethodPasted {
		t.Fatalf("ytLogin = %+v, want MethodPasted", m.ytLogin)
	}
	// cookies 文件已落盘（0600，含粘贴的 cookie）
	data, err := os.ReadFile(m.ytLogin.CookiesPath)
	if err != nil {
		t.Fatalf("cookies 文件应已落盘: %v", err)
	}
	if !strings.Contains(string(data), "SAPISID") {
		t.Errorf("cookies 文件应含粘贴内容: %q", string(data))
	}
	if fi, err := os.Stat(m.ytLogin.CookiesPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("cookies 文件权限应 0600: %v %v", fi, err)
	}

	var vd ytVerifyDoneMsg
	for _, msg := range execCmds(cmd) {
		if vm, ok := msg.(ytVerifyDoneMsg); ok {
			vd = vm
		}
	}
	m, _ = update(m, vd)
	if m.notice != "YT Music 登录有效" {
		t.Errorf("notice = %q, want YT Music 登录有效", m.notice)
	}
}

// 粘贴验证失败：HTTP 403（失效）与 logged_in=0（未登录）映射为友好文案。
func TestYTPasteLoginVerifyFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   ytRoundTripper
		want string
	}{
		{"session-invalid", ytRoundTripper{code: 403, body: ""}, "登录已失效，请重新导出 cookie"},
		{"not-logged-in", ytRoundTripper{code: 200, body: ytBrowseLoggedOut}, "登录无效，请重新导出 cookie"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
			env.client.SetHTTPClient(&http.Client{Transport: tc.rt})
			m := env.m
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
			for i := 0; i < 2; i++ {
				m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("SAPISID=bad")})
			m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
			var lp ytLoginPasteMsg
			for _, msg := range execCmds(cmd) {
				if pm, ok := msg.(ytLoginPasteMsg); ok {
					lp = pm
				}
			}
			m, cmd = update(m, lp)
			var vd ytVerifyDoneMsg
			for _, msg := range execCmds(cmd) {
				if vm, ok := msg.(ytVerifyDoneMsg); ok {
					vd = vm
				}
			}
			m, _ = update(m, vd)
			if m.lastError != tc.want {
				t.Errorf("lastError = %q, want %q", m.lastError, tc.want)
			}
			if m.notice != "" {
				t.Errorf("失败后不应有 notice: %q", m.notice)
			}
			// 无论成败刷新页面登录状态
			if !strings.Contains(stripANSI(m.plPage.view()), "YT Music · 已登录") {
				t.Error("验证失败后概览仍应显示已登录（配置已保存）")
			}
		})
	}
}

// ytVerifyErrorText 错误映射单测。
func TestYTVerifyErrorText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ytm.ErrNotLoggedIn, "登录无效，请重新导出 cookie"},
		{ytm.ErrSessionInvalid, "登录已失效，请重新导出 cookie"},
		{ytm.ErrNoLogin, "未配置登录"},
		{errors.New("网络错误"), "网络错误"},
	}
	for _, tc := range cases {
		if got := ytVerifyErrorText(tc.err); got != tc.want {
			t.Errorf("ytVerifyErrorText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// 设置输入子层 Esc：返回主菜单而非直接退出设置。
func TestYTSetupInputEscBackToMenu(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown}) // cookies.txt
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("随便")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPage.setupSub != setupMain || m.plPage.typing() {
		t.Fatalf("Esc 后应回主菜单且输入失焦: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.plPage.mode != plOverview {
		t.Fatalf("主菜单 Esc 后应回概览: mode=%v", m.plPage.mode)
	}
}

// 退出登录：设置列表末尾项 → ytLogoutMsg → ClearLogin + 状态复位 + notice。
func TestYTLogoutFlow(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=x"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 退出登录
	var lo ytLogoutMsg
	for _, msg := range execCmds(cmd) {
		if om, ok := msg.(ytLogoutMsg); ok {
			lo = om
		}
	}
	m, _ = update(m, lo)
	if m.notice != "已退出 YT Music 登录" {
		t.Errorf("notice = %q", m.notice)
	}
	if m.ytLogin.Method != ytm.MethodNone {
		t.Errorf("退出后 ytLogin = %+v, want MethodNone", m.ytLogin)
	}
	if env.store.Login().Method != ytm.MethodNone {
		t.Errorf("store 登录配置应已清除: %+v", env.store.Login())
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "YT Music · 未登录") {
		t.Error("退出后概览应显示未登录")
	}
}

// u URL 导入：输入 → ytImportMsg → 异步导入成功 → notice + 列表创建 + 同步映射。
func TestYTURLImportSuccess(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	url := ytTrackURL("PLIMP")
	env.fetcher.playlists[url] = model.Playlist{
		ID:     "PLIMP",
		Title:  "导入歌单",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if m.plPage.mode != plURLImport || !m.plPage.typing() {
		t.Fatalf("u 后应进入 URL 导入: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "粘贴 YouTube Music 歌单链接，Enter 导入") {
		t.Error("URL 导入占位缺失")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(url)})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var im ytImportMsg
	for _, msg := range execCmds(cmd) {
		if imsg, ok := msg.(ytImportMsg); ok {
			im = imsg
		}
	}
	if im.url != url {
		t.Fatalf("ytImportMsg.url = %q, want %q", im.url, url)
	}
	if m.plPage.mode != plOverview {
		t.Fatalf("提交后应回概览: mode=%v", m.plPage.mode)
	}

	m, cmd = update(m, im)
	if !m.ytSyncing {
		t.Fatal("导入期间应置 syncing")
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "YT Music · 同步中…") {
		t.Error("导入期间状态区应显示同步中")
	}
	var id ytImportDoneMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytImportDoneMsg); ok {
			id = dm
		}
	}
	m, _ = update(m, id)
	if m.notice != "已导入「导入歌单」2 首" {
		t.Errorf("notice = %q, want 已导入「导入歌单」2 首", m.notice)
	}
	if m.ytSyncing {
		t.Error("导入完成后 syncing 应复位")
	}
	lists := m.pl.Lists()
	if len(lists) != 1 || lists[0].Name != "YT: 导入歌单" {
		t.Fatalf("导入后列表 = %+v, want [YT: 导入歌单]", lists)
	}
	if len(m.pl.Tracks("YT: 导入歌单")) != 2 {
		t.Errorf("导入歌曲数 = %d, want 2", len(m.pl.Tracks("YT: 导入歌单")))
	}
	if !m.plPage.ytSyncNames["YT: 导入歌单"] {
		t.Error("导入后同步映射应推入页面")
	}
}

// u URL 导入：空输入 Enter 忽略（不产生消息、不退出输入）。
func TestYTURLImportEmptyIgnored(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("空 URL Enter 不应产生消息: %v", cmd)
	}
	if m.plPage.mode != plURLImport {
		t.Errorf("空输入应留在导入模式: mode=%v", m.plPage.mode)
	}
}

// u URL 导入失败：lastError + syncing 复位（页面已退出输入，用户可重新按 u）。
func TestYTURLImportFailure(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	env.fetcher.err = errors.New("yt-dlp 拉取失败")
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(ytTrackURL("PLIMP"))})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	var im ytImportMsg
	for _, msg := range execCmds(cmd) {
		if imsg, ok := msg.(ytImportMsg); ok {
			im = imsg
		}
	}
	m, cmd = update(m, im)
	var id ytImportDoneMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytImportDoneMsg); ok {
			id = dm
		}
	}
	m, _ = update(m, id)
	if !strings.Contains(m.lastError, "导入失败") {
		t.Errorf("lastError = %q, want 含导入失败", m.lastError)
	}
	if m.ytSyncing {
		t.Error("失败后 syncing 应复位")
	}
	if len(m.pl.Lists()) != 0 {
		t.Errorf("失败不应创建列表: %+v", m.pl.Lists())
	}
}

// y 同步全部：成功 notice + 本地列表创建 + 同步映射 + 重复触发忽略。
func TestYTSyncAllFlow(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=sap; __Secure-3PAPISID=3p"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	env.fetcher.playlists[ytTrackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "我的最爱",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}
	env.fetcher.playlists[ytTrackURL("PLBBB")] = model.Playlist{
		ID: "PLBBB", Title: "通勤歌单",
		Tracks: []model.Track{testTrack("v3"), testTrack("v4")},
	}
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	var sa ytSyncAllMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(ytSyncAllMsg); ok {
			sa = am
		}
	}
	m, cmd = update(m, sa)
	if !m.ytSyncing {
		t.Fatal("y 后应置 syncing")
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "YT Music · 同步中…") {
		t.Error("同步中状态区应显示")
	}

	// 同步中重复按 y：emit 的消息回灌后被 root 忽略（不产生新同步 cmd）
	m2, dup := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	var sa2 ytSyncAllMsg
	for _, msg := range execCmds(dup) {
		if am, ok := msg.(ytSyncAllMsg); ok {
			sa2 = am
		}
	}
	m2, dup = update(m2, sa2)
	if dup != nil {
		t.Errorf("同步中 ytSyncAllMsg 应被忽略, got cmd %v", dup)
	}
	if !m2.ytSyncing {
		t.Error("忽略后 syncing 应保持")
	}

	var sd ytSyncDoneMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytSyncDoneMsg); ok {
			sd = dm
		}
	}
	if sd.err != nil {
		t.Fatalf("SyncAll 应成功: %v", sd.err)
	}
	m, _ = update(m, sd)
	if m.notice != "已同步 2 个歌单 · 共 4 首" {
		t.Errorf("notice = %q, want 已同步 2 个歌单 · 共 4 首", m.notice)
	}
	if m.ytSyncing {
		t.Error("同步完成后 syncing 应复位")
	}
	lists := m.pl.Lists()
	if len(lists) != 2 || lists[0].Name != "YT: 我的最爱" || lists[1].Name != "YT: 通勤歌单" {
		t.Fatalf("同步后列表 = %+v", lists)
	}
	if got := idsOf(m.pl.Tracks("YT: 我的最爱")); len(got) != 2 || got[0] != "v1" || got[1] != "v2" {
		t.Errorf("我的最爱曲目 = %v", got)
	}
	if !m.plPage.ytSyncNames["YT: 我的最爱"] || !m.plPage.ytSyncNames["YT: 通勤歌单"] {
		t.Error("同步后页面同步映射缺失")
	}
	got := stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 已登录") || strings.Contains(got, "同步中") {
		t.Errorf("同步完成后状态区应回已登录: %q", got)
	}
}

// y 同步全部（未登录）：错误提示 + syncing 复位。
func TestYTSyncAllNotLoggedIn(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	var sa ytSyncAllMsg
	for _, msg := range execCmds(cmd) {
		if am, ok := msg.(ytSyncAllMsg); ok {
			sa = am
		}
	}
	m, cmd = update(m, sa)
	var sd ytSyncDoneMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytSyncDoneMsg); ok {
			sd = dm
		}
	}
	m, _ = update(m, sd)
	if !strings.Contains(m.lastError, "同步失败") || !strings.Contains(m.lastError, "登录") {
		t.Errorf("lastError = %q, want 含同步失败与登录提示", m.lastError)
	}
	if m.ytSyncing {
		t.Error("失败后 syncing 应复位")
	}
}

// 详情 r：同步列表刷新成功（notice + 列表内容替换 + 映射更新）。
func TestYTRefreshSyncList(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=sap; __Secure-3PAPISID=3p"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	m := env.m
	if _, err := m.pl.Create("YT: 我的最爱"); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("YT: 我的最爱", testTrack("old")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertSync(ytm.SyncEntry{PlaylistID: "PLAAA", ListName: "YT: 我的最爱", Count: 1}); err != nil {
		t.Fatal(err)
	}
	env.fetcher.playlists[ytTrackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "我的最爱",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	m.plPage = m.plPage.setYTSyncs(env.store.SyncEntries())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 进入详情
	if !m.plPage.ytSyncNames["YT: 我的最爱"] {
		t.Fatal("同步列表应标记在页面")
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "r 刷新") {
		t.Error("同步列表详情应提示 r 刷新")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	var rm ytRefreshMsg
	for _, msg := range execCmds(cmd) {
		if rmsg, ok := msg.(ytRefreshMsg); ok {
			rm = rmsg
		}
	}
	if rm.listName != "YT: 我的最爱" {
		t.Fatalf("ytRefreshMsg.listName = %q", rm.listName)
	}
	m, cmd = update(m, rm)
	if !m.ytSyncing {
		t.Fatal("刷新期间应置 syncing")
	}
	var rd ytRefreshDoneMsg
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytRefreshDoneMsg); ok {
			rd = dm
		}
	}
	m, _ = update(m, rd)
	if m.notice != "已刷新「YT: 我的最爱」2 首" {
		t.Errorf("notice = %q, want 已刷新「YT: 我的最爱」2 首", m.notice)
	}
	if got := idsOf(m.pl.Tracks("YT: 我的最爱")); len(got) != 2 || got[0] != "v1" || got[1] != "v2" {
		t.Errorf("刷新后曲目 = %v, want [v1 v2]（旧曲替换）", got)
	}
	if m.ytSyncing {
		t.Error("刷新完成后 syncing 应复位")
	}
}

// 详情 r：非同步列表 → 红字提示，不触发同步。
func TestYTRefreshNonSyncList(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	if _, err := m.pl.Create("普通列表"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 进入详情
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	var rm ytRefreshMsg
	for _, msg := range execCmds(cmd) {
		if rmsg, ok := msg.(ytRefreshMsg); ok {
			rm = rmsg
		}
	}
	m, cmd = update(m, rm)
	if m.lastError != "该列表不是 YT Music 同步列表" {
		t.Errorf("lastError = %q", m.lastError)
	}
	if cmd != nil {
		t.Errorf("非同步列表不应触发同步 cmd: %v", cmd)
	}
	if m.ytSyncing {
		t.Error("非同步列表不应置 syncing")
	}
}

// 详情模式下 s/y/u 不响应（仅概览）；r 在概览不响应（仅详情）。
func TestYTSyncKeysScopedToMode(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := m.pl.Create("列表A"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // 详情
	if m.plPage.mode != plDetail {
		t.Fatal("应进入详情")
	}
	for _, k := range []rune{'s', 'y', 'u'} {
		m2, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		if cmd != nil {
			t.Errorf("详情模式 %c 不应产生 cmd: %v", k, cmd)
		}
		if m2.plPage.mode != plDetail {
			t.Errorf("详情模式 %c 不应切换模式: %v", k, m2.plPage.mode)
		}
	}
	// 概览模式 r：重命名（原有语义，非刷新）
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // 回概览
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.plPage.mode != plNaming {
		t.Errorf("概览 r 应仍为重命名: mode=%v", m.plPage.mode)
	}
	if cmd != nil {
		t.Errorf("概览 r 不应产生刷新消息: %v", cmd)
	}
}

// URL 导入输入聚焦时 p/空格/q 是输入字符（root 让位，同命名输入模式）。
func TestYTURLImportConsumesGlobalKeys(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.plPicker != nil {
		t.Fatal("URL 导入输入聚焦时 p 不应打开选择器")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if got := m.plPage.input.Value(); got != "p q" {
		t.Errorf("input = %q, want %q", got, "p q")
	}
	if !m.plPage.typing() {
		t.Error("输入后应仍在 URL 导入模式")
	}
}

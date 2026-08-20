package ui

import (
	"runtime"

	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"music-tui/model"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/ytm"
)

// ---- 播放列表页：空态与两级视图 ----

// 空态渲染：无任何列表时显示提示与新建引导。
func TestPlaylistsEmptyView(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	if m.plPage.mode != plDetail {
		t.Fatalf("mode = %v, want plDetail", m.plPage.mode)
	}
	got := m.plPage.view()
	if !strings.Contains(got, "列表为空") || !strings.Contains(got, "按 a 添加到播放列表") {
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "n", Code: 'n'})
	if !m.plPage.typing() {
		t.Fatal("按 n 后应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "我的歌单"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "r", Code: 'r'})
	if !m.plPage.typing() {
		t.Fatal("按 r 后应进入命名输入模式")
	}
	if got := m.plPage.input.Value(); got != "旧名字" {
		t.Fatalf("重命名应预填旧名, input = %q", got)
	}
	// 删掉旧名后输入新名
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "新名字"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, cmd := update(m, tea.KeyPressMsg{Text: "d", Code: 'd'})
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 选中 B
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

// 详情页：a 弹“添加到”选择器 → 默认队列项追加（trackAppendMsg 回灌后 queue.Len 验证）、
// d 移除歌曲。
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	if m.plPage.mode != plDetail {
		t.Fatalf("mode = %v, want plDetail", m.plPage.mode)
	}
	if got := m.plPage.view(); !strings.Contains(got, "测试歌曲 t1") {
		t.Errorf("详情应显示歌曲, got %q", got)
	}

	// a 弹选择器 → Enter 默认项"下一首播放"（插入 t1；空队列无当前曲 → 队首）
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var tin trackInsertNextMsg
	for _, msg := range execCmds(cmd) {
		if im, ok := msg.(trackInsertNextMsg); ok {
			tin = im
		}
	}
	if tin.track.ID != "t1" {
		t.Fatalf("trackInsertNextMsg.track = %s, want t1", tin.track.ID)
	}
	m, _ = update(m, tin)
	if m.plPicker != nil {
		t.Fatal("插入后选择器应关闭")
	}
	if m.queue.Len() != 1 || fp.playCount() != 0 {
		t.Errorf("a 插入应只入队不播放: Len=%d playCount=%d", m.queue.Len(), fp.playCount())
	}

	// d 移除选中歌曲（t1）
	m, cmd = update(m, tea.KeyPressMsg{Text: "d", Code: 'd'})
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})  // 选中 t2（下标 1）
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

// 命名输入聚焦时 a/p/空格 是输入字符（root 让位，同搜索输入框模式）。
func TestNamingInputConsumesGlobalKeys(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "n", Code: 'n'})
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'}) // a 应输入而非打开选择器
	if m.plPicker != nil {
		t.Fatal("命名输入聚焦时 a 不应打开选择器")
	}
	if got := m.plPage.input.Value(); got != "a" {
		t.Errorf("input = %q, want %q", got, "a")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "p", Code: 'p'}) // p 应输入而非播放
	if got := m.plPage.input.Value(); got != "ap" {
		t.Errorf("input = %q, want %q", got, "ap")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeySpace}) // 空格应输入而非切换播放
	if got := m.plPage.input.Value(); got != "ap " {
		t.Errorf("input = %q, want %q", got, "ap ")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "q", Code: 'q'}) // q 应输入而非退出程序
	if got := m.plPage.input.Value(); got != "ap q" {
		t.Errorf("input = %q, want %q", got, "ap q")
	}
	if m.plPicker != nil {
		t.Fatal("q 不应打开选择器")
	}
	if !m.plPage.typing() {
		t.Error("q 输入后应仍在命名输入模式")
	}
}

// ---- 全局 a 键选择器 ----

// 搜索页选中歌曲按 a：picker 出现（首项为"当前播放队列"）→ 下移到列表名 →
// Enter 选择列表 → AddTrack 生效 → notice 显示 → picker 关闭 → 列表页刷新。
func TestPickTrackFromSearchAddsToPlaylist(t *testing.T) {
	toastSuccessDuration = time.Millisecond // 快进 toast 定时器（成功提示 cmd 是 tea.Tick）
	defer func() { toastSuccessDuration = 3 * time.Second }()
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, fp, fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	if !strings.Contains(stripANSI(m.plPicker.view()), "添加到") {
		t.Errorf("选择器标题缺失: %q", m.plPicker.view())
	}
	if m.plPicker.naming {
		t.Fatal("选择器初始不应在命名输入模式")
	}
	// 默认选中首项"下一首播放"；下移 2 次到列表"收藏"再 Enter
	if _, ok := m.plPicker.list.SelectedItem().(pickerQueueNextItem); !ok {
		t.Fatalf("默认选中项 = %+v, want pickerQueueNextItem", m.plPicker.list.SelectedItem())
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// cmd 为 toast 消失定时器（成功提示走 toast 通道）：执行后仅产生过期消息
	for _, msg := range execCmds(cmd) {
		if _, ok := msg.(toastExpireMsg); !ok {
			t.Errorf("选择器加入的 cmd 应为 toast 过期消息, got %#v", msg)
		}
	}
	if m.plPicker != nil {
		t.Fatal("Enter 后选择器应关闭")
	}
	trs := m.pl.Tracks("收藏")
	if len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("AddTrack 未生效: %+v", trs)
	}
	if activeToastText(m) != "已添加到「收藏」" {
		t.Errorf("toast = %q, want 已添加到「收藏」", activeToastText(m))
	}
	if fp.playCount() != 0 {
		t.Errorf("加入播放列表不应触发播放, playCount = %d", fp.playCount())
	}
	// 列表页已刷新（picker 关闭后 setLists）
	if m.plPage.overview.Title != "播放列表 (1)" {
		t.Errorf("picker 关闭后列表页未刷新, Title = %q", m.plPage.overview.Title)
	}
	// View 渲染绿色成功横幅
	if !strings.Contains(m.View().Content, lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✔ 已添加到「收藏」")) {
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
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
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
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	// 无既有列表：首项"下一首播放" + "当前播放队列" + 末尾"＋ 新建列表"；下移 2 次选中新建项
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("Enter 后应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "新歌单"})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if activeToastText(m) != "已添加到「新歌单」" {
		t.Errorf("toast = %q", activeToastText(m))
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
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 下一首播放 → 当前播放队列
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 当前播放队列 → 收藏
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 收藏 → "＋ 新建列表"
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "收藏"})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	// 下移到"＋ 新建列表"（跳过"下一首播放"、"当前播放队列"与"收藏"）→ Enter 进入命名输入
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPicker == nil || !m.plPicker.naming {
		t.Fatal("应进入命名输入模式")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "废名"})
	// 命名输入内 Esc：回到选择态（选择器不关闭）
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPicker == nil || m.plPicker.naming {
		t.Fatal("命名输入 Esc 应返回选择态")
	}
	// 选择态 Esc：关闭选择器
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPicker != nil {
		t.Fatal("Esc 应关闭选择器")
	}
	if len(m.pl.Lists()) != 1 {
		t.Errorf("取消不应创建列表: %+v", m.pl.Lists())
	}
	if trs := m.pl.Tracks("收藏"); len(trs) != 0 {
		t.Errorf("取消不应加入曲目: %+v", trs)
	}
	if activeToastText(m) != "" {
		t.Errorf("取消不应有成功提示, toast = %q", activeToastText(m))
	}
}

// 首页无选中歌曲：按 a 提示错误，不打开选择器；按 p 静默无操作（delegate 到首页）。
func TestAToastWhenNoSelection(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker != nil {
		t.Error("无选中歌曲不应打开选择器")
	}
	if !strings.Contains(activeToastText(m), "当前没有可添加的歌曲") {
		t.Errorf("toast = %q", activeToastText(m))
	}
	if m.toast == nil || m.toast.kind != toastError {
		t.Errorf("toast kind = %+v, want toastError", m.toast)
	}
	// p 无选中时静默：不弹选择器、不产生 cmd、不改 toast
	before := m.toast
	m, cmd := update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	if m.plPicker != nil {
		t.Error("p 不应打开选择器")
	}
	if cmd != nil {
		t.Errorf("p 不应产生 cmd, got %v", cmd)
	}
	if m.toast != before {
		t.Error("p 不应改变 toast")
	}
}

// 搜索输入框聚焦时 a/p 是输入字符（不打开选择器/不触发播放）。
func TestPickKeyTypesWhenSearchInputFocused(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "4", Code: '4'})
	m, _ = update(m, tea.KeyPressMsg{Text: "aaa"})
	if m.plPicker != nil {
		t.Error("输入框聚焦时 a 不应打开选择器")
	}
	if got := m.searchPage.input.Value(); got != "aaa" {
		t.Errorf("input = %q, want aaa", got)
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	if got := m.searchPage.input.Value(); got != "aaap" {
		t.Errorf("input = %q, want aaap", got)
	}
	if m.plPicker != nil {
		t.Error("输入框聚焦时 p 不应打开选择器")
	}
}

// 历史页选中记录按 a：picker 出现，下移到列表名后 Enter 加入。
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

	m, _ = update(m, tea.KeyPressMsg{Text: "5", Code: '5'}) // 历史页
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("历史页选中记录按 a 应打开选择器")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 下一首播放 → 当前播放队列
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 当前播放队列 → "收藏"
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPicker != nil {
		t.Fatal("Enter 后选择器应关闭")
	}
	if trs := m.pl.Tracks("收藏"); len(trs) != 1 || trs[0].ID != "t1" {
		t.Fatalf("历史曲目未加入: %+v", trs)
	}
}

// 选择器首项"下一首播放"（默认选中）：Enter 插入到当前曲之后（走全局 trackInsertNextMsg）；
// 第二项"当前播放队列"：Enter 追加到队尾（走全局 trackAppendMsg）。
// 均不设 notice（无成功 toast）、选择器关闭、不触发播放。
func TestPickerQueueItemAppendsToQueue(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, fp, fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())

	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker == nil {
		t.Fatal("按 a 应打开选择器")
	}
	// 首项"下一首播放"（默认选中）
	it0, ok := m.plPicker.list.SelectedItem().(pickerQueueNextItem)
	if !ok {
		t.Fatalf("默认选中项 = %+v, want pickerQueueNextItem", m.plPicker.list.SelectedItem())
	}
	if stripANSI(it0.Title()) != "▶ 下一首播放" {
		t.Errorf("下一首项 Title = %q, want ▶ 下一首播放", it0.Title())
	}
	if it0.Description() != "插入到当前曲之后" {
		t.Errorf("下一首项 Description = %q, want 插入到当前曲之后", it0.Description())
	}
	// 第二项"当前播放队列"（追加到队尾）
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	it, ok := m.plPicker.list.SelectedItem().(pickerQueueItem)
	if !ok {
		t.Fatalf("Down 后选中项 = %+v, want pickerQueueItem", m.plPicker.list.SelectedItem())
	}
	if stripANSI(it.Title()) != "▶ 当前播放队列" {
		t.Errorf("队列项 Title = %q, want ▶ 当前播放队列", it.Title())
	}
	if it.Description() != "追加到队尾" {
		t.Errorf("队列项 Description = %q, want 追加到队尾", it.Description())
	}

	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if m.plPicker != nil {
		t.Fatal("Enter 后选择器应关闭")
	}
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != -1 {
		t.Errorf("追加后 Len/CurrentIndex = %d/%d, want 1/-1", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 0 {
		t.Errorf("追加不应触发播放, playCount = %d", fp.playCount())
	}
	if activeToastText(m) != "" {
		t.Errorf("队列项追加不应有成功 toast, got %q", activeToastText(m))
	}
	// 曲目未加入任何播放列表（"收藏"仍为空）
	if trs := m.pl.Tracks("收藏"); len(trs) != 0 {
		t.Errorf("队列项不应写入播放列表: %+v", trs)
	}
}

// 搜索页 p 播放选中歌曲：与 Enter 相同（替换语义，回首页）。
func TestPPlaysSelectedInSearch(t *testing.T) {
	fp := newFakePlayer()
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, fp, fa, nil)
	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 选中 t2
	m, cmd := update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	var sel trackSelectedMsg
	for _, msg := range execCmds(cmd) {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t2" {
		t.Fatalf("p 应选中 t2, got %s", sel.track.ID)
	}
	m, _ = update(m, sel)
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != 0 {
		t.Fatalf("替换后 Len/CurrentIndex = %d/%d, want 1/0", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("play = %d %q, want 1 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
}

// 历史页 p 重播选中记录：与 Enter 相同（替换语义，回首页）。
func TestPPlaysSelectedInHistory(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if err := m.history.Add(testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	if err := m.history.Add(testTrack("t2")); err != nil {
		t.Fatal(err)
	}
	m = m.refreshHistory()

	m, _ = update(m, tea.KeyPressMsg{Text: "5", Code: '5'}) // 历史页
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})                      // 选中 t1（列表新项在前）
	m, cmd := update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	var sel trackSelectedMsg
	for _, msg := range execCmds(cmd) {
		if sm, ok := msg.(trackSelectedMsg); ok {
			sel = sm
		}
	}
	if sel.track.ID != "t1" {
		t.Fatalf("p 应重播 t1, got %s", sel.track.ID)
	}
	m, _ = update(m, sel)
	if m.queue.Len() != 1 || m.queue.CurrentIndex() != 0 {
		t.Fatalf("替换后 Len/CurrentIndex = %d/%d, want 1/0", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("play = %d %q, want 1 次 t1", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
}

// 详情模式 p 与 Enter 相同：整列表替换进队列，从选中曲开始播放，回首页。
func TestPPlaysPlaylistFromDetail(t *testing.T) {
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})  // 选中 t2（下标 1）
	m, cmd := update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
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
	if m.queue.Len() != 3 || m.queue.CurrentIndex() != 1 {
		t.Fatalf("队列 Len/CurrentIndex = %d/%d, want 3/1", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t2").URL {
		t.Errorf("play = %d %q, want 1 次 t2", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
}

// 概览模式 p：播放选中列表（从第一首开始）。
func TestPPlaysPlaylistFromOverview(t *testing.T) {
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, cmd := update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	var pl plLoadMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(plLoadMsg); ok {
			pl = lm
		}
	}
	if pl.name != "歌单A" || pl.index != 0 {
		t.Fatalf("plLoadMsg = %+v, want 歌单A/0（从第一首开始）", pl)
	}
	m, _ = update(m, pl)
	if got := idsOf(m.queue.Tracks()); len(got) != 3 || got[0] != "t1" {
		t.Fatalf("队列 = %v, want [t1 t2 t3]", got)
	}
	if m.queue.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", m.queue.CurrentIndex())
	}
	if fp.playCount() != 1 || fp.lastPlayed() != testTrack("t1").URL {
		t.Errorf("play = %d %q, want 1 次 t1", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("播放后应回首页, current = %v", m.current)
	}
}

// 队列页 p 与 Enter 相同：跳转播放选中曲目（保留队列）。
func TestPPlaysSelectedInQueue(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})

	m, _ = update(m, tea.KeyPressMsg{Text: "2", Code: '2'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 选中 t3（下标 2）
	m, cmd = update(m, tea.KeyPressMsg{Text: "p", Code: 'p'})
	var qp queuePlayMsg
	for _, msg := range execCmds(cmd) {
		if qm, ok := msg.(queuePlayMsg); ok {
			qp = qm
		}
	}
	if qp.index != 2 {
		t.Fatalf("queuePlayMsg.index = %d, want 2", qp.index)
	}
	m, _ = update(m, qp)
	if m.queue.Len() != 3 || m.queue.CurrentIndex() != 2 {
		t.Errorf("跳转后 Len/CurrentIndex = %d/%d, want 3/2", m.queue.Len(), m.queue.CurrentIndex())
	}
	if fp.playCount() != 2 || fp.lastPlayed() != testTrack("t3").URL {
		t.Errorf("play = %d %q, want 2 次 t3", fp.playCount(), fp.lastPlayed())
	}
	if m.current != pageHome {
		t.Errorf("跳转播放后应回首页, current = %v", m.current)
	}
}

// ---- 尺寸与提示 ----

// WindowSizeMsg 应下发到播放列表页与选择器（高度减 Tab 栏 + 分隔线 2 行 + 状态栏 1 行）。
func TestPlaylistsPageReceivesWindowSize(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	// 高度 = 40 - 4（顶部空行 + Tab 栏 + 分隔线 3 行 + 状态栏 1 行）
	if m.plPage.width != 100 || m.plPage.height != 36 {
		t.Errorf("plPage 尺寸 = %dx%d, want 100x36", m.plPage.width, m.plPage.height)
	}

	// 选择器打开时同样接收尺寸
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m2 := newTestModel(t, newFakePlayer(), fa, nil)
	m2 = searchAndPick(t, m2, fa)
	m2, _ = update(m2, tea.KeyPressMsg{Text: "a", Code: 'a'})
	m2, _ = update(m2, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m2.plPicker.width != 100 || m2.plPicker.height != 36 {
		t.Errorf("picker 尺寸 = %dx%d, want 100x36", m2.plPicker.width, m2.plPicker.height)
	}
}

// toast 生命周期只由定时器管理：新按键分发不再清除活跃 toast（旧 notice 语义），
// 只有过期消息（id 匹配）才清除。
func TestToastNotClearedByKeyDispatch(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	m = searchAndPick(t, m, fa)
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 下一首播放 → 当前播放队列
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 当前播放队列 → "收藏"
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if activeToastText(m) == "" {
		t.Fatal("前置失败: 应有成功 toast")
	}
	// 按键分发不再清除 toast（生命周期只由定时器管理）
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if activeToastText(m) == "" {
		t.Error("按键分发不应清除 toast（生命周期只由定时器管理）")
	}
	if m.toast == nil {
		t.Fatal("前置失败: 应有活跃 toast")
	}
	// 过期消息（id 匹配）才清除
	m, _ = update(m, toastExpireMsg{id: m.toast.id})
	if activeToastText(m) != "" {
		t.Errorf("过期消息（id 匹配）应清除 toast, got %q", activeToastText(m))
	}
}

// ---- YT Music 同步：状态区 / 登录设置 / URL 导入 / 同步全部 / 刷新 ----

// 概览顶部状态区三态：未登录 / 已登录 / 同步中；空列表时也显示。
func TestYTStatusLineStates(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})

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
	m.plPage = m.plPage.setYTSyncStatus(env.store.Login(), true, false)
	got = stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 同步中…") {
		t.Errorf("同步中状态行缺失: %q", got)
	}
}

// s 登录设置：主菜单四项 → 浏览器二级列表 → Esc 逐层返回概览。
func TestYTSyncSetupBrowserFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
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
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPage.setupSub != setupBrowser {
		t.Fatalf("Enter 后 sub=%v, want setupBrowser", m.plPage.setupSub)
	}
	got = stripANSI(m.plPage.view())
	if !strings.Contains(got, "Google Chrome") || !strings.Contains(got, "Chromium") || !strings.Contains(got, "Opera") {
		t.Errorf("浏览器列表应含全部支持浏览器: %q", got)
	}

	// Esc → 主菜单；再 Esc → 概览
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPage.setupSub != setupMain {
		t.Fatalf("Esc 后 sub=%v, want setupMain", m.plPage.setupSub)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPage.mode != plOverview {
		t.Fatalf("Esc 后 mode=%v, want plOverview", m.plPage.mode)
	}
}

// 浏览器选择 Enter → emit ytLoginMsg{Browser} → root 保存配置并异步验证。
// （不执行 verify cmd：浏览器方式会触碰真实浏览器 cookie 配置，仅断言编排。）
func TestYTBrowserLoginEmitsAndSaves(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 浏览器读取
	if m.plPage.setupSub != setupBrowser {
		t.Fatal("应进入浏览器二级列表")
	}
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 默认选中 Google Chrome
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
	if activeToastText(m) != "已保存登录配置，验证中…" {
		t.Errorf("toast = %q, want 已保存登录配置，验证中…", activeToastText(m))
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
	toastSuccessDuration = time.Millisecond // 快进 toast 定时器（BatchMsg 展开会执行 tick）
	defer func() { toastSuccessDuration = 3 * time.Second }()
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	cookiesFile := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(cookiesFile,
		[]byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\ttest-sap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 浏览器读取 → cookies.txt 文件路径
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPage.setupSub != setupCookiesInput || !m.plPage.typing() {
		t.Fatalf("应进入 cookies 路径输入: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	if m.plPage.input.Placeholder != "输入 cookies.txt 完整路径" && !strings.Contains(stripANSI(m.plPage.view()), "输入 cookies.txt 完整路径") {
		t.Errorf("输入框占位应为 cookies.txt 路径提示, got placeholder=%q view=%q", m.plPage.input.Placeholder, stripANSI(m.plPage.view()))
	}
	m, _ = update(m, tea.KeyPressMsg{Text: cookiesFile})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

	// root：保存配置 → 验证中 toast → 异步验证成功
	m, cmd = update(m, lf)
	if activeToastText(m) != "已保存登录配置，验证中…" {
		t.Errorf("toast = %q", activeToastText(m))
	}
	// cmd = tea.Batch(toast 定时器, ytVerifyCmd)：回灌 BatchMsg 由 Update 展开
	//（测试驱动方式，真实 bubbletea 运行时由事件循环处理 BatchMsg）。
	m, _ = update(m, cmd().(tea.BatchMsg))
	if activeToastText(m) != "YT Music 登录有效" {
		t.Errorf("toast = %q, want YT Music 登录有效", activeToastText(m))
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = update(m, tea.KeyPressMsg{Text: "/nonexistent/cookies.txt"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var lf ytLoginFileMsg
	for _, msg := range execCmds(cmd) {
		if lm, ok := msg.(ytLoginFileMsg); ok {
			lf = lm
		}
	}
	m, _ = update(m, lf)
	if !strings.Contains(activeToastText(m), "cookies.txt 不可读") {
		t.Errorf("toast = %q, want 含 cookies.txt 不可读", activeToastText(m))
	}
	if m.ytLogin.Method != ytm.MethodNone {
		t.Errorf("不可读路径不应保存配置: %+v", m.ytLogin)
	}
}

// 粘贴 Cookie 流程：输入 → ytLoginPasteMsg → 落盘 cookies 文件 + 配置 → 验证成功。
func TestYTPasteLoginFlow(t *testing.T) {
    if runtime.GOOS == "windows" {
        t.Skip("Windows 无 POSIX 权限位语义（cookies 文件 0600 断言），skip")
    }
	toastSuccessDuration = time.Millisecond // 快进 toast 定时器（BatchMsg 展开会执行 tick）
	defer func() { toastSuccessDuration = 3 * time.Second }()
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // → 粘贴 Cookie 字符串
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.plPage.setupSub != setupPasteInput || !m.plPage.typing() {
		t.Fatalf("应进入粘贴输入: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "粘贴 Cookie 字符串") {
		t.Error("输入框占位应为粘贴提示")
	}
	m, _ = update(m, tea.KeyPressMsg{Text: "SAPISID=abc; __Secure-3PAPISID=xyz"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if activeToastText(m) != "已保存登录配置，验证中…" {
		t.Errorf("toast = %q", activeToastText(m))
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

	// cmd = tea.Batch(toast 定时器, ytVerifyCmd)：回灌 BatchMsg 由 Update 展开
	//（测试驱动方式，真实 bubbletea 运行时由事件循环处理 BatchMsg）。
	m, _ = update(m, cmd().(tea.BatchMsg))
	if activeToastText(m) != "YT Music 登录有效" {
		t.Errorf("toast = %q, want YT Music 登录有效", activeToastText(m))
	}
}

// 粘贴验证失败：HTTP 403（失效）与 logged_in=0（未登录）映射为友好文案。
func TestYTPasteLoginVerifyFailures(t *testing.T) {
	toastSuccessDuration = time.Millisecond // 快进 toast 定时器（BatchMsg 展开会执行 tick）
	defer func() { toastSuccessDuration = 3 * time.Second }()
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
			m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
			m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
			for i := 0; i < 2; i++ {
				m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
			}
			m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = update(m, tea.KeyPressMsg{Text: "SAPISID=bad"})
			m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			var lp ytLoginPasteMsg
			for _, msg := range execCmds(cmd) {
				if pm, ok := msg.(ytLoginPasteMsg); ok {
					lp = pm
				}
			}
			m, cmd = update(m, lp)
			// cmd = tea.Batch(toast 定时器, ytVerifyCmd)：回灌 BatchMsg 由 Update 展开
			//（测试驱动方式，真实 bubbletea 运行时由事件循环处理 BatchMsg）。
			m, _ = update(m, cmd().(tea.BatchMsg))
			if activeToastText(m) != tc.want {
				t.Errorf("toast = %q, want %q", activeToastText(m), tc.want)
			}
			// M5：验证失败后状态区降级展示「已登录（验证失败）」（配置仍在）
			got := stripANSI(m.plPage.view())
			if !strings.Contains(got, "YT Music · 已登录（验证失败）") {
				t.Errorf("验证失败后概览应显示已登录（验证失败）, got %q", got)
			}
			if strings.Contains(got, "YT Music · 已登录\n") {
				t.Errorf("验证失败后不应显示纯已登录态: %q", got)
			}
		})
	}
}

// M5：验证失败 → 状态区/设置页降级「已登录（验证失败）」；
// 重新登录并验证成功 → 恢复「已登录」；初始加载未验证 → 维持「已登录」。
func TestYTVerifyFailureDegradesStatus(t *testing.T) {
	toastSuccessDuration = time.Millisecond // 快进 toast 定时器（BatchMsg 展开会执行 tick）
	defer func() { toastSuccessDuration = 3 * time.Second }()
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	env.client.SetHTTPClient(&http.Client{Transport: ytRoundTripper{code: 403, body: ""}})
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	for i := 0; i < 2; i++ {
		m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = update(m, tea.KeyPressMsg{Text: "SAPISID=bad"})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var lp ytLoginPasteMsg
	for _, msg := range execCmds(cmd) {
		if pm, ok := msg.(ytLoginPasteMsg); ok {
			lp = pm
		}
	}
	m, cmd = update(m, lp)
	// cmd = tea.Batch(toast 定时器, ytVerifyCmd)：回灌 BatchMsg 由 Update 展开
	//（测试驱动方式，真实 bubbletea 运行时由事件循环处理 BatchMsg）。
	m, _ = update(m, cmd().(tea.BatchMsg)) // 验证失败（403）

	if !m.ytInvalid {
		t.Fatal("验证失败后 ytInvalid 应为 true")
	}
	if got := stripANSI(m.plPage.view()); !strings.Contains(got, "YT Music · 已登录（验证失败）") {
		t.Errorf("状态区应显示已登录（验证失败）: %q", got)
	}
	// 设置页当前状态行一致降级
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	if got := stripANSI(m.plPage.view()); !strings.Contains(got, "当前状态：已登录（验证失败）") {
		t.Errorf("设置页应显示已登录（验证失败）: %q", got)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	// 重新登录（成功验证）→ 恢复已登录（用粘贴方式：不触碰真实浏览器配置）
	env.client.SetHTTPClient(&http.Client{Transport: ytRoundTripper{code: 200, body: ytBrowseOK}})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	for i := 0; i < 2; i++ {
		m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = update(m, tea.KeyPressMsg{Text: "SAPISID=good"})
	m, cmd = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var lp2 ytLoginPasteMsg
	for _, msg := range execCmds(cmd) {
		if pm, ok := msg.(ytLoginPasteMsg); ok {
			lp2 = pm
		}
	}
	m, cmd = update(m, lp2)
	// 同上：回灌 BatchMsg 展开（toast 定时器 + ytVerifyCmd）
	m, _ = update(m, cmd().(tea.BatchMsg)) // 验证成功
	if m.ytInvalid {
		t.Error("验证成功后 ytInvalid 应为 false")
	}
	if got := stripANSI(m.plPage.view()); !strings.Contains(got, "YT Music · 已登录") || strings.Contains(got, "验证失败") {
		t.Errorf("验证成功后状态区应恢复已登录: %q", got)
	}
}

// 初始启动（未验证过）显示已登录（配置存在即显示，仅验证失败才降级）。
func TestYTInitialUnverifiedShowsLoggedIn(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=x"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	m := env.m
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	got := stripANSI(m.plPage.view())
	if !strings.Contains(got, "YT Music · 已登录") || strings.Contains(got, "验证失败") {
		t.Errorf("初始未验证应显示已登录: %q", got)
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown}) // cookies.txt
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = update(m, tea.KeyPressMsg{Text: "随便"})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPage.setupSub != setupMain || m.plPage.typing() {
		t.Fatalf("Esc 后应回主菜单且输入失焦: sub=%v typing=%v", m.plPage.setupSub, m.plPage.typing())
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 退出登录
	var lo ytLogoutMsg
	for _, msg := range execCmds(cmd) {
		if om, ok := msg.(ytLogoutMsg); ok {
			lo = om
		}
	}
	m, _ = update(m, lo)
	if activeToastText(m) != "已退出 YT Music 登录" {
		t.Errorf("toast = %q", activeToastText(m))
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "u", Code: 'u'})
	if m.plPage.mode != plURLImport || !m.plPage.typing() {
		t.Fatalf("u 后应进入 URL 导入: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
	if m.plPage.input.Placeholder != "粘贴 YouTube Music 歌单链接，Enter 导入" && !strings.Contains(stripANSI(m.plPage.view()), "粘贴 YouTube Music 歌单链接，Enter 导入") {
		t.Errorf("URL 导入占位缺失, got placeholder=%q", m.plPage.input.Placeholder)
	}
	m, _ = update(m, tea.KeyPressMsg{Text: url})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if activeToastText(m) != "已导入「导入歌单」2 首" {
		t.Errorf("toast = %q, want 已导入「导入歌单」2 首", activeToastText(m))
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "u", Code: 'u'})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "u", Code: 'u'})
	m, _ = update(m, tea.KeyPressMsg{Text: ytTrackURL("PLIMP")})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if !strings.Contains(activeToastText(m), "导入失败") {
		t.Errorf("toast = %q, want 含导入失败", activeToastText(m))
	}
	if m.ytSyncing {
		t.Error("失败后 syncing 应复位")
	}
	if len(m.pl.Lists()) != 0 {
		t.Errorf("失败不应创建列表: %+v", m.pl.Lists())
	}
}

// ---- l 本地路径导入（页面层：输入 → plLocalAddMsg；root 层负责扫描与入库） ----

// l 本地路径导入：输入 → plLocalAddMsg。Enter 后留在输入模式——失败时
// root 只 toast（不退出输入），用户可直接改路径重试；成功由 root 退出输入。
func TestPlaylistLocalAddFlow(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "l", Code: 'l'})
	if m.plPage.mode != plLocalAdd || !m.plPage.typing() {
		t.Fatalf("l 后应进入本地路径输入: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
	got := stripANSI(m.plPage.view())
	if m.plPage.input.Placeholder != "输入本地音乐目录路径，Enter 扫描" && !strings.Contains(got, "输入本地音乐目录路径，Enter 扫描") {
		t.Errorf("本地路径输入占位缺失, placeholder=%q", m.plPage.input.Placeholder)
	}
	if !strings.Contains(got, "Enter 扫描 · Esc 返回") {
		t.Error("本地路径输入提示行缺失")
	}
	dir := t.TempDir()
	m, _ = update(m, tea.KeyPressMsg{Text: dir})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	var lm plLocalAddMsg
	for _, msg := range execCmds(cmd) {
		if imsg, ok := msg.(plLocalAddMsg); ok {
			lm = imsg
		}
	}
	if lm.path != dir {
		t.Fatalf("plLocalAddMsg.path = %q, want %q", lm.path, dir)
	}
	if m.plPage.mode != plLocalAdd {
		t.Fatalf("Enter 后应留在输入模式（失败可重试，成功由 root 退出）: mode=%v", m.plPage.mode)
	}
	if got := m.plPage.input.Value(); got != dir {
		t.Errorf("提交后输入内容应保留（供修改重试）, got %q", got)
	}
}

// l 本地路径导入：空输入 Enter 忽略（不产生消息、不退出输入）。
func TestPlaylistLocalAddEmptyIgnored(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "l", Code: 'l'})
	m, _ = update(m, tea.KeyPressMsg{Text: "   "})
	m, cmd := update(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("空路径 Enter 不应产生消息: %v", cmd)
	}
	if m.plPage.mode != plLocalAdd {
		t.Errorf("空输入应留在本地输入模式: mode=%v", m.plPage.mode)
	}
}

// l 本地路径导入：Esc 取消回概览。
func TestPlaylistLocalAddEscCancels(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "l", Code: 'l'})
	m, _ = update(m, tea.KeyPressMsg{Text: "/tmp/x"})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.plPage.mode != plOverview || m.plPage.typing() {
		t.Fatalf("Esc 后应回概览: mode=%v typing=%v", m.plPage.mode, m.plPage.typing())
	}
}

// 本地路径输入聚焦时 a/空格/q 是输入字符（root 让位，同 URL 导入/命名输入）。
func TestPlaylistLocalAddConsumesGlobalKeys(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "l", Code: 'l'})
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker != nil {
		t.Fatal("本地路径输入聚焦时 a 不应打开选择器")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = update(m, tea.KeyPressMsg{Text: "q", Code: 'q'})
	if got := m.plPage.input.Value(); got != "a q" {
		t.Errorf("input = %q, want %q", got, "a q")
	}
	if !m.plPage.typing() {
		t.Error("输入后应仍在本地路径输入模式")
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, cmd := update(m, tea.KeyPressMsg{Text: "y", Code: 'y'})
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
	m2, dup := update(m, tea.KeyPressMsg{Text: "y", Code: 'y'})
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
	if activeToastText(m) != "已同步 2 个歌单 · 共 4 首" {
		t.Errorf("toast = %q, want 已同步 2 个歌单 · 共 4 首", activeToastText(m))
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
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, cmd := update(m, tea.KeyPressMsg{Text: "y", Code: 'y'})
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
	if !strings.Contains(activeToastText(m), "同步失败") || !strings.Contains(activeToastText(m), "登录") {
		t.Errorf("toast = %q, want 含同步失败与登录提示", activeToastText(m))
	}
	if m.ytSyncing {
		t.Error("失败后 syncing 应复位")
	}
}

// 跟进项 C：ytInvalid 随同步结果更新——验证失败置位后成功 SyncAll 清除；
// 验证有效态下 SyncAll 返回 ErrSessionInvalid → 置位。
func TestYTSyncDoneUpdatesInvalidState(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=sap; __Secure-3PAPISID=3p"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	env.fetcher.playlists[ytTrackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "我的最爱",
		Tracks: []model.Track{testTrack("v1")},
	}
	env.fetcher.playlists[ytTrackURL("PLBBB")] = model.Playlist{
		ID: "PLBBB", Title: "通勤歌单",
		Tracks: []model.Track{testTrack("v2")},
	}
	m := env.m

	// 验证失败 → 置位（状态区降级展示）
	m, _ = update(m, ytVerifyDoneMsg{err: ytm.ErrSessionInvalid})
	if !m.ytInvalid {
		t.Fatal("验证失败后 ytInvalid 应为 true")
	}
	if got := stripANSI(m.plPage.view()); !strings.Contains(got, "已登录（验证失败）") {
		t.Errorf("验证失败后状态区应降级: %q", got)
	}

	// 成功 SyncAll → 清除标记（状态区恢复已登录）
	cmd := ytSyncAllCmd(env.client, env.m.pl)
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
	if m.ytInvalid {
		t.Error("成功同步后应清除验证失败标记")
	}
	if got := stripANSI(m.plPage.view()); strings.Contains(got, "验证失败") {
		t.Errorf("成功同步后状态区不应再降级: %q", got)
	}

	// 会话失效（browse 403 → ErrSessionInvalid）→ 置位
	env.client.SetHTTPClient(&http.Client{Transport: ytRoundTripper{code: 403, body: ""}})
	cmd = ytSyncAllCmd(env.client, env.m.pl)
	sd = ytSyncDoneMsg{}
	for _, msg := range execCmds(cmd) {
		if dm, ok := msg.(ytSyncDoneMsg); ok {
			sd = dm
		}
	}
	if !errors.Is(sd.err, ytm.ErrSessionInvalid) {
		t.Fatalf("SyncAll 应返回 ErrSessionInvalid, got %v", sd.err)
	}
	m, _ = update(m, sd)
	if !m.ytInvalid {
		t.Error("SyncAll 返回 ErrSessionInvalid 后应置位验证失败标记")
	}
	if got := stripANSI(m.plPage.view()); !strings.Contains(got, "已登录（验证失败）") {
		t.Errorf("失效后状态区应降级: %q", got)
	}
}

// 跟进项 C（导入/刷新 handler）：成功清除、会话错误置位、其他错误保持。
func TestYTImportRefreshDoneUpdateInvalidState(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	if _, err := env.store.SetPastedLogin("SAPISID=sap; __Secure-3PAPISID=3p"); err != nil {
		t.Fatal(err)
	}
	env.refreshYTStatus()
	m := env.m

	m, _ = update(m, ytVerifyDoneMsg{err: ytm.ErrNotLoggedIn}) // 验证失败 → 置位
	if !m.ytInvalid {
		t.Fatal("验证失败后 ytInvalid 应为 true")
	}

	// 成功导入 → 清除
	m, _ = update(m, ytImportDoneMsg{res: ytm.SyncResult{Remote: ytm.RemotePlaylist{ID: "PLX", Title: "导入歌单"}, ListName: "YT: 导入歌单", TrackCount: 1}})
	if m.ytInvalid {
		t.Error("成功导入后应清除验证失败标记")
	}

	// 刷新返回 ErrSessionInvalid（包装形式，errors.Is 可识别）→ 置位
	m, _ = update(m, ytRefreshDoneMsg{err: fmt.Errorf("拉取歌单失败: %w", ytm.ErrSessionInvalid)})
	if !m.ytInvalid {
		t.Error("刷新返回 ErrSessionInvalid 后应置位验证失败标记")
	}

	// 其他错误（网络等）→ 保持置位
	m, _ = update(m, ytImportDoneMsg{err: errors.New("yt-dlp 网络错误")})
	if !m.ytInvalid {
		t.Error("网络错误不应清除验证失败标记")
	}

	// 成功刷新 → 清除
	m, _ = update(m, ytRefreshDoneMsg{res: ytm.SyncResult{Remote: ytm.RemotePlaylist{ID: "PLX", Title: "导入歌单"}, ListName: "YT: 导入歌单", TrackCount: 2}})
	if m.ytInvalid {
		t.Error("成功刷新后应清除验证失败标记")
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	if !m.plPage.ytSyncNames["YT: 我的最爱"] {
		t.Fatal("同步列表应标记在页面")
	}
	if !strings.Contains(stripANSI(m.plPage.view()), "r 刷新") {
		t.Error("同步列表详情应提示 r 刷新")
	}
	m, cmd := update(m, tea.KeyPressMsg{Text: "r", Code: 'r'})
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
	if activeToastText(m) != "已刷新「YT: 我的最爱」2 首" {
		t.Errorf("toast = %q, want 已刷新「YT: 我的最爱」2 首", activeToastText(m))
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入详情
	m, cmd := update(m, tea.KeyPressMsg{Text: "r", Code: 'r'})
	var rm ytRefreshMsg
	for _, msg := range execCmds(cmd) {
		if rmsg, ok := msg.(ytRefreshMsg); ok {
			rm = rmsg
		}
	}
	m, cmd = update(m, rm)
	if activeToastText(m) != "该列表不是 YT Music 同步列表" {
		t.Errorf("toast = %q", activeToastText(m))
	}
	_ = cmd // cmd 为 toast 消失定时器（错误提示走 toast 通道）；无同步/刷新 cmd
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

	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 详情
	if m.plPage.mode != plDetail {
		t.Fatal("应进入详情")
	}
	for _, k := range []rune{'s', 'y', 'u'} {
		m2, cmd := update(m, tea.KeyPressMsg{Text: string(k), Code: k})
		if cmd != nil {
			t.Errorf("详情模式 %c 不应产生 cmd: %v", k, cmd)
		}
		if m2.plPage.mode != plDetail {
			t.Errorf("详情模式 %c 不应切换模式: %v", k, m2.plPage.mode)
		}
	}
	// 概览模式 r：重命名（原有语义，非刷新）
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // 回概览
	m, cmd := update(m, tea.KeyPressMsg{Text: "r", Code: 'r'})
	if m.plPage.mode != plNaming {
		t.Errorf("概览 r 应仍为重命名: mode=%v", m.plPage.mode)
	}
	if cmd != nil {
		t.Errorf("概览 r 不应产生刷新消息: %v", cmd)
	}
}

// URL 导入输入聚焦时 a/空格/q 是输入字符（root 让位，同命名输入模式）。
// ---- 提示行贴底（bottomHint）----

// 播放列表页三个列表态（概览/详情/登录设置）的提示行应渲染在页面内容区
// 最后一行（窗口最底行），View 恰好 24 行不溢出；输入态（命名/URL 导入）
// 保持现状不动（不在此断言）。
func TestPlaylistsHintOnLastLine(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})

	// 概览（1 个列表）
	if _, err := m.pl.Create("收藏"); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("收藏", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	assertHintOnLastLine(t, m, "Enter 查看")

	// 详情
	m.plPage = m.plPage.enterDetail(m.pl.Lists()[0])
	assertHintOnLastLine(t, m, "Enter/p 从选中曲播放整个列表")

	// 登录设置主菜单
	m.plPage = m.plPage.enterSyncSetup()
	assertHintOnLastLine(t, m, "↑↓ 选择 · Enter 确认")

	// 登录设置输入子层（粘贴 Cookie）
	m.plPage = m.plPage.beginSetupInput(setupPasteInput, "粘贴 Cookie 字符串（name=value; ...）")
	assertHintOnLastLine(t, m, "↑↓ 选择 · Enter 确认")
}

// 登录设置浏览器二级选择时提示行同样贴底（hint 文案去掉 Esc 返回）。
func TestPlaylistsHintOnLastLineBrowserSub(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "s", Code: 's'})
	m.plPage = m.plPage.enterSetupBrowser()
	assertHintOnLastLine(t, m, "↑↓ 选择 · Enter 确认")
}

// TestPlaylistsHintOnLastLineManyLists 概览列表多到出现分页行时提示行仍贴底。
func TestPlaylistsHintOnLastLineManyLists(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	for i := 0; i < 9; i++ {
		if _, err := m.pl.Create(fmt.Sprintf("列表%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	m.plPage = m.plPage.setLists(m.pl.Lists())
	assertHintOnLastLine(t, m, "Enter 查看")
}

// 概览/详情空态时提示行同样贴底。
func TestPlaylistsEmptyHintOnLastLine(t *testing.T) {
	env := newYTTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m := env.m
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})

	// 概览空态（无列表）
	assertHintOnLastLine(t, m, "Enter 查看")

	// 详情空态
	m.plPage = m.plPage.enterDetail(playlists.List{Name: "收藏"})
	assertHintOnLastLine(t, m, "Enter/p 从选中曲播放整个列表")
}

func TestYTURLImportConsumesGlobalKeys(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyPressMsg{Text: "3", Code: '3'})
	m, _ = update(m, tea.KeyPressMsg{Text: "u", Code: 'u'})
	m, _ = update(m, tea.KeyPressMsg{Text: "a", Code: 'a'})
	if m.plPicker != nil {
		t.Fatal("URL 导入输入聚焦时 a 不应打开选择器")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = update(m, tea.KeyPressMsg{Text: "q", Code: 'q'})
	if got := m.plPage.input.Value(); got != "a q" {
		t.Errorf("input = %q, want %q", got, "a q")
	}
	if !m.plPage.typing() {
		t.Error("输入后应仍在 URL 导入模式")
	}
}

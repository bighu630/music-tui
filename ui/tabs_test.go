package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"music-tui/player"
)

// TestMain 强制默认渲染器输出 ANSI 颜色码：测试环境（非 TTY）下 lipgloss
// 默认按 Ascii profile 渲染，不产生任何转义序列，样式断言（高亮/Faint）
// 无从谈起。显式设为 TrueColor 后 tabStyle/Faint 断言才有区分度；
// 现有其他测试只做纯文本 Contains 断言，转义码仅包裹整段文本不受影响。
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// stripANSI 去除 ANSI 转义序列取纯文本（lipgloss v1.1.0 无 RemoveANSI，
// 等价替代：lipgloss v2 的 RemoveANSI 即 x/ansi 的 Strip 包装）。
func stripANSI(s string) string { return ansi.Strip(s) }

// 五标题齐全（初始无曲目 → 首页标签带 ⏹）。
func TestTabBarShowsFiveTitles(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	view := stripANSI(m.View())
	for _, title := range []string{"⏹ 首页", "队列", "播放列表", "搜索", "历史"} {
		if !strings.Contains(view, title) {
			t.Errorf("Tab 栏缺少 %q，view = %q", title, view)
		}
	}
}

// 初始在首页：首页标签高亮（Bold+212），其余 Faint；当前页不得同时 Faint。
// 直接复用 tabs.go 的包级样式变量，避免本地重复声明与实现不同步。
func TestTabBarHighlightsCurrentPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	view := m.View()
	if !strings.Contains(view, tabStyle.Render("⏹ 首页")) {
		t.Errorf("当前页标签应高亮（%q），view = %q", tabStyle.Render("⏹ 首页"), view)
	}
	for _, title := range []string{"队列", "播放列表", "搜索", "历史"} {
		if !strings.Contains(view, tabInactiveStyle.Render(title)) {
			t.Errorf("非当前页 %q 应为 Faint 样式", title)
		}
	}
	if strings.Contains(view, tabInactiveStyle.Render("⏹ 首页")) {
		t.Error("当前页不应为 Faint 样式")
	}
}

// Tab/数字键切换后高亮跟随移动。
func TestTabBarHighlightsFollowsSwitch(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // → 队列
	view := m.View()
	if !strings.Contains(view, tabStyle.Render("队列")) {
		t.Error("切到队列页后标签应高亮“队列”")
	}
	if strings.Contains(view, tabStyle.Render("⏹ 首页")) {
		t.Error("首页标签不应再高亮")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}) // → 搜索
	if !strings.Contains(m.View(), tabStyle.Render("搜索")) {
		t.Error("按 4 后标签应高亮“搜索”")
	}
}

// 队列数量：空队列不带数量，入队后显示 (N)。
func TestTabBarQueueCount(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if strings.Contains(stripANSI(m.View()), "队列 (") {
		t.Error("空队列标签不应显示数量")
	}
	for _, id := range []string{"q1", "q2", "q3"} {
		m, _ = update(m, trackAppendMsg{track: testTrack(id)})
	}
	if !strings.Contains(stripANSI(m.View()), "队列 (3)") {
		t.Errorf("队列标签应显示 (3)，view = %q", stripANSI(m.View()))
	}
}

// 播放状态图标：无曲目 ⏹ / 播放中 ⏵ / 暂停 ⏸。
func TestTabBarPlayStateIcon(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if !strings.Contains(stripANSI(m.View()), "⏹ 首页") {
		t.Error("无曲目时首页标签应显示 ⏹")
	}
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if !strings.Contains(stripANSI(m.View()), "⏵ 首页") {
		t.Error("播放中首页标签应显示 ⏵")
	}
	m, _ = update(m, playerEventMsg{ev: player.StateEvent{Playing: false}})
	if !strings.Contains(stripANSI(m.View()), "⏸ 首页") {
		t.Error("暂停时首页标签应显示 ⏸")
	}
}

// 回归：queuePage.setSize 此前从未被调用（列表固定 80x24）；
// 现在 WindowSizeMsg 应下发尺寸，且高度减 2（Tab 栏 + 分隔线占 2 行）。
func TestQueuePageReceivesWindowSize(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.queuePage.width != 100 || m.queuePage.height != 38 {
		t.Errorf("queuePage 尺寸 = %dx%d, want 100x38（高度减 Tab 栏 + 分隔线 2 行）",
			m.queuePage.width, m.queuePage.height)
	}
}

// 分隔线：未收到 WindowSizeMsg（宽度为 0）时不输出；收到窗口尺寸后
// 第 2 行为横贯全宽的 ─ 线（第 1 行仍为标签行），且宽度跟随窗口变化。
func TestTabBarDividerLine(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	if strings.Contains(stripANSI(m.View()), "─") {
		t.Error("未收到 WindowSizeMsg 前不应渲染分隔线")
	}

	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := strings.Split(stripANSI(m.View()), "\n")
	if len(lines) < 2 || lines[1] != strings.Repeat("─", 80) {
		t.Errorf("第 2 行应为 80 个 ─，实际 = %q", lines[1])
	}
	if !strings.Contains(lines[0], "首页") {
		t.Errorf("第 1 行应为标签行（含“首页”），实际 = %q", lines[0])
	}
	if !strings.Contains(m.View(), dividerStyle.Render(strings.Repeat("─", 80))) {
		t.Error("分隔线应使用 dividerStyle（Faint 弱化样式，与整体风格一致）")
	}

	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	lines = strings.Split(stripANSI(m.View()), "\n")
	if len(lines) < 2 || lines[1] != strings.Repeat("─", 100) {
		t.Errorf("宽度变化后第 2 行应为 100 个 ─，实际 = %q", lines[1])
	}
}

// ---- 鼠标交互（点击切换 + hover 高亮） ----

// 固定状态下的标签文本（无曲目 + 队列 3 首），与 tabBar 分隔约定（2 空格）一致。
// 用 ansi.StringWidth 独立计算各标签 0-based 起始列，避免与实现共享内部函数。
func mouseTabCols() []struct {
	text string
	col  int
	want page
} {
	labels := []struct {
		text string
		col  int
		want page
	}{
		{"⏹ 首页", 0, pageHome},
		{"队列 (3)", 0, pageQueue},
		{"播放列表", 0, pagePlaylists},
		{"搜索", 0, pageSearch},
		{"历史", 0, pageHistory},
	}
	col := 0
	for i := range labels {
		if i > 0 {
			col += 2 // 标签间分隔
		}
		labels[i].col = col
		col += ansi.StringWidth(labels[i].text)
	}
	return labels
}

// 点击每个标签（按下即响应）应切到对应页；点击当前页标签幂等。
func TestMouseClickSwitchesPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"q1", "q2", "q3"} {
		m, _ = update(m, trackAppendMsg{track: testTrack(id)})
	}
	seps := mouseTabCols()
	// 先注入悬停状态：悬停在“搜索”标签上（hoverTab=1），验证点击会清除悬停
	m, _ = update(m, tea.MouseMsg{Action: tea.MouseActionMotion, X: seps[1].col + 1, Y: 0})
	for _, lb := range seps {
		click := lb.col + 1 // 标签内部一列（0-based）
		m2, _ := update(m, tea.MouseMsg{
			Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: click, Y: 0,
		})
		if m2.current != lb.want {
			t.Errorf("点击 %q (x=%d) 后 current = %v, want %v", lb.text, click, m2.current, lb.want)
		}
		if m2.hoverTab != -1 {
			t.Errorf("点击 %q (x=%d) 后 hoverTab = %d, want -1（点击应清除悬停）", lb.text, click, m2.hoverTab)
		}
	}
	// 幂等：先切到搜索页，再点当前页“搜索”应保持 pageSearch
	m, _ = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: seps[3].col + 1, Y: 0,
	})
	if m.current != pageSearch {
		t.Fatalf("点击“搜索”后 current = %v, want pageSearch", m.current)
	}
	m2, _ := update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: seps[3].col + 1, Y: 0,
	})
	if m2.current != pageSearch {
		t.Errorf("点击当前页“搜索”应幂等, current = %v, want pageSearch", m2.current)
	}
}

// 点击标签间分隔不应切换页面。
// 注：mouseTabCols 按“队列 (3)”固定状态计算列位，本测试同样先入队 3 首对齐。
func TestMouseClickOnSeparatorIgnored(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"q1", "q2", "q3"} {
		m, _ = update(m, trackAppendMsg{track: testTrack(id)})
	}
	seps := mouseTabCols()
	for i := 1; i < len(seps); i++ {
		// 上一标签末尾与下一标签起始之间的两个分隔格
		for _, x := range []int{seps[i].col - 2, seps[i].col - 1} {
			m2, _ := update(m, tea.MouseMsg{
				Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: 0,
			})
			if m2.current != pageHome {
				t.Errorf("点击分隔 (x=%d) 不应切页, current = %v", x, m2.current)
			}
		}
	}
}

// 点击非首行、或 X 超出最后一个标签 → 不切页。
func TestMouseClickOutsideTabBarIgnored(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, _ := update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1, // 第二行
	})
	if m2.current != pageHome {
		t.Error("点击非首行不应切页")
	}
	m2, _ = update(m, tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 500, Y: 0, // 超界
	})
	if m2.current != pageHome {
		t.Error("点击超界位置不应切页")
	}
}

// 悬停非当前页标签 → 下划线高亮；移出 Tab 栏 → 清除。
func TestMouseHoverHighlightsTab(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	for _, id := range []string{"q1", "q2", "q3"} {
		m, _ = update(m, trackAppendMsg{track: testTrack(id)})
	}
	seps := mouseTabCols()
	x := seps[1].col + 1 // 悬停"队列"标签
	m2, _ := update(m, tea.MouseMsg{Action: tea.MouseActionMotion, X: x, Y: 0})
	if m2.hoverTab != int(pageQueue) {
		t.Errorf("hoverTab = %d, want %d", m2.hoverTab, int(pageQueue))
	}
	if !strings.Contains(m2.View(), tabHoverStyle.Render("队列")) {
		t.Error("悬停的标签应显示下划线高亮")
	}
	// 鼠标移出 Tab 栏 → 清除 hover
	m3, _ := update(m2, tea.MouseMsg{Action: tea.MouseActionMotion, X: x, Y: 5})
	if m3.hoverTab != -1 {
		t.Errorf("移出后 hoverTab = %d, want -1", m3.hoverTab)
	}
	if strings.Contains(m3.View(), tabHoverStyle.Render("队列")) {
		t.Error("移出后不应再有下划线高亮")
	}
}

// 悬停当前页标签 → 保持 tabStyle（当前页高亮优先于 hover）。
func TestMouseHoverOnCurrentTabKeepsTabStyle(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m2, _ := update(m, tea.MouseMsg{Action: tea.MouseActionMotion, X: 1, Y: 0}) // 悬停首页
	if !strings.Contains(m2.View(), tabStyle.Render("⏹ 首页")) {
		t.Error("悬停当前页应保持 tabStyle（高亮优先于 hover）")
	}
}

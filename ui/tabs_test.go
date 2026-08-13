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
// 无从谈起。显式设为 TrueColor 后 activeTab/Faint 断言才有区分度；
// 现有其他测试只做纯文本 Contains 断言，转义码仅包裹整段文本不受影响。
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// stripANSI 去除 ANSI 转义序列取纯文本（lipgloss v1.1.0 无 RemoveANSI，
// 等价替代：lipgloss v2 的 RemoveANSI 即 x/ansi 的 Strip 包装）。
func stripANSI(s string) string { return ansi.Strip(s) }

// activeTab 当前页标签样式（与 tabs.go 实现保持一致，测试据此断言高亮）。
func activeTab() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
}

// 四标题齐全（初始无曲目 → 首页标签带 ⏹）。
func TestTabBarShowsFourTitles(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	view := stripANSI(m.View())
	for _, title := range []string{"⏹ 首页", "搜索", "历史", "队列"} {
		if !strings.Contains(view, title) {
			t.Errorf("Tab 栏缺少 %q，view = %q", title, view)
		}
	}
}

// 初始在首页：首页标签高亮（Bold+212），其余 Faint；当前页不得同时 Faint。
func TestTabBarHighlightsCurrentPage(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	view := m.View()
	if !strings.Contains(view, activeTab().Render("⏹ 首页")) {
		t.Errorf("当前页标签应高亮（%q），view = %q", activeTab().Render("⏹ 首页"), view)
	}
	for _, title := range []string{"搜索", "历史", "队列"} {
		if !strings.Contains(view, lipgloss.NewStyle().Faint(true).Render(title)) {
			t.Errorf("非当前页 %q 应为 Faint 样式", title)
		}
	}
	if strings.Contains(view, lipgloss.NewStyle().Faint(true).Render("⏹ 首页")) {
		t.Error("当前页不应为 Faint 样式")
	}
}

// Tab/数字键切换后高亮跟随移动。
func TestTabBarHighlightsFollowsSwitch(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab}) // → 搜索
	view := m.View()
	if !strings.Contains(view, activeTab().Render("搜索")) {
		t.Error("切到搜索页后标签应高亮“搜索”")
	}
	if strings.Contains(view, activeTab().Render("⏹ 首页")) {
		t.Error("首页标签不应再高亮")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}) // → 队列
	if !strings.Contains(m.View(), activeTab().Render("队列")) {
		t.Error("按 4 后标签应高亮“队列”")
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
// 现在 WindowSizeMsg 应下发尺寸，且高度减 1（Tab 栏占 1 行）。
func TestQueuePageReceivesWindowSize(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.queuePage.width != 100 || m.queuePage.height != 39 {
		t.Errorf("queuePage 尺寸 = %dx%d, want 100x39（高度减 Tab 栏 1 行）",
			m.queuePage.width, m.queuePage.height)
	}
}

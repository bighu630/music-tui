package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// tabStyle 当前页标签样式：加粗 + 粉色高亮（与歌词高亮行/队列当前曲目一致）。
var tabStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

// tabInactiveStyle 非当前页标签样式：弱化。
var tabInactiveStyle = lipgloss.NewStyle().Faint(true)

// tabHoverStyle 鼠标悬停的非当前页标签样式：下划线提示（当前页高亮优先）。
var tabHoverStyle = lipgloss.NewStyle().Underline(true)

// dividerStyle 分隔线弱化样式（与 Faint 弱化约定一致）。
var dividerStyle = lipgloss.NewStyle().Faint(true)

// tabSeg 描述 Tab 栏一个标签的渲染信息（tabBar 渲染与 tabHitAt 命中检测共用）。
type tabSeg struct {
	page  page
	label string
	style lipgloss.Style
	col   int // 0-based 起始列，未含居中偏移（与 bubbletea MouseMsg.X 同基准）
	width int // 渲染后可见宽度（ANSI 剥离，中文按 2 列）
}

// tabSegments 计算五个标签的渲染信息。
// 注意：labels 顺序必须与 page 枚举 iota 顺序一致（pageHome..pageHistory = 0..4），
// 调换顺序须同步调整枚举，否则高亮与鼠标命中会错位。
func (m Model) tabSegments() []tabSeg {
	labels := []string{m.homeTabLabel(), m.queueTabLabel(), "播放列表", "搜索", "历史"}
	segs := make([]tabSeg, 0, len(labels))
	col := 0
	for i, label := range labels {
		if i > 0 {
			col += 2 // 标签间分隔宽度
		}
		style := tabInactiveStyle
		switch {
		case page(i) == m.current:
			style = tabStyle
		case i == m.hoverTab:
			style = tabHoverStyle
		}
		segs = append(segs, tabSeg{
			page:  page(i),
			label: label,
			style: style,
			col:   col,
			width: ansi.StringWidth(style.Render(label)),
		})
		col += segs[len(segs)-1].width
	}
	return segs
}

// tabBar 渲染顶部标签栏：标签行（五页标题 + 当前页高亮 + 首页播放状态图标 +
// 队列数量标记 + 悬停下划线）在窗口宽度内水平居中（偏移 m.tabPad()，
// 宽度未知或窗口过窄时左对齐）；随后追加一条横贯全宽的分隔线
// （宽度 m.width 由 WindowSizeMsg 下发；为 0 时不输出，避免空行）。
// 纯函数（无状态），由 View 拼在页面内容上方。
func (m Model) tabBar() string {
	var sb strings.Builder
	sb.WriteString(strings.Repeat(" ", m.tabPad()))
	for i, seg := range m.tabSegments() {
		if i > 0 {
			sb.WriteString("  ")
		}
		sb.WriteString(seg.style.Render(seg.label))
	}
	if m.width > 0 {
		sb.WriteString("\n" + dividerStyle.Render(strings.Repeat("─", m.width)))
	}
	return sb.String()
}

// tabPad 返回标签行居中起始列（0-based）：窗口宽度 m.width 已知且大于
// 标签总宽（tabSegments 裸列号布局的最后一列+宽度）时，返回
// (m.width - totalWidth) / 2；否则（宽度未知或窗口过窄）返回 0 不居中。
// 渲染（tabBar）与命中检测（tabHitAt）共用此偏移，保证鼠标点击列
// （MouseMsg.X）与视觉位置一致。
func (m Model) tabPad() int {
	if m.width <= 0 {
		return 0
	}
	segs := m.tabSegments()
	last := segs[len(segs)-1]
	totalWidth := last.col + last.width
	if totalWidth >= m.width {
		return 0
	}
	return (m.width - totalWidth) / 2
}

// tabHitAt 返回点击列 x（0-based，同 bubbletea MouseMsg.X）命中的标签页；
// 命中标签间分隔/空白时返回 false。先减去居中偏移 m.tabPad()（与 tabBar
// 渲染偏移一致），落在左侧留白（x < 0）直接判定未命中。
func (m Model) tabHitAt(x int) (page, bool) {
	x -= m.tabPad()
	if x < 0 {
		return 0, false
	}
	for _, seg := range m.tabSegments() {
		if x >= seg.col && x < seg.col+seg.width {
			return seg.page, true
		}
	}
	return 0, false
}

// homeTabLabel 首页标签：播放状态图标 + 标题。
// ⏵ 播放中 / ⏸ 已暂停 / ⏹ 未播放（与首页页体内状态文案的图标一致）。
func (m Model) homeTabLabel() string {
	switch {
	case m.state.Track == nil:
		return "⏹ 首页"
	case m.state.Playing:
		return "⏵ 首页"
	default:
		return "⏸ 首页"
	}
}

// queueTabLabel 队列标签：数量 >0 时显示 "队列 (N)"，空队列不带数量。
func (m Model) queueTabLabel() string {
	if n := m.queue.Len(); n > 0 {
		return fmt.Sprintf("队列 (%d)", n)
	}
	return "队列"
}

package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// tabStyle 当前页标签样式：加粗 + 粉色高亮（与歌词高亮行/队列当前曲目一致）。
var tabStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))

// tabInactiveStyle 非当前页标签样式：弱化。
var tabInactiveStyle = lipgloss.NewStyle().Faint(true)

// tabBar 渲染顶部标签栏：四页标题 + 当前页高亮 + 首页播放状态图标 +
// 队列数量标记。纯函数（无状态），由 View 拼在页面内容上方。
//
// 注意：labels 的元素顺序耦合 page 枚举的 iota 顺序（pageHome/
// pageSearch/pageHistory/pageQueue = 0..3），循环里用 page(i) 与当前页
// 比对决定高亮；若未来调换标签顺序，必须同步调整枚举顺序，否则高亮错位。
func (m Model) tabBar() string {
	labels := []string{m.homeTabLabel(), "搜索", "历史", m.queueTabLabel()}
	var sb strings.Builder
	for i, label := range labels {
		if i > 0 {
			sb.WriteString("  ")
		}
		style := tabInactiveStyle
		if page(i) == m.current {
			style = tabStyle
		}
		sb.WriteString(style.Render(label))
	}
	return sb.String()
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

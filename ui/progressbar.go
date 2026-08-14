package ui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// progressPalette 是已播段的渐变色阶（紫→粉，5 段），与 bubbles progress
// WithDefaultGradient 的观感一致。
var progressPalette = []lipgloss.Color{"63", "99", "129", "177", "212"}

// progressUnplayedStyle 是未播段 ━ 的样式：Faint 灰。
var progressUnplayedStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))

// gradientColor 返回已播段第 i 个字符（共 total 个）的渐变色：
// 按 i/(total-1) 比例在色阶中取色；total<=1 时用末段色。
func gradientColor(i, total int) lipgloss.Color {
	if total <= 1 {
		return progressPalette[len(progressPalette)-1]
	}
	ratio := float64(i) / float64(total-1)
	idx := int(math.Round(ratio * float64(len(progressPalette)-1)))
	return progressPalette[idx]
}

// sliderColor 返回滑块 ● 的颜色（当前进度色）：已播段非空时用末段色，
// 未开始播放（0%）时用色阶首色。
func sliderColor(pos int) lipgloss.Color {
	if pos == 0 {
		return progressPalette[0]
	}
	return gradientColor(pos-1, pos)
}

// lineProgressBar 渲染单行线条渐变进度条：已播段为紫→粉渐变 ━，
// 滑块 ●（当前进度色）占一个字符位，未播段为 Faint 灰 ━。
// 滑块位置 filled = round(percent*width)（clamp 到 [0, width-1]）；
// 可见宽度恒等于 width（ANSI 序列不计宽度）。
// 宽度不足 3 时退化为纯字符条（width<=1 → "●"，width==2 → "━●"）。
func lineProgressBar(width int, percent float64) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "●"
	}
	if width == 2 {
		return "━●"
	}
	pos := int(math.Round(percent * float64(width)))
	if pos < 0 {
		pos = 0
	}
	if pos > width-1 {
		pos = width - 1
	}
	var b strings.Builder
	b.Grow(width * 8)
	for i := 0; i < pos; i++ {
		b.WriteString(lipgloss.NewStyle().Foreground(gradientColor(i, pos)).Render("━"))
	}
	b.WriteString(lipgloss.NewStyle().Foreground(sliderColor(pos)).Render("●"))
	if n := width - pos - 1; n > 0 {
		b.WriteString(strings.Repeat(progressUnplayedStyle.Render("━"), n))
	}
	return b.String()
}

// progressClickPercent 把进度条上的点击列 x（0-based）换算为进度百分比 [0,1]；
// 越界 clamp；barWidth<=0 时返回 0（防御，不 panic）。
func progressClickPercent(x, barWidth int) float64 {
	if barWidth <= 0 {
		return 0
	}
	p := float64(x) / float64(barWidth)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

package ui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// 渐变端点 RGB（紫 #5A56E0 → 粉 #EE6FF8，与 bubbles progress
// WithDefaultGradient 观感一致）：已播段逐字符线性插值（平滑渐变），
// 量化到 256 色（6×6×6 立方体）兼容 tmux/老终端（回归：曾用 5 段固定
// 色阶，颜色跳变呈"一段一段"而非渐变）。
const (
	gradR0, gradG0, gradB0 = 0x5A, 0x56, 0xE0 // 紫
	gradR1, gradG1, gradB1 = 0xEE, 0x6F, 0xF8 // 粉
)

// progressUnplayedStyle 是未播段 ━ 的样式：Faint 灰。
var progressUnplayedStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))

// gradientRGB 返回已播段第 i 个字符（共 total 个）的 RGB 插值色：
// 紫→粉线性插值（24-bit 真彩色，逐字符平滑渐变）。曾用 256 色立方体量化
// ——短 RGB 路径只穿过 3-4 级色阶，视觉呈"一段一段"（回归：用户反馈
// 渐变像色块）。24-bit 每字符颜色微变（步长 ≈ 3/通道），观感连续。
func gradientRGB(i, total int) (r, g, b int) {
	if total <= 1 {
		return gradR1, gradG1, gradB1
	}
	t := float64(i) / float64(total-1)
	r = gradR0 + int(float64(gradR1-gradR0)*t)
	g = gradG0 + int(float64(gradG1-gradG0)*t)
	b = gradB0 + int(float64(gradB1-gradB0)*t)
	return
}

// sliderIndex 返回滑块 ● 的插值位置：已播段非空时用末段色，未开始播放（0%）
// 时用起始紫。
func sliderIndex(pos int) int {
	if pos == 0 {
		return 0
	}
	return pos - 1
}

// renderGradientChar 渲染一个真彩色渐变字符（━ 已播 / ● 滑块）。
func renderGradientChar(ch rune, r, g, b int) string {
	return "[38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) + "m" + string(ch) + "[0m"
}

// itoa 整数转十进制字符串（避免 fmt.Sprintf 热路径开销）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
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
	b.Grow(width * 30)
	// 已播段：逐字符 24-bit 插值色（平滑渐变）。
	for i := 0; i < pos; i++ {
		r, g, bb := gradientRGB(i, pos)
		b.WriteString(renderGradientChar('━', r, g, bb))
	}
	// 滑块：当前进度色（末段粉 / 起始紫）。
	si := sliderIndex(pos)
	if si == 0 {
		b.WriteString(renderGradientChar('●', gradR0, gradG0, gradB0))
	} else {
		r, g, bb := gradientRGB(si, pos)
		b.WriteString(renderGradientChar('●', r, g, bb))
	}
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

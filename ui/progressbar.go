package ui

import (
	"math"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// progressPalette 是已播段的渐变色阶（紫→粉，5 段），与 bubbles progress
// WithDefaultGradient 的观感一致。
var progressPalette = []lipgloss.Color{"63", "99", "129", "177", "212"}

// progressUnplayedStyle 是未播段 ━ 的样式：Faint 灰。
var progressUnplayedStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))

// progressFilledChars/progressSliderChars 已播段 5 色阶 "━" 与滑块 "●" 的
// 预渲染结果（下标 = progressPalette 下标）：避免 lineProgressBar 逐字符
// NewStyle 的重复开销。lipgloss 对相同样式+文本的渲染输出是确定的，预渲染
// 与逐字符渲染字节一致（Nit 6 纯性能优化，不改变渲染输出；回归：
// TestProgressPreRenderedBytes）。
// 惰性初始化：lipgloss 的全局 ColorProfile 可能在包加载后才被设置（如测试
// TestMain 强制 TrueColor），包级 init 时渲染会拿到 Ascii profile 丢失色码；
// 真实程序与测试都在首次渲染前设置好 profile，Once 缓存首见 profile 即可。
var (
	progressCharsOnce sync.Once
	progressFilled    []string
	progressSlider    []string
)

func renderProgressChars() {
	progressFilled = make([]string, len(progressPalette))
	progressSlider = make([]string, len(progressPalette))
	for i, c := range progressPalette {
		progressFilled[i] = lipgloss.NewStyle().Foreground(c).Render("━")
		progressSlider[i] = lipgloss.NewStyle().Foreground(c).Render("●")
	}
}

func progressFilledChars() []string {
	progressCharsOnce.Do(renderProgressChars)
	return progressFilled
}

func progressSliderChars() []string {
	progressCharsOnce.Do(renderProgressChars)
	return progressSlider
}

// gradientIndex 返回已播段第 i 个字符（共 total 个）的色阶下标：
// 按 i/(total-1) 比例在色阶中取色；total<=1 时用末段色。
func gradientIndex(i, total int) int {
	if total <= 1 {
		return len(progressPalette) - 1
	}
	ratio := float64(i) / float64(total-1)
	return int(math.Round(ratio * float64(len(progressPalette)-1)))
}

// gradientColor 返回已播段第 i 个字符（共 total 个）的渐变色。
func gradientColor(i, total int) lipgloss.Color {
	return progressPalette[gradientIndex(i, total)]
}

// sliderIndex 返回滑块 ● 的色阶下标：已播段非空时用末段色，未开始播放（0%）
// 时用色阶首色。
func sliderIndex(pos int) int {
	if pos == 0 {
		return 0
	}
	return gradientIndex(pos-1, pos)
}

// sliderColor 返回滑块 ● 的颜色（当前进度色）。
func sliderColor(pos int) lipgloss.Color {
	return progressPalette[sliderIndex(pos)]
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
	filled := progressFilledChars()
	// 已播段：连续同色字符合并为 strings.Repeat 复用预渲染串（字节与逐字符
	// 写入一致，且避免每字符查样式表）；不同色阶交界处自然换串。
	for i := 0; i < pos; {
		j := gradientIndex(i, pos)
		run := 1
		for i+run < pos && gradientIndex(i+run, pos) == j {
			run++
		}
		b.WriteString(strings.Repeat(filled[j], run))
		i += run
	}
	b.WriteString(progressSliderChars()[sliderIndex(pos)])
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

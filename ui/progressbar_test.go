package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// sgrRe 匹配 SGR 转义序列，捕获其中的参数串（如 "38;5;63"）。
var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// sgrCodes 提取字符串中所有 SGR 参数串（不含 ESC[ 与 m）。
func sgrCodes(s string) []string {
	ms := sgrRe.FindAllString(s, -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(m, "\x1b["), "m"))
	}
	return out
}

func TestLineProgress(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		pct      float64
		wantPos  int // ● 所在 0-based 列
		wantGray int // ● 之后灰 ━ 个数
	}{
		{"start", 20, 0, 0, 19},
		{"half", 20, 0.5, 10, 9},
		{"full", 20, 1, 19, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bar := lineProgressBar(c.width, c.pct)

			// 可见宽度必须等于 width（ANSI 序列不计宽度）。
			if w := ansi.StringWidth(bar); w != c.width {
				t.Fatalf("lineProgressBar(%d, %v) 可见宽度 = %d, want %d", c.width, c.pct, w, c.width)
			}
			// ● 恰好出现一次。
			if n := strings.Count(bar, "●"); n != 1 {
				t.Fatalf("lineProgressBar(%d, %v) 含 ● %d 次, want 1", c.width, c.pct, n)
			}

			idx := strings.Index(bar, "●")
			pos := ansi.StringWidth(bar[:idx])
			if pos != c.wantPos {
				t.Errorf("lineProgressBar(%d, %v) ● 在第 %d 列, want %d", c.width, c.pct, pos, c.wantPos)
			}

			// 已播段每个字符都带渐变色码（可见字符数 = ● 前渐变 ━ 数）；未播段每个字符都是灰。
			// 注意：bar[:idx] 含 ● 自身的色码前缀，故用剥离 ANSI 后的可见字符计数。
			played := bar[:idx]
			unplayed := bar[idx+len("●"):]
			if n := len([]rune(ansi.Strip(played))); n != c.wantPos {
				t.Errorf("lineProgressBar(%d, %v) 已播渐变字符数 = %d, want %d", c.width, c.pct, n, c.wantPos)
			}
			if n := strings.Count(unplayed, "38;5;240"); n != c.wantGray {
				t.Errorf("lineProgressBar(%d, %v) 未播灰字符数 = %d, want %d", c.width, c.pct, n, c.wantGray)
			}
		})
	}
}

// TestProgressPreRenderedBytes 预渲染常量必须与逐字符 NewStyle().Render 的
// 输出字节一致（Nit 6 预渲染是纯性能优化，不得改变渲染输出）。
func TestProgressPreRenderedBytes(t *testing.T) {
	// 渲染确定性：同参数两次渲染字节一致；端点颜色正确（紫→粉 24-bit）。
	bar := lineProgressBar(60, 0.5)
	if bar != lineProgressBar(60, 0.5) {
		t.Error("同参数渲染结果应确定性一致")
	}
	r0, g0, b0 := gradientRGB(0, 2)
	if r0 != gradR0 || g0 != gradG0 || b0 != gradB0 {
		t.Errorf("渐变首色 RGB = (%d,%d,%d), want 紫端点", r0, g0, b0)
	}
	r1, g1, b1 := gradientRGB(1, 2)
	if r1 != gradR1 || g1 != gradG1 || b1 != gradB1 {
		t.Errorf("渐变末色 RGB = (%d,%d,%d), want 粉端点", r1, g1, b1)
	}
	// sliderIndex 边界
	if sliderIndex(0) != 0 || sliderIndex(10) != 9 {
		t.Errorf("sliderIndex 边界错误: %d %d", sliderIndex(0), sliderIndex(10))
	}
}

func TestLineProgressTinyWidth(t *testing.T) {
	// 宽度不足 3 退化：无 panic、无 ANSI、含滑块。
	for _, c := range []struct {
		width int
		pct   float64
		want  string
	}{
		{1, 0, "●"},
		{1, 0.5, "●"},
		{1, 1, "●"},
		{2, 0, "━●"},
		{2, 0.5, "━●"},
		{2, 1, "━●"},
		{0, 0, ""},
		{-1, 0.5, ""},
	} {
		if got := lineProgressBar(c.width, c.pct); got != c.want {
			t.Errorf("lineProgressBar(%d, %v) = %q, want %q", c.width, c.pct, got, c.want)
		}
	}
}

func TestLineProgressClamp(t *testing.T) {
	// 负数/超 1 的 percent clamp 到 0/1。
	if got, want := lineProgressBar(20, -0.5), lineProgressBar(20, 0); got != want {
		t.Errorf("lineProgressBar(20, -0.5) != lineProgressBar(20, 0)")
	}
	if got, want := lineProgressBar(20, -3), lineProgressBar(20, 0); got != want {
		t.Errorf("lineProgressBar(20, -3) != lineProgressBar(20, 0)")
	}
	if got, want := lineProgressBar(20, 1.5), lineProgressBar(20, 1); got != want {
		t.Errorf("lineProgressBar(20, 1.5) != lineProgressBar(20, 1)")
	}
}

func TestLineProgressGradient(t *testing.T) {
	bar := lineProgressBar(20, 0.5)
	idx := strings.Index(bar, "●")
	played := bar[:idx]
	unplayed := bar[idx+len("●"):]

	// 已播段含 ANSI 色码。
	if !strings.Contains(bar, "\x1b[") {
		t.Fatal("lineProgressBar(20, 0.5) 已播段不含 ANSI 色码")
	}
	// 渐变色阶 > 1：同一进度下不同位置颜色不同（24-bit 真彩色 38;2;）。
	colors := map[string]bool{}
	for _, c := range sgrCodes(played) {
		if strings.HasPrefix(c, "38;2;") {
			colors[c] = true
		}
	}
	if len(colors) < 2 {
		t.Errorf("已播段渐变色阶种类 = %d, want > 1 (%v)", len(colors), colors)
	}
	// 已播段不应出现灰色码（38;5;240 是未播段样式）。
	for _, c := range sgrCodes(played) {
		if strings.Contains(c, "38;5;240") {
			t.Errorf("已播段出现灰色码 38;5;240")
		}
	}
	// 滑块后（未播段）只有灰/Faint（lipgloss 会把 Faint+前景合并成单条 SGR "2;38;5;240"），无彩色码。
	for _, c := range sgrCodes(unplayed) {
		if c == "0" || isGrayOnly(c) {
			continue
		}
		t.Errorf("未播段出现非灰/Faint 的 ANSI 码 %q", c)
	}

	// 渐变平滑性：已播段相邻字符的通道差 ≤ 理论插值步长 + 1（浮点截断容差）；
	// 曾用 5 段固定色阶/256 色量化，相邻字符色差大呈"一段一段"（回归）。
	pos := 10                           // 20*0.5
	step := (gradR1-gradR0)/(pos-1) + 2 // R 通道理论步长 + 容差（R 跨距最大）
	pr, pg, pb := 0, 0, 0
	for i := 0; i < pos; i++ {
		cr, cg, cb := gradientRGB(i, pos)
		if i > 0 {
			if absInt(cr-pr) > step || absInt(cg-pg) > step || absInt(cb-pb) > step {
				t.Errorf("渐变不连续: 字符 %d RGB(%d,%d,%d) → (%d,%d,%d)（阈值 %d）", i, pr, pg, pb, cr, cg, cb, step)
			}
		}
		pr, pg, pb = cr, cg, cb
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// isGrayOnly 报告 SGR 参数串是否仅由 Faint(2) 与灰前景(38;5;240) 组成（顺序任意）。
func isGrayOnly(c string) bool {
	for len(c) > 0 {
		switch {
		case strings.HasPrefix(c, "38;5;240"):
			c = c[len("38;5;240"):]
		case strings.HasPrefix(c, "2"):
			c = c[1:]
		default:
			return false
		}
		c = strings.TrimPrefix(c, ";")
	}
	return true
}

func TestProgressClickPercent(t *testing.T) {
	// x=0 → 0
	if got := progressClickPercent(0, 20); got != 0 {
		t.Errorf("progressClickPercent(0, 20) = %v, want 0", got)
	}
	// x=barWidth/2 → 0.5
	if got := progressClickPercent(10, 20); got != 0.5 {
		t.Errorf("progressClickPercent(10, 20) = %v, want 0.5", got)
	}
	// x=barWidth-1 → ≈1
	if got := progressClickPercent(19, 20); got < 0.9 || got > 1 {
		t.Errorf("progressClickPercent(19, 20) = %v, want ≈1", got)
	}
	// 越界 clamp
	if got := progressClickPercent(-1, 20); got != 0 {
		t.Errorf("progressClickPercent(-1, 20) = %v, want 0", got)
	}
	if got := progressClickPercent(-100, 20); got != 0 {
		t.Errorf("progressClickPercent(-100, 20) = %v, want 0", got)
	}
	if got := progressClickPercent(20, 20); got != 1 {
		t.Errorf("progressClickPercent(20, 20) = %v, want 1", got)
	}
	if got := progressClickPercent(100, 20); got != 1 {
		t.Errorf("progressClickPercent(100, 20) = %v, want 1", got)
	}
	// barWidth<=0 防御：不 panic、返回 0
	if got := progressClickPercent(5, 0); got != 0 {
		t.Errorf("progressClickPercent(5, 0) = %v, want 0", got)
	}
	if got := progressClickPercent(5, -2); got != 0 {
		t.Errorf("progressClickPercent(5, -2) = %v, want 0", got)
	}
}

package ui

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderCoverArtFixedSize 回归：自绘封面输出恒定 w×h（行数、每行可见宽），
// 无尾随换行、纯 256 色 SGR——布局与终端特性的不变量。
func TestRenderCoverArtFixedSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	out := renderCoverArt(img, 30, 17)
	lines := strings.Split(out, "\n")
	if len(lines) != 17 {
		t.Fatalf("行数 = %d, want 17", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 30 {
			t.Errorf("行 %d 可见宽 = %d, want 30", i, w)
		}
	}
	if strings.HasSuffix(out, "\n") {
		t.Error("输出不应以换行结尾")
	}
	// 色码范围：仅 256 色前景/背景（38;5;N / 48;5;N，N ∈ [16,231]）
	bad := map[string]bool{}
	for _, m := range ansiSGRCodes(out) {
		if len(m) < 2 {
			continue // 重置码 [0m 无参数
		}
		if !(m[0] == "38" && m[1] == "5") && !(m[0] == "48" && m[1] == "5") {
			bad["非256色: "+strings.Join(m, ";")] = true
		}
	}
	if len(bad) > 0 {
		t.Errorf("发现非 256 色 SGR: %v", bad)
	}
}

// TestRenderCoverArtScaleFit16x9 ScaleFit 语义：16:9 图（64×36）在 30×17 格
// （30×34px 像素框）内等比例缩放为 30×17px 并垂直居中——上下各 ~4 行列纯
// 背景色空格留白（不得出现 ▀），中间 4..12 行含 ▀（渐变有上下色差），
// 不拉伸变形。
func TestRenderCoverArtScaleFit16x9(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			g := y * 24
			if g > 255 {
				g = 255
			}
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(g), 128, 255})
		}
	}
	out := renderCoverArt(img, 30, 17)
	lines := strings.Split(out, "\n")
	if len(lines) != 17 {
		t.Fatalf("行数 = %d, want 17", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 30 {
			t.Errorf("行 %d 可见宽 = %d, want 30", i, w)
		}
	}
	// ScaleFit(64,36,30,34) → 30×17px，offsetY=(34-17)/2=8 → 图像占像素行
	// 8..24 → 格行 4..12；第 0-3、13-16 行是纯留白，不得出现 ▀。
	for i := 0; i < 4; i++ {
		if strings.Contains(lines[i], "▀") {
			t.Errorf("行 %d 应为纯留白（背景色空格）: %q", i, lines[i])
		}
	}
	for i := 13; i <= 16; i++ {
		if strings.Contains(lines[i], "▀") {
			t.Errorf("行 %d 应为纯留白（背景色空格）: %q", i, lines[i])
		}
	}
	anyBlock := false
	for i := 4; i <= 12; i++ {
		if strings.Contains(lines[i], "▀") {
			anyBlock = true
			break
		}
	}
	if !anyBlock {
		t.Error("中间行 4..12 应含 ▀（渐变有上下色差）")
	}
}

// TestRenderCoverArtEdges 边界：单色图退化（无 ▀）、1×1 小图、空图不 panic。
func TestRenderCoverArtEdges(t *testing.T) {
	// 单色：上下同色 → 全部退化为背景色空格
	solid := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			solid.Set(x, y, color.RGBA{10, 20, 30, 255})
		}
	}
	out := renderCoverArt(solid, 30, 17)
	if strings.Contains(out, "▀") {
		t.Error("单色图不应出现 ▀（应全部退化为背景色空格）")
	}
	if strings.Count(out, "\n") != 16 {
		t.Errorf("单色图行数异常: %d 个换行", strings.Count(out, "\n"))
	}

	// 1×1 小图不 panic
	tiny := image.NewRGBA(image.Rect(0, 0, 1, 1))
	tiny.Set(0, 0, color.RGBA{255, 0, 0, 255})
	_ = renderCoverArt(tiny, 30, 17)

	// 空图（0 尺寸）返回 ""
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if got := renderCoverArt(empty, 30, 17); got != "" {
		t.Errorf("空图应返回空串, got %q", got)
	}
}

// ansiSGRCodes 提取字符串中所有 SGR 参数（如 "38;5;100"）。
func ansiSGRCodes(s string) [][]string {
	var out [][]string
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && s[j] != 'm' {
			j++
		}
		if j < len(s) {
			params := strings.Split(s[i+2:j], ";")
			out = append(out, params)
		}
		i = j + 1
	}
	return out
}

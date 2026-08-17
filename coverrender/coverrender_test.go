package coverrender

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// ---- 几何 ----

func TestScaleFit(t *testing.T) {
	tests := []struct {
		name         string
		srcW, srcH   int
		boxW, boxH   int
		wantW, wantH int
	}{
		{"方形图在横框", 1000, 1000, 30, 34, 30, 30},
		{"16:9 宽度受限", 1280, 720, 30, 34, 30, 17},
		{"竖图高度受限", 720, 1280, 30, 34, 19, 34},
		{"极小放大不超框", 1, 1, 30, 34, 30, 30},
		{"4K 大图缩小", 3840, 2160, 30, 34, 30, 17},
		{"超宽条钳到 1 行", 10000, 1, 30, 34, 30, 1},
		{"放大填满宽度受限", 20, 20, 30, 34, 30, 30},
		{"竖直比例放大高度受限", 10, 20, 30, 34, 17, 34},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := ScaleFit(tt.srcW, tt.srcH, tt.boxW, tt.boxH)
			if w != tt.wantW || h != tt.wantH {
				t.Errorf("ScaleFit(%d,%d,%d,%d) = %d×%d, want %d×%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, w, h, tt.wantW, tt.wantH)
			}
			if w > tt.boxW || h > tt.boxH {
				t.Errorf("ScaleFit 超出框 %d×%d", w, h)
			}
		})
	}
	if w, h := ScaleFit(0, 10, 30, 34); w != 0 || h != 0 {
		t.Errorf("0 源宽应返回 (0,0), got %d×%d", w, h)
	}
	if w, h := ScaleFit(10, 10, 0, 0); w != 0 || h != 0 {
		t.Errorf("0 框应返回 (0,0), got %d×%d", w, h)
	}
}

// ---- 探测（env 注入，t.Setenv 自动恢复；每个用例先重置缓存）----

func TestDetectModeDefault(t *testing.T) {
	ResetModeCacheForTests()
	// 测试环境 stdin 非 TTY → 恒 half
	if m := DetectMode(); m != ModeHalf {
		t.Fatalf("非交互默认应 half, got %v", m)
	}
}

func TestDetectModeEnvOverride(t *testing.T) {
	for _, tc := range []struct{ env string; want Mode }{
		{"kitty", ModeKitty}, {"sixel", ModeSixel}, {"halfblocks", ModeHalf},
		{"KITTY", ModeKitty}, // 大小写不敏感
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MUSIC_TUI_COVER", tc.env)
			ResetModeCacheForTests()
			if m := DetectMode(); m != tc.want {
				t.Errorf("env=%s → %v, want %v", tc.env, m, tc.want)
			}
		})
	}
	// 非法值：忽略继续（非交互 → half）
	t.Setenv("MUSIC_TUI_COVER", "bogus")
	ResetModeCacheForTests()
	if m := DetectMode(); m != ModeHalf {
		t.Errorf("非法 env 应回退 half, got %v", m)
	}
}

func TestDetectModeHints(t *testing.T) {
	// 模拟交互 stdin（hint 路径可达）
	old := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	defer func() { stdinIsTTY = old }()

	t.Run("KITTY_WINDOW_ID", func(t *testing.T) {
		for _, set := range []string{"1", "12345"} {
			t.Setenv("KITTY_WINDOW_ID", set)
			ResetModeCacheForTests()
			if m := DetectMode(); m != ModeKitty {
				t.Errorf("KITTY_WINDOW_ID=%s → %v, want kitty", set, m)
			}
		}
	})
	t.Run("ghostty", func(t *testing.T) {
		t.Setenv("TERM_PROGRAM", "ghostty")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("TERM_PROGRAM=ghostty → %v, want kitty", m)
		}
	})
	t.Run("foot", func(t *testing.T) {
		t.Setenv("TERM", "foot")
		t.Setenv("TERM_PROGRAM", "")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeSixel {
			t.Errorf("TERM=foot → %v, want sixel", m)
		}
	})
	t.Run("TERM-sixel", func(t *testing.T) {
		t.Setenv("TERM", "xterm-sixel")
		t.Setenv("TERM_PROGRAM", "")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeSixel {
			t.Errorf("TERM=xterm-sixel → %v, want sixel", m)
		}
	})
	t.Run("tmux 优先回退", func(t *testing.T) {
		t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
		t.Setenv("KITTY_WINDOW_ID", "1")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("TMUX 内应 half（即使 KITTY_WINDOW_ID 存在）→ %v", m)
		}
	})
	t.Run("screen TERM 回退", func(t *testing.T) {
		t.Setenv("TERM", "screen-256color")
		t.Setenv("KITTY_WINDOW_ID", "1")
		t.Setenv("TMUX", "")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("TERM=screen* 应 half → %v", m)
		}
	})
}

// ---- 字体 size ----

func TestFontCellSize(t *testing.T) {
	ResetFontCellCacheForTests()
	w, h := FontCellSize()
	if w != 8 || h != 16 {
		t.Errorf("默认字体应 8×16, got %d×%d", w, h)
	}
	t.Setenv("MUSIC_TUI_CELL_W", "9")
	t.Setenv("MUSIC_TUI_CELL_H", "18")
	ResetFontCellCacheForTests()
	w, h = FontCellSize()
	if w != 9 || h != 18 {
		t.Errorf("env 覆盖应 9×18, got %d×%d", w, h)
	}
}

// ---- kitty 序列 ----

func gradientImg(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func TestKittySequence(t *testing.T) {
	img := gradientImg(128, 72) // 16:9
	out := Kitty(img, 30, 17, 8, 16)
	// in-flow 契约：17 行 × 30 列
	lines := strings.Split(out, "\n")
	if len(lines) != 17 {
		t.Fatalf("行数 = %d, want 17", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 30 {
			t.Errorf("行 %d 宽度 = %d, want 30", i, w)
		}
	}
	// 关键控制串
	for _, want := range []string{"\x1b_Ga=t,", "U=1", "\x1b_Ga=p,i=", "q=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("序列缺少 %q", want)
		}
	}
	// 占位符出现在图像子矩形，首尾空格（16:9 全宽→首列是占位符；取竖图验证留白）
	if !strings.Contains(out, "\U0010EEEE") {
		t.Error("应含 U+10EEEE 占位符")
	}
}

func TestKittyPortraitCentering(t *testing.T) {
	img := gradientImg(72, 128) // 竖图
	out := Kitty(img, 30, 17, 8, 16)
	lines := strings.Split(out, "\n")
	// 竖图 ScaleFit(72,128,30,17)：scale=min(30/72,17/128)=0.133 → imgC=10、imgR=17
	// → 图像横跨全部行、占列 10..19 → 每行首列必为空格（留白），子矩形内有占位符
	for i, ln := range lines {
		runes := []rune(ln)
		if runes[0] == 0x10EEEE {
			t.Errorf("行 %d 首列（col 0）应为空格留白，got 占位符（offsetX=%d）", i, 10)
		}
	}
	row := []rune(ansi.Strip(lines[8])) // 剥掉 SGR 后按可见字符索引
	hasPlaceholder := false
	for c := 10; c < 20; c++ {
		if row[c] == 0x10EEEE {
			hasPlaceholder = true
		}
	}
	if !hasPlaceholder {
		t.Error("子矩形（列 10..19）内应有占位符")
	}
}

func TestKittyEdgeCases(t *testing.T) {
	// 1×1 与空图不 panic、非空
	small := image.NewRGBA(image.Rect(0, 0, 1, 1))
	small.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if out := Kitty(small, 30, 17, 8, 16); out == "" {
		t.Error("1×1 图不应输出空串")
	}
	if out := Kitty(gradientImg(0, 0), 30, 17, 8, 16); out == "" {
		t.Error("空图兜底后不应输出空串")
	}
}

// ---- sixel 序列 ----

func TestSixelSequence(t *testing.T) {
	img := gradientImg(128, 72)
	out := Sixel(img, 30, 17, 8, 16)
	if !strings.HasPrefix(out, "\x1bPq") {
		t.Fatalf("应为 DCS 前缀 \\x1bPq, got %q", out[:8])
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Error("应为 DCS 后缀 \\x1b\\")
	}
	// 不含换行（外带定位载荷；行契约由集成层处理）
	if strings.Contains(out, "\n") {
		t.Error("Sixel 载荷不应含换行")
	}
	// 基本结构：色定义 #N;2;R;G;B 与像素字符 '?'..'~'
	if !strings.Contains(out, "#") || !strings.Contains(out, ";2;") {
		t.Error("应有色定义（#N;2;R;G;B）")
	}
	hasPixel := false
	for _, r := range out {
		if r >= '?' && r <= '~' {
			hasPixel = true
			break
		}
	}
	if !hasPixel {
		t.Error("应有 sixel 像素字符（'?'..'~'）")
	}
}

func TestSixelClear(t *testing.T) {
	out := SixelClear(30, 17, 8, 16)
	if !strings.HasPrefix(out, "\x1bPq") || !strings.HasSuffix(out, "\x1b\\") {
		t.Fatalf("SixelClear 应为合法 DCS, got %q", out[:10])
	}
}

// ---- 半块渲染 ----

func TestHalfBlocksGrid(t *testing.T) {
	img := gradientImg(64, 64)
	out := HalfBlocks(img, 30, 17)
	lines := strings.Split(out, "\n")
	if len(lines) != 17 {
		t.Fatalf("行数 = %d, want 17", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 30 {
			t.Errorf("行 %d 宽度 = %d, want 30", i, w)
		}
	}
	if strings.HasSuffix(out, "\n") {
		t.Error("输出不应以换行结尾")
	}
	// 256 色 SGR 检查
	bad := map[string]bool{}
	for _, m := range ansiSGRCodes(out) {
		if len(m) < 2 {
			continue
		}
		if !(m[0] == "38" && m[1] == "5") && !(m[0] == "48" && m[1] == "5") {
			bad[strings.Join(m, ";")] = true
		}
	}
	if len(bad) > 0 {
		t.Errorf("发现非 256 色 SGR: %v", bad)
	}
}

func TestHalfBlocksScaleFit16x9(t *testing.T) {
	img := gradientImg(64, 36) // 16:9
	out := HalfBlocks(img, 30, 17)
	lines := strings.Split(out, "\n")
	// ScaleFit(64,36,30,34)=30×17px → offsetY=(34-17)/2=8 → 图像占像素行 8..24 →
	// 格行 4..12：第 0-3 行与 13-16 行应为纯留白（无 ▀），中间含 ▀
	for i := 0; i < 4; i++ {
		if strings.Contains(lines[i], "▀") {
			t.Errorf("行 %d 应为留白（上边距），含 ▀", i)
		}
	}
	for i := 13; i < 17; i++ {
		if strings.Contains(lines[i], "▀") {
			t.Errorf("行 %d 应为留白（下边距），含 ▀", i)
		}
	}
	found := false
	for i := 4; i <= 12; i++ {
		if strings.Contains(lines[i], "▀") {
			found = true
			break
		}
	}
	if !found {
		t.Error("图像区（第 4..12 行）应有 ▀ 像素")
	}
}

func TestHalfBlocksEdges(t *testing.T) {
	solid := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			solid.Set(x, y, color.RGBA{10, 20, 30, 255})
		}
	}
	out := HalfBlocks(solid, 30, 17)
	if strings.Contains(out, "▀") {
		t.Error("单色图不应出现 ▀（应全部退化为背景色空格）")
	}
	if strings.Count(out, "\n") != 16 {
		t.Errorf("单色图行数异常: %d 个换行", strings.Count(out, "\n"))
	}
	tiny := image.NewRGBA(image.Rect(0, 0, 1, 1))
	tiny.Set(0, 0, color.RGBA{255, 0, 0, 255})
	_ = HalfBlocks(tiny, 30, 17) // 不 panic
	if got := HalfBlocks(gradientImg(0, 0), 30, 17); got != "" {
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
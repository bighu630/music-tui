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
			// 收紧后 KITTY_WINDOW_ID 需能力校验：注入 OK 应答模拟真实 kitty
			lastQueryRaw = "\x1b_Gi=31;OK\x1b\\"
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
	t.Run("foot 默认像素风（sixel 需 env 强制）", func(t *testing.T) {
		t.Setenv("TERM", "foot")
		t.Setenv("TERM_PROGRAM", "")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("TERM=foot → %v, want half（sixel 不自动）", m)
		}
	})
	t.Run("tmux + KITTY_WINDOW_ID → kitty（外层确证）", func(t *testing.T) {
		t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
		t.Setenv("KITTY_WINDOW_ID", "1")
		ResetModeCacheForTests()
		lastQueryRaw = "\x1b_Gi=31;OK\x1b\\"
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("TMUX 内 KITTY_WINDOW_ID=1（外层确证 kitty）→ %v, want kitty", m)
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
// ---- 能力查询与缓存注入 ----

// TestDetectModeCapability 回归：真实终端应答优先于环境提示——
// 用户的 foot 配置 TERM=xterm-256color（无 TERM_PROGRAM），环境提示不可靠，
// 靠启动期能力查询（SetCapability）确认 sixel/kitty。
func TestDetectModeCapability(t *testing.T) {
	old := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	defer func() { stdinIsTTY = old }()
	// 模拟 foot 但 TERM 无 foot 线索
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")

	t.Run("kitty 应答优先", func(t *testing.T) {
		ResetModeCacheForTests()
		SetCapability(ModeKitty)
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("capability=kitty 应优先 → %v", m)
		}
	})
	t.Run("无应答回落环境提示", func(t *testing.T) {
		ResetModeCacheForTests()
		SetCapability(ModeHalf) // 未确认
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("xterm-256color 无应答应 half → %v", m)
		}
	})
	t.Run("sixel 能力应答不自动启用（六边形网格驻留擦除问题）", func(t *testing.T) {
		ResetModeCacheForTests()
		SetCapability(ModeSixel)
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("capability=sixel 不应自动启用 → %v（需 env 强制）", m)
		}
	})
	t.Run("tmux 内即使有应答也回退", func(t *testing.T) {
		t.Setenv("TMUX", "/tmp/tmux-x/0,0,0")
		ResetModeCacheForTests()
		SetCapability(ModeSixel)
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("TMUX 内应 half（capability 也回退）→ %v", m)
		}
	})
	t.Run("env 强制最高优先（sixel 显式开启）", func(t *testing.T) {
		t.Setenv("TMUX", "")
		t.Setenv("MUSIC_TUI_COVER", "sixel")
		ResetModeCacheForTests()
		if m := DetectMode(); m != ModeSixel {
			t.Errorf("MUSIC_TUI_COVER=sixel 应启用 → %v", m)
		}
	})
	t.Run("env 强制 halfblocks 覆盖 capability", func(t *testing.T) {
		t.Setenv("MUSIC_TUI_COVER", "halfblocks")
		ResetModeCacheForTests()
		SetCapability(ModeSixel)
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("MUSIC_TUI_COVER=halfblocks 应覆盖 → %v", m)
		}
	})
}

// TestQueryCapabilityResponse 解析模拟应答（注入 stdin 读取不便——QueryCapability
// 直接读 os.Stdin；此处验证应答判定的纯逻辑：SetCapability 注入等价路径。
// 应答格式参考：foot/xterm DA1 "\x1b[?62;4;22;...c"；kitty 图形应答 "\x1b_Gi=31;OK\x1b\\"。
func TestQueryCapabilityResponseFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp string
		want Mode
	}{
		{"kitty OK 应答", "\x1b_Gi=31;OK\x1b\\", ModeKitty},
		{"DA1 含 4（sixel）", "\x1b[?62;4;22;23c", ModeSixel},
		{"DA1 无 sixel", "\x1b[?62;22;23c", ModeHalf},
		{"空应答", "", ModeHalf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 判定逻辑与 QueryCapability 内联一致（读 stdin 的 IO 部分无法在
			// 单元测试中无副作用模拟，此处覆盖字符串判定）
			s := tc.resp
			got := ModeHalf
			switch {
			case containsStr(s, "OK"):
				got = ModeKitty
			case containsStr(s, ";4"):
				got = ModeSixel
			}
			if got != tc.want {
				t.Errorf("resp %q → %v, want %v", tc.resp, got, tc.want)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestSixelPassAlignment 回归：颜色分离 pass 方案——每个 pass 恰好输出 boxW 个
// 像素字符（列位对齐，RLE 展开后计）。早期实现把同列多色掩码连续输出导致列位
// 膨胀 k 倍、花屏。解析输出：以 '$'（pass 分隔/回行首）与 '-'（band 分隔）切分，
// 像素字符段（'?'..'~'，含 '!'N 重复前缀展开）长度必须恒 == boxW。
func TestSixelPassAlignment(t *testing.T) {
	// 红蓝各半的竖条图：每列同色、列间两色——强制每 band 出现多色多 pass
	img := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			if x < 8 {
				img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	const w, h = 30, 17
	out := Sixel(img, w, h, 8, 16)
	// 去掉 DCS 外壳（\x1bPq / \x1b\），避免头尾转义字节被误计为像素字符
	out = out[len("\x1bPq") : len(out)-len("\x1b\\")]

	// 解析：逐字符扫描，像素字符段按 '$' 与 '-' 分隔；'!' 重复前缀展开为 N 个
	// 像素字符。段长（展开后）必须恒 == boxW。
	boxW := w * 8
	segLen := 0
	segs := map[int]int{}
	flush := func() {
		if segLen > 0 {
			segs[segLen]++
			segLen = 0
		}
	}
	b := []byte(out)
	for i := 0; i < len(b); {
		switch {
		case b[i] == '!':
			// !N<chr>：重复 N 次
			j := i + 1
			n := 0
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				n = n*10 + int(b[j]-'0')
				j++
			}
			if j < len(b) && n > 0 {
				segLen += n
			}
			i = j + 1
		case b[i] >= '?' && b[i] <= '~':
			segLen++
			i++
		case b[i] == '$' || b[i] == '-':
			flush()
			i++
		default:
			i++
		}
	}
	flush()
	for ln, cnt := range segs {
		if ln != boxW {
			t.Errorf("像素字符段长度 = %d（%d 段）, want 恒 %d（列位对齐）", ln, cnt, boxW)
		}
	}
	if len(segs) == 0 {
		t.Error("应有像素字符段")
	}
}


// TestSixelPayloadSize 回归：照片类彩色封面（渐变图）载荷必须被量化上限+RLE
// 压住（早期 216 色全 pass 编码实测 488KB，超长 DCS 与 bubbletea 帧 flush 并发
// 写 stdout 时字节交错损坏 → 终端丢弃 → 无图）。
func TestSixelPayloadSize(t *testing.T) {
	out := Sixel(gradientImg(512, 512), 30, 17, 15, 16) // 对应 450×272 画布
	if len(out) > 200*1024 {
		t.Errorf("sixel 载荷 = %d 字节, want < 200KB（量化上限+RLE 未生效？）", len(out))
	}
	t.Logf("sixel 载荷 = %d 字节", len(out))
}

// TestDetectModeTMUXKittyRelay tmux 内 kitty 中继：默认回退半块；确认外层
// kitty（SetTMUXKittyRelay）后允许 kitty；env 仍最高优先。
func TestDetectModeTMUXKittyRelay(t *testing.T) {
	old := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	defer func() { stdinIsTTY = old }()
	t.Setenv("TMUX", "/tmp/tmux-x/0,0,0")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")

	t.Run("tmux 内 KITTY_WINDOW_ID 透传 → kitty（回归）", func(t *testing.T) {
		t.Setenv("KITTY_WINDOW_ID", "1")
		ResetModeCacheForTests()
		SetTMUXKittyRelay(false)
		lastQueryRaw = "\x1b_Gi=31;OK\x1b\\"
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("tmux + KITTY_WINDOW_ID=1（外层确证 kitty）→ %v, want kitty", m)
		}
	})
	t.Run("tmux 内无 kitty 证据 → 半块", func(t *testing.T) {
		t.Setenv("KITTY_WINDOW_ID", "")
		ResetModeCacheForTests()
		SetTMUXKittyRelay(false)
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("tmux 未确证 kitty → %v, want half", m)
		}
	})
	t.Run("确认外层 kitty 后允许", func(t *testing.T) {
		ResetModeCacheForTests()
		SetCapability(ModeKitty) // 模拟 main 查询 client_termname=*kitty* 后置位
		SetTMUXKittyRelay(true)
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("tmux+kitty 中继确认 → %v, want kitty", m)
		}
	})
	t.Run("env 覆盖仍在 tmux 生效", func(t *testing.T) {
		t.Setenv("MUSIC_TUI_COVER", "kitty")
		ResetModeCacheForTests()
		SetTMUXKittyRelay(false)
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("env=kitty 在 tmux 内应生效 → %v", m)
		}
	})
}

// TestKittyPayloadSizeBound 回归：大 cell（tmux 内 14×27 格 → 显示 420×459px）
// 传输分辨率封顶到基准 8×16 格（240×272px），载荷从 300-500KB 压到 ~100KB 内
//（tmux 中继超大 APC 易丢）。
// TestDetectModeFootLeak 回归：foot 泄漏 KITTY_WINDOW_ID=1（跨会话继承）不应误触发 kitty。
// 仅当 lastQueryRaw 含 OK 或能力确证时才信任。
func TestDetectModeFootLeak(t *testing.T) {
	old := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	defer func() { stdinIsTTY = old }()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "1")
	t.Setenv("TMUX", "")
	t.Run("泄漏 KITTY_WINDOW_ID 无 OK 应为 half", func(t *testing.T) {
		ResetModeCacheForTests()
		lastQueryRaw = ""
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("泄漏 KITTY_WINDOW_ID=1 无 OK → %v, want half", m)
		}
	})
	t.Run("ENODATA 无 OK 仍 half", func(t *testing.T) {
		ResetModeCacheForTests()
		lastQueryRaw = "_Gi=31;ENODATA"
		if m := DetectMode(); m != ModeHalf {
			t.Errorf("ENODATA → %v, want half", m)
		}
	})
	t.Run("含 OK 应 kitty", func(t *testing.T) {
		ResetModeCacheForTests()
		lastQueryRaw = "\x1b_Gi=31;OK\x1b\\"
		if m := DetectMode(); m != ModeKitty {
			t.Errorf("含 OK → %v, want kitty", m)
		}
	})
}

func TestKittyPayloadSizeBound(t *testing.T) {
	for _, tc := range []struct {
		name              string
		cellW, cellH      int
		wantTransmitLimit bool
	}{
		{"大 cell(tmux 14x27) 封顶", 14, 27, true},
		{"标准 8x16 不缩", 8, 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := Kitty(gradientImg(512, 512), 30, 17, tc.cellW, tc.cellH)
			if tc.wantTransmitLimit && len(out) > 120*1024 {
				t.Errorf("载荷 = %d 字节, want ≤120KB（传输封顶未生效）", len(out))
			}
			if !tc.wantTransmitLimit && len(out) > 300*1024 {
				t.Errorf("标准 cell 载荷过大 = %d 字节", len(out))
			}
			t.Logf("%s 载荷 = %d 字节", tc.name, len(out))
		})
	}
}

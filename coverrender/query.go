package coverrender

import (
	"image/color"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 能力查询：不依赖环境变量猜测，直接向终端发协议查询确认能力（TUI 启动前调用）。
// 环境提示（TERM/TERM_PROGRAM）在 foot 配置 TERM=xterm-256color 等场景下不可靠，
// 以终端实际应答为准。查询只在启动早期、bubbletea 接管 stdin 之前执行——运行期
// 查询会与 TUI 输入循环抢读（响应字节会被当作按键事件）。

var capabilityMode Mode // 启动期查询结果（ModeHalf=未确认，回落环境提示）

// lastQueryRaw 最近一次能力查询的原始应答（诊断用，main 日志输出）。
var lastQueryRaw string

// LastQueryRaw 返回最近一次能力查询的原始应答文本（转义序列原样）。
func LastQueryRaw() string { return lastQueryRaw }

// fontCellSizeRe 匹配 CSI 16t 应答：\x1b[6;<行高px>;<列宽px>t
// （注意：子码 6 才是 cell 尺寸；子码 4 是 CSI 14t 窗口像素的应答）
var fontCellSizeRe = regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`)

// SetCapability 注入启动期能力查询结果（main 在 TUI 启动前调用 QueryCapability
// 后写入；测试可直接注入）。
func SetCapability(m Mode) {
	capabilityMode = m
}

// QueryCapability 向终端发起能力查询（best-effort，timeout 超时返回 half,false）：
//   - DA1（\x1b[c）：响应含 ";4"（sixel 属性）→ ModeSixel
//   - kitty 图形查询（\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA）：f=24 需 3 字节，AAAA 刚好满足
//     （原 f=32 需 4 字节，AAAA 解码仅 3 字节 → kitty 回 ENODATA 误判 halfblocks；参考 go-termimg）
//     响应含 "OK" → ModeKitty（kitty 对 a=q 应答：\x1b_Gi=31;OK\x1b\\，不支持时 ENOSUPPORT）
//     兜底：含 Gi 且非 ENOSUPPORT 的 ENODATA 亦视为 kitty（仅 kitty 会回 Gi，避免载荷微差再次回落）
//   - CSI 16t（\x1b[16t）：响应 \x1b[4;<cellH>;<cellW>t —— 终端自报字符格像素，
//     与六边形/kitty 渲染同一像素空间（ioctl 的窗口物理像素在 wayland 缩放下
//     与终端实际渲染 cell 不一致，导致六边形图像按错误像素高度绘制、行数溢出）。
//     解析成功后经 SetFontCellSize 注入。
//
// 必须在不支持协议的终端上无副作用：三者都是查询（不改状态），非支持终端忽略或
// 无应答（超时回落）。stdin 非 TTY（测试/CI/管道）直接返回 (ModeHalf, false)。
func QueryCapability(timeout time.Duration) (Mode, bool) {
	if !stdinIsTTY() {
		return ModeHalf, false
	}
	// 并发发出三个查询（终端串行应答）
	_, _ = io.WriteString(os.Stdout, "\x1b[c")
	_, _ = io.WriteString(os.Stdout, "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\") // f=24 对应 3 字节，AAAA 刚好满足，避免 ENODATA；与 go-termimg 一致
	_, _ = io.WriteString(os.Stdout, "\x1b[16t")

	s := string(readResponse(timeout))
	lastQueryRaw = s
	// CSI 16t：终端自报字符格像素 \x1b[6;<cellH>;<cellW>t
	if m := fontCellSizeRe.FindStringSubmatch(s); m != nil {
		if h, err1 := strconv.Atoi(m[1]); err1 == nil {
			if w, err2 := strconv.Atoi(m[2]); err2 == nil && w > 0 && h > 0 {
				SetFontCellSize(w, h) // 16t 是权威来源
			}
		}
	}
	switch {
	case strings.Contains(s, "OK"): // kitty 图形协议应答（优先）
		return ModeKitty, true
	case (strings.Contains(s, "Gi=") || strings.Contains(s, "_Gi")) && !strings.Contains(s, "ENOSUPPORT"):
		// 兜底：kitty 独有 Gi 前缀，ENODATA 亦含 Gi（仅 kitty 会回 Gi），避免载荷微差回落 halfblocks
		return ModeKitty, true
	case strings.Contains(s, ";4"): // DA1 含 sixel 属性（形如 \x1b[?62;4;22;…c）
		return ModeSixel, true
	}
	return ModeHalf, false
}
// dsrRe 匹配 DSR 光标位置应答：\x1b[<row>;<col>R
var dsrRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// CalibrateCellSize 用 DSR 实测终端字符格像素高（不依赖 CSI 16t——foot 实测不回
// 16t）。原理：在 (1,1) 画一块已知像素高（calPx）的六边形 → 查询光标位置 →
// 图像占用行数 = row-1 → cellH = ceil(calPx/占用行数)。结果与六边形渲染同一像素
// 空间，任何终端都准确。仅校准高度（宽度沿用 ioctl/16t/env 的现有推算——纵向
// 行数溢出是六边形错位的主因；横向 30 列宽度的 ±几像素不影响对齐）。
//
// 必须在 TUI 启动前调用（raw 模式读 stdin 阶段）；测试图绘制在 (1,1) 主屏左上角，
// TUI 进入 alt screen 后自然被覆盖。返回 (cellW, cellH, 占用行数, ok) 供日志。
func CalibrateCellSize(timeout time.Duration) (w, h, rows int, ok bool) {
	if !stdinIsTTY() {
		return 0, 0, 0, false
	}
	const calPx = 96 // 测试图像素高（96 = 常见 cellH 的整数倍，占用行数易算）
	_, _ = io.WriteString(os.Stdout, "\x1b[1;1H")
	test := Sixel(solidImage(calPx, calPx, color.RGBA{0, 0, 0, 255}), 1, 1, calPx, calPx)
	_, _ = io.WriteString(os.Stdout, test)
	_, _ = io.WriteString(os.Stdout, "\x1b[6n") // DSR 光标位置

	s := string(readResponse(timeout))
	m := dsrRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, false
	}
	row, _ := strconv.Atoi(m[1])
	rows = row - 1
	if rows <= 0 {
		return 0, 0, rows, false
	}
	h = (calPx + rows - 1) / rows // ceil
	if h < 5 || h > 60 {
		return 0, 0, rows, false // 异常值，拒绝
	}
	// 宽度沿用现有推算链（env > 16t > ioctl > 默认），不触发 FontCellSize 缓存
	w = 8
	if qw, qh := queryFontW, queryFontH; qw > 0 && qh > 0 {
		w = qw
	} else if iw, _, iok := ioctlCellSize(); iok {
		w = iw
	}
	if ew, eok := envInt("MUSIC_TUI_CELL_W"); eok && ew > 0 {
		w = ew
	}
	SetCalibratedCellSize(w, h)
	// 清掉测试图（背景色覆盖）
	clear := Sixel(solidImage(calPx, calPx, color.RGBA{0, 0, 0, 255}), 1, 1, calPx, calPx)
	_, _ = io.WriteString(os.Stdout, "\x1b[1;1H"+clear)
	return w, h, rows, true
}

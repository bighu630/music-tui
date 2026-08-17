package coverrender

import (
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
//   - kitty 图形查询（\x1b_Gi=31,a=q,s=1,v=1）：响应含 "OK" → ModeKitty
//     （kitty 对 a=q 的应答格式：\x1b_Gi=31;OK\x1b\\，不支持时 ENOSUPPORT/EBADMSG）
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
	_, _ = io.WriteString(os.Stdout, "\x1b_Gi=31,s=1,v=1,a=q\x1b\\")
	_, _ = io.WriteString(os.Stdout, "\x1b[16t")

	s := string(readResponse(timeout))
	// CSI 16t：终端自报字符格像素 \x1b[6;<cellH>;<cellW>t
	if m := fontCellSizeRe.FindStringSubmatch(s); m != nil {
		if h, err1 := strconv.Atoi(m[1]); err1 == nil {
			if w, err2 := strconv.Atoi(m[2]); err2 == nil && w > 0 && h > 0 {
				SetFontCellSize(w, h)
			}
		}
	}
	switch {
	case strings.Contains(s, "OK"): // kitty 图形协议应答
		return ModeKitty, true
	case strings.Contains(s, ";4"): // DA1 含 sixel 属性（形如 \x1b[?62;4;22;…c）
		return ModeSixel, true
	}
	return ModeHalf, false
}
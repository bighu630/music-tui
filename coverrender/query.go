package coverrender

import (
	"io"
	"os"
	"strings"
	"time"
)

// 能力查询：不依赖环境变量猜测，直接向终端发协议查询确认能力（TUI 启动前调用）。
// 环境提示（TERM/TERM_PROGRAM）在 foot 配置 TERM=xterm-256color 等场景下不可靠，
// 以终端实际应答为准。查询只在启动早期、bubbletea 接管 stdin 之前执行——运行期
// 查询会与 TUI 输入循环抢读（响应字节会被当作按键事件）。

var capabilityMode Mode // 启动期查询结果（0=未查询/不支持，此时回落环境提示）

// SetCapability 注入启动期能力查询结果（main 在 TUI 启动前调用 QueryCapability
// 后写入；测试可直接注入）。ModeHalf 表示"未确认"，DetectMode 回落环境提示。
func SetCapability(m Mode) {
	capabilityMode = m
}

// QueryCapability 向终端发起能力查询（best-effort，timeout 超时返回 half,false）：
//   - DA1（\x1b[c）：响应含 ";4"（sixel 属性）→ ModeSixel
//   - kitty 图形查询（\x1b_Gi=31,a=q,s=1,v=1）：响应含 "OK" → ModeKitty
//     （kitty 对 a=q 的应答格式：\x1b_Gi=31;OK\x1b\\，不支持时 ENOSUPPORT/EBADMSG）
//
// 必须在不支持协议的终端上无副作用：两者都是查询（不改状态），非支持终端忽略或
// 无应答（超时回落）。stdin 非 TTY（测试/CI/管道）直接返回 (ModeHalf, false)。
func QueryCapability(timeout time.Duration) (Mode, bool) {
	if !stdinIsTTY() {
		return ModeHalf, false
	}
	// 并发发出两个查询（终端串行应答）
	_, _ = io.WriteString(os.Stdout, "\x1b[c")
	_, _ = io.WriteString(os.Stdout, "\x1b_Gi=31,s=1,v=1,a=q\x1b\\")

	resp := readResponse(timeout)
	s := string(resp)
	switch {
	case strings.Contains(s, "OK"): // kitty 图形协议应答
		return ModeKitty, true
	case strings.Contains(s, ";4"): // DA1 含 sixel 属性（xterm 应答形如 \x1b[?62;4;22;…c）
		return ModeSixel, true
	}
	return ModeHalf, false
}

// readResponse 以 timeout 上限读取 stdin 应答（一次性；等不到就放弃）。
// 读取必须在原始输入接管之前：调用方保证（TUI 启动前）。
func readResponse(timeout time.Duration) []byte {
	// 起读 goroutine，select 超时放弃（读不会永久卡住调用方；goroutine 挂起
	// 在后续 stdin 被 TUI 接管时自然结束）。
	type res struct {
		buf []byte
	}
	ch := make(chan res, 1)
	go func() {
		var buf [256]byte
		n, _ := os.Stdin.Read(buf[:])
		ch <- res{buf: buf[:n]}
	}()
	select {
	case r := <-ch:
		return r.buf
	case <-time.After(timeout):
		return nil
	}
}
/*
Package csi provides CSI (Control Sequence Introducer) query functions for terminal capabilities
*/
package csi

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// QueryTimeout is the default timeout for CSI queries
const QueryTimeout = 100 * time.Millisecond

// readQueryResponse 发送查询并同步读取响应（QueryTimeout 超时）。
// 用 select(2) 手动超时：Go 的 SetReadDeadline 在 term.MakeRaw 之后的 fd 上
// 失效（poller 与 termios 交互问题，实测永久阻塞），select 不受 termios 影响。
// 同时避免原实现无超时 goroutine 读 /dev/tty 导致的读劫持（泄漏 goroutine
// 与 TUI 输入抢同一终端，吞掉后续按键）。
func readQueryResponse(tty *os.File) string {
	fd := int(tty.Fd())
	var rfds syscall.FdSet
	if fd/64 >= len(rfds.Bits) {
		return ""
	}
	rfds.Bits[fd/64] |= 1 << uint(fd%64)
	tv := syscall.NsecToTimeval(int64(QueryTimeout))
	n, err := syscall.Select(fd+1, &rfds, nil, nil, &tv)
	if n <= 0 || err != nil {
		return "" // 超时或无就绪
	}
	buf := make([]byte, 64)
	n, err = syscall.Read(fd, buf)
	if err != nil || n <= 0 {
		return ""
	}
	return string(buf[:n])
}

// QueryTextAreaSizeInPixels queries text area size in pixels using CSI 14t
// returns: width and height in pixels, or 0,0 if query fails
func QueryTextAreaSizeInPixels() (width, height int, ok bool) {
	query := "\x1b[14t"
	if inTmux() {
		query = wrapTmuxPassthrough(query)
	}

	// Open controlling terminal
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, false
	}
	defer tty.Close()

	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return 0, 0, false
	}
	defer term.Restore(int(tty.Fd()), oldState)

	if _, err := tty.WriteString(query); err != nil {
		return 0, 0, false
	}

	response := readQueryResponse(tty)
	// Parse response: CSI 4 ; height ; width t
	if strings.Contains(response, "[4;") {
		parts := strings.Split(response, ";")
		if len(parts) >= 3 {
			fmt.Sscanf(parts[1], "%d", &height)
			fmt.Sscanf(parts[2], "%dt", &width)
			return width, height, true
		}
	}
	return 0, 0, false
}

// QueryCharacterCellSizeInPixels queries character cell size in pixels using CSI 16t
// returns: width and height in pixels per character, or 0,0,false if query fails
func QueryCharacterCellSizeInPixels() (width, height int, ok bool) {
	query := "\x1b[16t"
	if inTmux() {
		query = wrapTmuxPassthrough(query)
	}

	// Open controlling terminal
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, false
	}
	defer tty.Close()

	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return 0, 0, false
	}
	defer term.Restore(int(tty.Fd()), oldState)

	if _, err := tty.WriteString(query); err != nil {
		return 0, 0, false
	}

	response := readQueryResponse(tty)
	// Parse response: CSI 6 ; height ; width t
	if strings.Contains(response, "[6;") && strings.Contains(response, "t") {
		parts := strings.Split(response, ";")
		if len(parts) >= 3 {
			fmt.Sscanf(parts[1], "%d", &height)
			fmt.Sscanf(parts[2], "%dt", &width)
		}
		if width > 0 && height > 0 {
			return width, height, true
		}
	}
	return 0, 0, false
}

// QueryCSI18t queries text area size in characters using CSI 18t

// QueryXTSMGRAPHICS queries Sixel graphics geometry using XTSMGRAPHICS (xterm 344+)
// returns: width and height in pixels, and success status
func QueryXTSMGRAPHICS() (width, height int, ok bool) {
	// Pi=2 (Sixel), Pa=1 (read), Pv=0
	query := "\x1b[?2;1;0S"
	if inTmux() {
		query = wrapTmuxPassthrough(query)
	}

	// Open controlling terminal
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, false
	}
	defer tty.Close()

	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return 0, 0, false
	}
	defer term.Restore(int(tty.Fd()), oldState)

	if _, err := tty.WriteString(query); err != nil {
		return 0, 0, false
	}

	response := readQueryResponse(tty)
	// Parse response: CSI ? 2 ; Ps ; width ; height S
	// Ps=0 means success
	if strings.Contains(response, "?2;") && strings.Contains(response, "S") {
		parts := strings.Split(response, ";")
		if len(parts) >= 4 {
			var status int
			fmt.Sscanf(parts[1], "%d", &status)
			if status == 0 { // 0 = success
				fmt.Sscanf(parts[2], "%d", &width)
				fmt.Sscanf(parts[3], "%dS", &height)
				return width, height, true
			}
		}
	}
	return 0, 0, false
}

// QueryWindowSize queries the terminal for its current window size
func QueryWindowSize() (cols, rows int, err error) {
	return term.GetSize(int(os.Stdin.Fd()))
}

// QueryFontSize queries the font size from pixel and character dimensions
// This is useful when combining CSI 14t and CSI 18t results
func QueryFontSize() (fontWidth, fontHeight int, ok bool) {
	// Get pixel size from text area (CSI 14t)
	pixelWidth, pixelHeight, ok := QueryTextAreaSizeInPixels()
	if !ok {
		return 0, 0, false
	}
	// Get character size from text area (same as CSI 18t)
	charCols, charRows, err := QueryWindowSize()
	if err != nil || charCols <= 0 || charRows <= 0 {
		return 0, 0, false
	}

	if pixelWidth <= 0 || pixelHeight <= 0 || charCols <= 0 || charRows <= 0 {
		return 0, 0, false
	}

	fontWidth = pixelWidth / charCols
	fontHeight = pixelHeight / charRows

	// font sizes should be reasonable (between 4 and 50 pixels)
	if fontWidth < 4 || fontWidth > 50 || fontHeight < 4 || fontHeight > 50 {
		return 0, 0, false
	}

	return fontWidth, fontHeight, true
}

// QuerySupported checks if a terminal likely supports CSI queries
// This is a heuristic based on terminal type and environment
func QuerySupported() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	// Some terminals are known to not support or have disabled CSI queries
	termProgram := os.Getenv("TERM_PROGRAM")
	switch termProgram {
	case "Apple_Terminal":
		// Apple Terminal often has CSI queries disabled for security
		return false
	case "vscode":
		// VS Code integrated terminal may not support all CSI queries
		return false
	}
	// Check for known problematic environments
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

// inTmux checks if running inside tmux or if tmux mode is forced
func inTmux() bool {
	return os.Getenv("TMUX") != "" || os.Getenv("TERM_PROGRAM") == "tmux"
}

// wrapTmuxPassthrough wraps an escape sequence for tmux passthrough if needed
func wrapTmuxPassthrough(output string) string {
	// tmux passthrough format: \ePtmux;\e{escaped_sequence}\e\\
	// All \e (ESC) characters in the sequence must be doubled
	return "\x1bPtmux;\x1b" + strings.ReplaceAll(output, "\x1b", "\x1b\x1b") + "\x1b\\"
}

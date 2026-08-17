//go:build !windows

package coverrender

import "golang.org/x/sys/unix"

// ioctlCellSize 通过 TIOCGWINSZ 获取窗口像素尺寸与行列数，推算每字符格像素。
func ioctlCellSize() (cellW, cellH int, ok bool) {
	ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ) // stdout
	if err != nil {
		return 0, 0, false
	}
	// xpixel/ypixel 部分终端为 0（未报告）→ 无法推算
	if ws.Xpixel <= 0 || ws.Ypixel <= 0 || ws.Col <= 0 || ws.Row <= 0 {
		return 0, 0, false
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
}
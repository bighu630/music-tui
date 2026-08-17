//go:build windows

package coverrender

// ioctlCellSize windows 无 TIOCGWINSZ：恒失败，回落默认字体尺寸。
func ioctlCellSize() (cellW, cellH int, ok bool) {
	return 0, 0, false
}
package coverrender

import (
	"os"
	"strconv"
	"sync"
)

var (
	fontOnce sync.Once
	fontW    int
	fontH    int

	// queryFontW/H 启动期 CSI 16t 查询结果（终端自报字符格像素，与六边形/kitty
	// 渲染同一像素空间；-1 = 未查询/未得到）。ioctl 的窗口物理像素在 wayland
	// 缩放下与终端实际渲染 cell 不一致（foot 实测 15×29 偏大导致六边形行数溢出）。
	queryFontW = -1
	queryFontH = -1
)

// SetFontCellSize 注入启动期 CSI 16t 查询结果（main 在 TUI 前调用
// QueryCapability 后自动注入；测试可直接调用）。
func SetFontCellSize(w, h int) {
	queryFontW, queryFontH = w, h
}

// FontCellSize 返回终端字符格的像素宽高（kitty/sixel 把 cell 换算成像素用）。
// 优先级：环境变量 MUSIC_TUI_CELL_W / MUSIC_TUI_CELL_H（两者都合法时）>
// 启动期 CSI 16t 终端自报值 > ioctl(TIOCGWINSZ) 窗口像素÷行列推算 > 默认 (8,16)。
// 结果进程级缓存；测试改 env 后调 ResetFontCellCacheForTests。
func FontCellSize() (w, h int) {
	fontOnce.Do(func() { fontW, fontH = computeFontCellSize() })
	return fontW, fontH
}

func computeFontCellSize() (int, int) {
	// 1. 环境变量
	ew, okw := envInt("MUSIC_TUI_CELL_W")
	eh, okh := envInt("MUSIC_TUI_CELL_H")
	if okw && okh && ew > 0 && eh > 0 {
		return ew, eh
	}
	// 2. 启动期 CSI 16t 终端自报
	if queryFontW > 0 && queryFontH > 0 {
		return queryFontW, queryFontH
	}
	// 3. ioctl 窗口像素 ÷ 行列
	if w, h, ok := ioctlCellSize(); ok {
		return w, h
	}
	// 4. 默认
	return 8, 16
}

// ioctlCellSize 通过 TIOCGWINSZ 获取窗口像素尺寸与行列数，推算每字符格像素
// （平台相关：unix 实现见 font_unix.go，windows 恒失败回落默认）。

// envInt 读取正整数环境变量；缺失或非法返回 (0, false)。
func envInt(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ResetFontCellCacheForTests 清空字体尺寸缓存（测试改 env 后调用）。
func ResetFontCellCacheForTests() {
	fontOnce = sync.Once{}
	fontW, fontH = 0, 0
	queryFontW, queryFontH = -1, -1
}
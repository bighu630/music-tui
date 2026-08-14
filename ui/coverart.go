package ui

import (
	"fmt"
	"image"
	"strings"
)

// renderCoverArt 把封面图片渲染为固定 w 列 × h 行的半块字符画：
// 每个字符单元 = 图片 1×2 像素（上/下各 1 像素），上像素作前景、
// 下像素作背景、字符用 ▀（上半块）叠加显示；上下同色时退化为
// 背景色空格（省字节）。颜色量化为 256 色（6×6×6 立方体）以兼容
// tmux/老终端。
//
// 输出恒定 w 列 × h 行（无尾随换行、无光标序列），与终端特性无关，
// 尺寸固定后不再依赖 go-termimg（其 mosaic 尺寸单位为像素、tmux 下
// 渲染输出行数不稳定，实测 1~17 行漂移导致布局崩坏）。
func renderCoverArt(img image.Image, w, h int) string {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return ""
	}
	// 最近邻采样：目标像素 (x, y) ← 源像素按比例映射（覆盖 0 缩放除零）
	sample := func(x, y int) (uint8, uint8, uint8) {
		sx := x * srcW / w
		sy := y * srcH / (h * 2)
		if sx >= srcW {
			sx = srcW - 1
		}
		if sy >= srcH {
			sy = srcH - 1
		}
		r, g, b, _ := img.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
	var sb strings.Builder
	sb.Grow(w * h * 20)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tr, tg, tb := sample(x, y*2) // 上像素
			br, bg, bb := sample(x, y*2+1)
			// 上下像素色差小于阈值时视为同色：退化为背景色空格，
			// 避免近色噪点产生大量 ▀ 并节省输出字节。
			if diff(tr, br)+diff(tg, bg)+diff(tb, bb) < 24 {
				fmt.Fprintf(&sb, "\x1b[48;5;%dm \x1b[0m", color256(br, bg, bb))
			} else {
				fmt.Fprintf(&sb, "\x1b[38;5;%dm\x1b[48;5;%dm▀\x1b[0m",
					color256(tr, tg, tb), color256(br, bg, bb))
			}
		}
		if y < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// diff 返回两通道差的绝对值（uint8 差值）。
func diff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// color256 把 RGB 量化为 256 色 6×6×6 立方体下标（16 + r*36 + g*6 + b）。
func color256(r, g, b uint8) int {
	return 16 + int(r)*6/256*36 + int(g)*6/256*6 + int(b)*6/256
}

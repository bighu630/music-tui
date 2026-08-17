package coverrender

import (
	"fmt"
	"image"
	"strings"
)

// HalfBlocks 把源图渲染为 w 列 × h 行的半块字符画（任何终端可用，纯 256 色 SGR，
// 布局恒定）。每个字符单元 = 图片 1×2 像素（上/下各 1 像素），上像素作前景、
// 下像素作背景、字符用 ▀（上半块）叠加显示；上下同色时退化为背景色空格（省字节）。
//
// 缩放语义：图片在 w×(h*2) 像素框（2 像素/格纵向）内按 ScaleFit（等比例 contain）
// 取整居中，框内超出图像矩形的像素输出背景色空格留白（不超出、不裁切、不畸变）；
// 像素用双线性采样（抗摩尔纹/防糊——"高清"感知的来源）。
//
// 输出恒定 w 列 × h 行（无尾随换行、无光标序列），与终端特性无关。
func HalfBlocks(img image.Image, w, h int) string {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return ""
	}
	// 源图等比例缩放到 w×(h*2) 像素框内并居中
	imgW, imgH := ScaleFit(srcW, srcH, w, h*2)
	if imgW <= 0 || imgH <= 0 {
		return ""
	}
	offsetX, offsetY := CenterIn(imgW, imgH, w, h*2)

	pix := func(x, y int) (float64, float64, float64) {
		r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		return float64(r), float64(g), float64(b)
	}
	// 双线性采样：图像局部坐标 (fx, fy) → 源坐标；越界钳位到最后一像素。
	sample := func(fx, fy float64) (uint8, uint8, uint8) {
		sx := fx * float64(srcW) / float64(imgW)
		sy := fy * float64(srcH) / float64(imgH)
		if sx < 0 {
			sx = 0
		}
		if sx >= float64(srcW-1) {
			sx = float64(srcW - 1)
		}
		if sy < 0 {
			sy = 0
		}
		if sy >= float64(srcH-1) {
			sy = float64(srcH - 1)
		}
		x0, y0 := int(sx), int(sy)
		x1, y1 := x0+1, y0+1
		if x1 >= srcW {
			x1 = srcW - 1
		}
		if y1 >= srcH {
			y1 = srcH - 1
		}
		dx, dy := sx-float64(x0), sy-float64(y0)
		w00 := (1 - dx) * (1 - dy)
		w10 := dx * (1 - dy)
		w01 := (1 - dx) * dy
		w11 := dx * dy
		r00, g00, b00 := pix(x0, y0)
		r10, g10, b10 := pix(x1, y0)
		r01, g01, b01 := pix(x0, y1)
		r11, g11, b11 := pix(x1, y1)
		return uint8((w00*r00 + w10*r10 + w01*r01 + w11*r11) / 256),
			uint8((w00*g00 + w10*g10 + w01*g01 + w11*g11) / 256),
			uint8((w00*b00 + w10*b10 + w01*b01 + w11*b11) / 256)
	}

	var sb strings.Builder
	sb.Grow(w * h * 20)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var tr, tg, tb, br, bg, bb uint8
			fx := x - offsetX
			if fx >= 0 && fx < imgW {
				if fy := y*2 - offsetY; fy >= 0 && fy < imgH {
					tr, tg, tb = sample(float64(fx), float64(fy))
				}
				if fy := y*2 + 1 - offsetY; fy >= 0 && fy < imgH {
					br, bg, bb = sample(float64(fx), float64(fy))
				}
			}
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
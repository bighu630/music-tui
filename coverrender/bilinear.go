package coverrender

import (
	"image"
	"image/color"
	"image/draw"
)

// solidImage 生成 w×h 的纯色图。
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}

// bilinearScale 把源图双线性缩放到 dstW×dstH（不保持比例——调用方先按 ScaleFit
// 算好目标尺寸；越界钳位，16 位色精度）。
func bilinearScale(img image.Image, dstW, dstH int) *image.RGBA {
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW < 1 || srcH < 1 {
		return solidImage(dstW, dstH, color.RGBA{0, 0, 0, 255})
	}
	out := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	pix := func(x, y int) (float64, float64, float64) {
		r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return float64(r), float64(g), float64(bb)
	}
	for dy := 0; dy < dstH; dy++ {
		sy := float64(dy) * float64(srcH) / float64(dstH)
		if sy < 0 {
			sy = 0
		}
		if sy >= float64(srcH-1) {
			sy = float64(srcH - 1)
		}
		y0 := int(sy)
		y1 := y0 + 1
		if y1 >= srcH {
			y1 = srcH - 1
		}
		dyw := sy - float64(y0)
		for dx := 0; dx < dstW; dx++ {
			sx := float64(dx) * float64(srcW) / float64(dstW)
			if sx < 0 {
				sx = 0
			}
			if sx >= float64(srcW-1) {
				sx = float64(srcW - 1)
			}
			x0 := int(sx)
			x1 := x0 + 1
			if x1 >= srcW {
				x1 = srcW - 1
			}
			dxw := sx - float64(x0)
			r00, g00, b00 := pix(x0, y0)
			r10, g10, b10 := pix(x1, y0)
			r01, g01, b01 := pix(x0, y1)
			r11, g11, b11 := pix(x1, y1)
			w00 := (1 - dxw) * (1 - dyw)
			w10 := dxw * (1 - dyw)
			w01 := (1 - dxw) * dyw
			w11 := dxw * dyw
			out.SetRGBA(dx, dy, color.RGBA{
				uint8((w00*r00 + w10*r10 + w01*r01 + w11*r11) / 256),
				uint8((w00*g00 + w10*g10 + w01*g01 + w11*g11) / 256),
				uint8((w00*b00 + w10*b10 + w01*b01 + w11*b11) / 256),
				255,
			})
		}
	}
	return out
}

// compositeFrame 把源图等比例缩放后合成到 w×h 全帧画布（背景深色 RGB{16,16,16}）：
// ScaleFit 保持比例、居中，框内超出图像处的留边即等比例 letterbox（不裁切、不超出）。
// 供 sixel 使用：整块画布输出天然占满全帧，避免逐格透明背景兼容问题。
func compositeFrame(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	canvas := solidImage(w, h, color.RGBA{16, 16, 16, 255})
	if srcW <= 0 || srcH <= 0 {
		return canvas
	}
	dw, dh := ScaleFit(srcW, srcH, w, h)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	scaled := bilinearScale(img, dw, dh)
	ox, oy := CenterIn(dw, dh, w, h)
	if ox < 0 {
		ox = 0
	}
	if oy < 0 {
		oy = 0
	}
	draw.Draw(canvas, image.Rect(ox, oy, ox+dw, oy+dh), scaled, image.Point{}, draw.Src)
	return canvas
}
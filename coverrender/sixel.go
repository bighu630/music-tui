package coverrender

import (
	"fmt"
	"image"
	"image/color"
	"strings"
)

// Sixel 生成 sixel 图形协议的全帧画布 DCS 序列（供外带绝对定位写出，不内联于布局）。
//
// 布局契约：源图等比例缩放到 width*cellW × height*cellH 像素的全帧画布
// （留边填深色 RGB{16,16,16} letterbox，不裁切、不超出、比例不变），整块编码为
// sixel DCS。返回串不含尾随换行（不构成 in-flow 行），集成层把它写到屏幕坐标
// (row,col) 后图像恰好铺满 width×height 个字符格。换歌/回退时用 SixelClear 清除。
//
// 编码算法（DECDIS 六像素）：每 6 像素一 band；对 band 内每个像素列，按该列 6 行
// 的**每种颜色**分别输出色选 + 位掩码（bit i = 该列第 i 行是否命中该色）——同列多色
// 必须逐色输出，否则非首行色像素会丢失（早期实现的缺陷，导致图像空洞/不可见）。
// 颜色量化为 6×6×6 立方体（216 色）。
func Sixel(img image.Image, width, height, cellW, cellH int) string {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		img = solidImage(1, 1, color.RGBA{0, 0, 0, 255})
		srcW, srcH = 1, 1
	}
	if cellW <= 0 {
		cellW = 8
	}
	if cellH <= 0 {
		cellH = 16
	}
	if width < 1 {
		width = DefaultW
	}
	if height < 1 {
		height = DefaultH
	}
	boxW, boxH := width*cellW, height*cellH

	frame := compositeFrame(img, boxW, boxH)

	var sb strings.Builder
	sb.WriteString("\x1bPq")
	cur := -1 // 当前已选中的颜色槽
	def := [256]bool{}
	for by := 0; by < boxH; by += 6 {
		sb.WriteByte('$') // 回车到左边界（同一 band 新起一行）
		rows := 6
		if by+rows > boxH {
			rows = boxH - by
		}
		for x := 0; x < boxW; x++ {
			// 统计该列 band 各行颜色：按出现顺序（从上到下）逐色输出
			var colColors []int
			var rowColor [6]int
			for r := 0; r < rows; r++ {
				c := quantizeSixel(frame.RGBAAt(x, by+r))
				rowColor[r] = c
				seen := false
				for _, cc := range colColors {
					if cc == c {
						seen = true
						break
					}
				}
				if !seen {
					colColors = append(colColors, c)
				}
			}
			if len(colColors) == 0 {
				continue
			}
			for _, c := range colColors {
				if c != cur {
					cur = c
					if !def[c] {
						def[c] = true
						r6, g6, b6 := c/36, (c/6)%6, c%6
						toPct := func(v int) int { return v * 100 / 255 }
						sb.WriteString(fmt.Sprintf("#%d;2;%d;%d;%d", c,
							toPct(r6*255/5), toPct(g6*255/5), toPct(b6*255/5)))
					} else {
						sb.WriteString(fmt.Sprintf("#%d", c))
					}
				}
			}
			// 该列各色掩码：bit i = 第 i 行命中该色
			for i, c := range colColors {
				var mask byte
				for r := 0; r < rows; r++ {
					if rowColor[r] == c {
						mask |= 1 << r
					}
				}
				if mask != 0 {
					sb.WriteByte('?' + mask) // 0x3F + 6 位掩码 → 可见字符
				}
				_ = i
			}
		}
		if by+6 < boxH {
			sb.WriteByte('-') // 移到下一 band
		}
	}
	sb.WriteString("\x1b\\")
	return sb.String()
}

// SixelClear 生成本背景色的全帧 sixel DCS（清除该区域已绘制的旧图像）。
// width/height 为要覆盖的字符格数（含 cellW/cellH 像素换算）。
func SixelClear(width, height, cellW, cellH int) string {
	bg := solidImage(width*cellW, height*cellH, color.RGBA{16, 16, 16, 255})
	return Sixel(bg, width, height, cellW, cellH)
}

// quantizeSixel 6×6×6 色彩立方体量化（216 色，0..215）。
func quantizeSixel(c color.RGBA) int {
	r6 := int(c.R) * 6 / 256
	g6 := int(c.G) * 6 / 256
	b6 := int(c.B) * 6 / 256
	return r6*36 + g6*6 + b6
}
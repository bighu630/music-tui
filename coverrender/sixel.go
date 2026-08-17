package coverrender

import (
	"fmt"
	"image"
	"image/color"
	"strings"
)

// sixelBandColorLimit 单 band 允许的最大颜色数（pass 数 = 颜色数；照片类封面全
// 216 色量化时 pass 可达数十趟，载荷数倍膨胀——实测 450×272 画布 488KB，超长
// DCS 与 bubbletea ticker 帧 flush 并发写 stdout 时字节交错、损坏后被终端丢弃）。
// 限制后载荷稳定 ≤ 上限×boxW×band 数，并配合 RLE 进一步压缩。
const sixelBandColorLimit = 8

// Sixel 生成 sixel 图形协议的全帧画布 DCS 序列（供外带绝对定位写出，不内联于布局）。
//
// 布局契约：源图等比例缩放到 width*cellW × height*cellH 像素的全帧画布
// （留边填深色 RGB{16,16,16} letterbox，不裁切、不超出、比例不变），整块编码为
// sixel DCS。返回串不含尾随换行（不构成 in-flow 行），集成层把它写到屏幕坐标
// (row,col) 后图像恰好铺满 width×height 个字符格。换歌/回退时用 SixelClear 清除。
//
// 编码算法（DECDIS 六像素，颜色分离 pass 方案）：每 6 像素一 band；band 内统计
// 全部出现过的颜色（有序去重，上限 sixelBandColorLimit，超出合并到已有色），
// **每种颜色单独一趟 pass**：每趟输出恰好 boxW 个掩码字符（第 i 个字符 = 该列
// band 内像素是否命中此颜色；不命中的列输出掩码 0 占位推进列位），趟间用 '$'
// 回行首。同列多色由多趟叠加绘制——若把多色掩码直接连续输出，列位会被推进 k
// 次导致图像横向膨胀/错位花屏。颜色量化为 4×4×4 立方体（64 色，照片足够且
// 有效压缩 pass 数）。掩码字符连续相同时用 '!' 重复前缀压缩（SIXEL RLE）。
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
	def := [216]bool{}
	// rleRun 输出当前待写字符（含 RLE 压缩）：连续相同 ≥3 个用 !N 前缀。
	rleRun := func(w *strings.Builder, ch byte, n int) {
		if n <= 0 {
			return
		}
		if n >= 3 {
			fmt.Fprintf(w, "!%d%c", n, ch)
		} else {
			for i := 0; i < n; i++ {
				w.WriteByte(ch)
			}
		}
	}
	for by := 0; by < boxH; by += 6 {
		rows := 6
		if by+rows > boxH {
			rows = boxH - by
		}
		// 收集该 band 全部出现过的颜色（有序去重；超限合并到已有色）
		var bandColors []int
		rowColor := make([][]int, boxW) // 每列 band 各行的量化色
		{
			seen := [216]bool{}
			for x := 0; x < boxW; x++ {
				rowColor[x] = make([]int, rows)
				for r := 0; r < rows; r++ {
					c := quantizeSixel(frame.RGBAAt(x, by+r))
					rowColor[x][r] = c
					if !seen[c] {
						seen[c] = true
						bandColors = append(bandColors, c)
					}
				}
			}
			if len(bandColors) > sixelBandColorLimit {
				// 保留前 limit 色，其余合并到最近的保留色
				keep := bandColors[:sixelBandColorLimit]
				for _, c := range bandColors[sixelBandColorLimit:] {
					nearest := keep[0]
					best := colorDist(c, nearest)
					for _, k := range keep[1:] {
						if d := colorDist(c, k); d < best {
							best = d
							nearest = k
						}
					}
					for x := 0; x < boxW; x++ {
						for r := 0; r < rows; r++ {
							if rowColor[x][r] == c {
								rowColor[x][r] = nearest
							}
						}
					}
				}
				bandColors = keep
			}
		}
		// 颜色分离 pass：每种颜色一趟，每趟输出恰好 boxW 个掩码字符（RLE 压缩）
		for _, c := range bandColors {
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
			// 生成该 pass 的掩码序列，RLE 压缩写出
			var lastCh byte
			var run int
			flush := func() { rleRun(&sb, lastCh, run) }
			for x := 0; x < boxW; x++ {
				var mask byte
				for r := 0; r < rows; r++ {
					if rowColor[x][r] == c {
						mask |= 1 << r
					}
				}
				ch := '?' + mask
				if ch == lastCh {
					run++
				} else {
					flush()
					lastCh = ch
					run = 1
				}
			}
			flush()
			sb.WriteByte('$') // 回本 band 行首，准备下一颜色 pass
		}
		if by+6 < boxH {
			sb.WriteByte('-') // 移到下一 band
		}
	}
	sb.WriteString("\x1b\\")
	return sb.String()
}

// colorDist 两量化色的 RGB 距离（欧氏平方）。
func colorDist(a, b int) int {
	ar6, ag6, ab6 := a/36, (a/6)%6, a%6
	br6, bg6, bb6 := b/36, (b/6)%6, b%6
	dr, dg, db := ar6-br6, ag6-bg6, ab6-bb6
	return dr*dr + dg*dg + db*db
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
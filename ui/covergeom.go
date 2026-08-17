package ui

import "math"

// scaleFit 计算源图 (srcW,srcH) 在 boxW×boxH 像素框内**等比例缩放**（ScaleFit/contain）
// 后的目标像素尺寸：scale = min(boxW/srcW, boxH/srcH)，取整用 Round（不超框）。
//
// 不变量：目标尺寸不超出框（imgW<=boxW && imgH<=boxH）、比例保持（取整误差 <1px）、
// 不小于 1×1（防止缩放后为 0 引发除零/空渲染）。输入含 0/负值返回 (0,0)（无效，
// 调用方先行排除）。这是双路径（半块自绘 / kitty / sixel）共用的几何核心。
func scaleFit(srcW, srcH, boxW, boxH int) (imgW, imgH int) {
	if srcW <= 0 || srcH <= 0 || boxW <= 0 || boxH <= 0 {
		return 0, 0
	}
	scale := math.Min(float64(boxW)/float64(srcW), float64(boxH)/float64(srcH))
	imgW = int(math.Round(float64(srcW) * scale))
	imgH = int(math.Round(float64(srcH) * scale))
	if imgW < 1 {
		imgW = 1
	}
	if imgH < 1 {
		imgH = 1
	}
	return imgW, imgH
}
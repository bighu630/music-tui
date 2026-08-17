package coverrender

import "math"

// ScaleFit 计算源图 (srcW,srcH) 在 boxW×boxH 像素/单元格框内**等比例缩放**（contain）
// 后的目标尺寸：scale = min(boxW/srcW, boxH/srcH)，Round 取整（不超框）。
//
// 不变量：结果不超出框（imgW<=boxW && imgH<=boxH）、比例保持（取整误差 <1px）、
// 不小于 1×1（防除零/空渲染）。输入含 0/负值返回 (0,0)（调用方先行排除）。
// 单位无关：传入像素或单元格均可（比例只与比值相关）。
func ScaleFit(srcW, srcH, boxW, boxH int) (imgW, imgH int) {
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

// CenterIn 返回 imgW×imgH 在 boxW×boxH 内居中时的左上偏移量（像素/单元格单位）。
// 非法输入返回 (0,0)。
func CenterIn(imgW, imgH, boxW, boxH int) (ox, oy int) {
	if imgW <= 0 || imgH <= 0 || boxW < 0 || boxH < 0 {
		return 0, 0
	}
	return (boxW - imgW) / 2, (boxH - imgH) / 2
}
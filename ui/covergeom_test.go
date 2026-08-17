package ui

import "testing"

func TestScaleFit(t *testing.T) {
	tests := []struct {
		name            string
		srcW, srcH      int
		boxW, boxH      int
		wantW, wantH    int
	}{
		{"方形图在横框", 1000, 1000, 30, 34, 30, 30},
		{"16:9 在 30×34 像素框（宽度受限）", 1280, 720, 30, 34, 30, 17},
		{"竖图在框内（高度受限）", 720, 1280, 30, 34, 19, 34},
		{"极小 1×1 放大不超框", 1, 1, 30, 34, 30, 30},
		{"4K 大图缩小", 3840, 2160, 30, 34, 30, 17},
		{"超宽长条钳到 1 行", 10000, 1, 30, 34, 30, 1},
		{"图比框小则原尺寸", 20, 20, 30, 34, 20, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := scaleFit(tt.srcW, tt.srcH, tt.boxW, tt.boxH)
			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("scaleFit(%d,%d,%d,%d) = %d×%d, want %d×%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, gotW, gotH, tt.wantW, tt.wantH)
			}
			// 不变量：不超框
			if gotW > tt.boxW || gotH > tt.boxH {
				t.Errorf("scaleFit(%d,%d,%d,%d) = %d×%d 超出框 %d×%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, gotW, gotH, tt.boxW, tt.boxH)
			}
		})
	}
}

// TestScaleFitAspect 比例保持（像素取整误差 < 1px，源图不同尺寸下缩放比例一致）。
func TestScaleFitAspect(t *testing.T) {
	// 16:9 在 30×34：宽度受限，image 高 ≈ 30*9/16 = 16.875，取整 17
	w, h := scaleFit(1280, 720, 30, 34)
	if got := float64(w) / float64(h); got < 16.0/9.0-0.1 || got > 16.0/9.0+0.1 {
		t.Errorf("16:9 缩放后比例 %.3f 失真（w=%d h=%d）", got, w, h)
	}
	// 1:1 在 30×34：宽度受限，结果仍近似方形
	w, h = scaleFit(640, 640, 30, 34)
	if w != h {
		t.Errorf("方形图缩放后应为方形, got %d×%d", w, h)
	}
}

func TestScaleFitInvalid(t *testing.T) {
	if w, h := scaleFit(0, 10, 30, 34); w != 0 || h != 0 {
		t.Errorf("0 源宽应返回 (0,0), got %d×%d", w, h)
	}
	if w, h := scaleFit(10, 10, 0, 0); w != 0 || h != 0 {
		t.Errorf("0 框应返回 (0,0), got %d×%d", w, h)
	}
	if w, h := scaleFit(-5, 10, 30, 34); w != 0 || h != 0 {
		t.Errorf("负源宽应返回 (0,0), got %d×%d", w, h)
	}
	// 非零且框合法：结果为非零
	if w, h := scaleFit(2, 2, 3, 3); w == 0 || h == 0 {
		t.Errorf("合法输入不应返回 0 尺寸, got %d×%d", w, h)
	}
}
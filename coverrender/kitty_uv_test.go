// TestKittyThroughUltraviolet 验证 patched third_party/ultraviolet 保留零宽 APC。
// kitty 协议的 APC 序列 (\x1b_G ... \x1b\) 属零宽控制序列，upstream ultraviolet
// 曾在 StyledString.printString 中对零宽序列的 default 分支以 "=" 覆盖 cell.Content，
// 导致 APC 与同一 cell 的占位符 U+10EEEE 拼接被丢弃，图像空白。本测试通过 Draw
// 与 Lines 两条路径分别断言 APC、占位符及同 cell 共存均被保留，以防回归。
package coverrender

import (
	"image"
	"image/color"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestKittyThroughUltraviolet(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 32), uint8(y * 32), 128, 255})
		}
	}
	seq := Kitty(img, 30, 15, 8, 16) // 30x15 => offset 0,首个 cell 即占位符，保证 APC 与 placeholder 同 cell 共存可校验（30x17 会因居中首行全空格导致 APC 仅附于空格）
	if !strings.Contains(seq, "\x1b_G") {
		t.Fatalf("kitty seq missing APC")
	}
	// Wrap via ultraviolet StyledString and draw
	ss := uv.NewStyledString(seq)
	area := ss.Bounds()
	buf := uv.NewScreenBuffer(area.Dx(), area.Dy())
	ss.Draw(buf, area)

	// --- Draw 路径 ---
	foundDraw := false
	for y := 0; y < buf.Height(); y++ {
		for x := 0; x < buf.Width(); x++ {
			c := buf.CellAt(x, y)
			if strings.Contains(c.Content, "\x1b_G") {
				foundDraw = true
				break
			}
		}
		if foundDraw {
			break
		}
	}
	if !foundDraw {
		t.Fatalf("ultraviolet Draw dropped APC sequence: no cell.Content contains \\x1b_G; kitty image would be blank")
	}

	// Lines 路径 (s==nil 分支) 独立校验
	lines := ss.Lines(ansi.WcWidth)
	foundLines := false
	for _, ln := range lines {
		for _, c := range ln {
			if strings.Contains(c.Content, "\x1b_G") {
				foundLines = true
				break
			}
		}
		if foundLines {
			break
		}
	}
	if !foundLines {
		t.Fatalf("ultraviolet Lines dropped APC sequence: no cell.Content contains \\x1b_G (s==nil branch)")
	}

	// --- 占位符校验：Draw 与 Lines 各自独立 ---
	foundDrawPH := false
	for y := 0; y < buf.Height(); y++ {
		for x := 0; x < buf.Width(); x++ {
			c := buf.CellAt(x, y)
			if strings.Contains(c.Content, "\U0010EEEE") {
				foundDrawPH = true
				break
			}
		}
		if foundDrawPH {
			break
		}
	}
	if !foundDrawPH {
		t.Fatalf("placeholder U+10EEEE missing after ultraviolet Draw")
	}

	foundLinesPH := false
	for _, ln := range lines {
		for _, c := range ln {
			if strings.Contains(c.Content, "\U0010EEEE") {
				foundLinesPH = true
				break
			}
		}
		if foundLinesPH {
			break
		}
	}
	if !foundLinesPH {
		t.Fatalf("placeholder U+10EEEE missing after ultraviolet Lines (s==nil branch)")
	}

	// --- 同 cell 共存校验：APC 与占位符须拼接在同一 Cell.Content ---
	foundCoDraw := false
	for y := 0; y < buf.Height(); y++ {
		for x := 0; x < buf.Width(); x++ {
			c := buf.CellAt(x, y)
			if strings.Contains(c.Content, "\x1b_G") && strings.Contains(c.Content, "\U0010EEEE") {
				foundCoDraw = true
				break
			}
		}
		if foundCoDraw {
			break
		}
	}
	if !foundCoDraw {
		t.Fatalf("kitty APC and placeholder not co-located in same cell after Draw: expected a cell with both \\x1b_G and U+10EEEE")
	}

	foundCoLines := false
	for _, ln := range lines {
		for _, c := range ln {
			if strings.Contains(c.Content, "\x1b_G") && strings.Contains(c.Content, "\U0010EEEE") {
				foundCoLines = true
				break
			}
		}
		if foundCoLines {
			break
		}
	}
	if !foundCoLines {
		t.Fatalf("kitty APC and placeholder not co-located in same cell after Lines: expected a cell with both \\x1b_G and U+10EEEE (s==nil branch)")
	}
}

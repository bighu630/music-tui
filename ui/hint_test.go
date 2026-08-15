package ui

import (
	"strings"
	"testing"
)

// TestBottomHintFillsHeight hint 恒在最后一行的同时 content 顶部对齐，
// 总行数恰好等于 h。
func TestBottomHintFillsHeight(t *testing.T) {
	got := bottomHint(22, "a\nb", "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 22 {
		t.Fatalf("bottomHint 行数 = %d, want 22", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" {
		t.Errorf("content 应顶部对齐, got %q %q", lines[0], lines[1])
	}
	if lines[21] != "hint" {
		t.Errorf("hint 应在最后一行, got %q", lines[21])
	}
}

// TestBottomHintContentFillsExactly content 恰好 h-1 行时 hint 紧跟其后。
func TestBottomHintContentFillsExactly(t *testing.T) {
	content := strings.Repeat("x\n", 20) + "x" // 21 行
	got := bottomHint(22, content, "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 22 || lines[21] != "hint" {
		t.Errorf("content=h-1 时应零 padding: %d 行, last=%q", len(lines), lines[21])
	}
}

// TestBottomHintEmptyContent content 为空时 hint 也在最后一行。
func TestBottomHintEmptyContent(t *testing.T) {
	got := bottomHint(5, "", "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 || lines[4] != "hint" {
		t.Errorf("空 content 行数 = %d, last=%q, want 5/hint", len(lines), lines[4])
	}
}

// TestBottomHintContentTooTall content 超过 h-1 行时不崩溃（调用方保证不超，
// 超了由页面布局测试兜底暴露）。
func TestBottomHintContentTooTall(t *testing.T) {
	got := bottomHint(5, strings.Repeat("x\n", 9)+"x", "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 11 || lines[10] != "hint" {
		t.Errorf("超高超时 hint 仍应跟在 content 后: %d 行", len(lines))
	}
}

// assertHintOnLastLine 页面布局断言：m.View() 恰好占满窗口（80x24，不超屏
// 也不缺行），且最后一行（窗口最底行）stripANSI 后含 hint 文本。
// 三个页面的布局测试共用。
func assertHintOnLastLine(t *testing.T, m Model, hint string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("View 行数 = %d, want 24（tabBar 2 + 页面 h=22）", len(lines))
	}
	if got := stripANSI(lines[23]); !strings.Contains(got, hint) {
		t.Errorf("最后一行应为提示行, got %q, want 含 %q", got, hint)
	}
}

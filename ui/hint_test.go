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
	if stripANSI(lines[21]) != "hint" {
		t.Errorf("hint 应在最后一行, got %q", stripANSI(lines[21]))
	}
	if !strings.Contains(lines[21], "\x1b[2m") {
		t.Errorf("末行 hint 应为 faint 样式, got %q", lines[21])
	}
}

// TestBottomHintContentFillsExactly content 恰好 h-1 行时 hint 紧跟其后。
func TestBottomHintContentFillsExactly(t *testing.T) {
	content := strings.Repeat("x\n", 20) + "x" // 21 行
	got := bottomHint(22, content, "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 22 || stripANSI(lines[21]) != "hint" {
		t.Errorf("content=h-1 时应零 padding: %d 行, last=%q", len(lines), stripANSI(lines[21]))
	}
}

// TestBottomHintEmptyContent content 为空时 hint 也在最后一行。
func TestBottomHintEmptyContent(t *testing.T) {
	got := bottomHint(5, "", "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 || stripANSI(lines[4]) != "hint" {
		t.Errorf("空 content 行数 = %d, last=%q, want 5/hint", len(lines), stripANSI(lines[4]))
	}
}

// TestBottomHintContentTooTall content 超过 h-1 行时不崩溃（调用方保证不超，
// 超了由页面布局测试兜底暴露）。
func TestBottomHintContentTooTall(t *testing.T) {
	got := bottomHint(5, strings.Repeat("x\n", 9)+"x", "hint")
	lines := strings.Split(got, "\n")
	if len(lines) != 11 || stripANSI(lines[10]) != "hint" {
		t.Errorf("超高超时 hint 仍应跟在 content 后: %d 行", len(lines))
	}
}

// assertHintOnLastLine 页面布局断言：m.View() 恰好占满窗口（80x24，不超屏
// 也不缺行）。master 已合并 toast 分支（底部常驻状态栏）：窗口 = tabBar 2 +
// 页面 h=21 + 状态栏 1；提示行在页面内容区最后一行 = 状态栏上方一行
// （倒数第二行），stripANSI 后含 hint 文本且为 faint 样式。
// 三个页面的布局测试共用。
func assertHintOnLastLine(t *testing.T, m Model, hint string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("View 行数 = %d, want 24（tabBar 2 + 页面 h=21 + 状态栏 1）", len(lines))
	}
	if got := stripANSI(lines[22]); !strings.Contains(got, hint) {
		t.Errorf("倒数第二行（状态栏上方）应为提示行, got %q, want 含 %q", got, hint)
	}
	if !strings.Contains(lines[22], "\x1b[2m") {
		t.Errorf("提示行应为 faint 样式, got %q", lines[22])
	}
	if !strings.Contains(lines[23], "未在播放") {
		t.Errorf("最底行应为常驻状态栏, got %q", lines[23])
	}
}

package ui

import "strings"

// bottomHint 把 hint 放到第 h 行（页面内容区最后一行）：content 顶部对齐，
// 中间以空行补齐。content 行数超过 h-1 时不做截断（调用方负责 content
// 不超过 h-1；溢出由页面布局测试兜底暴露）。
func bottomHint(h int, content, hint string) string {
	lines := strings.Count(content, "\n") + 1
	if pad := h - 1 - lines; pad > 0 {
		content += strings.Repeat("\n", pad)
	}
	return content + "\n" + hint
}

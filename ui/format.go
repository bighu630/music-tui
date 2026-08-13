package ui

import (
	"fmt"
	"time"
)

// formatDuration 将秒数格式化为 mm:ss 或 HH:MM:SS（不足 1 秒按四舍五入）。
func formatDuration(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// formatPlayedAt 把播放时间格式化为"今天 15:04 / 昨天 15:04 / 2006-01-02 15:04"。
// now 参数注入便于测试。
func formatPlayedAt(t time.Time, now time.Time) string {
	t = t.Local()
	sameDay := t.Year() == now.Year() && t.YearDay() == now.YearDay()
	if sameDay {
		return "今天 " + t.Format("15:04")
	}
	yesterday := now.AddDate(0, 0, -1)
	if t.Year() == yesterday.Year() && t.YearDay() == yesterday.YearDay() {
		return "昨天 " + t.Format("15:04")
	}
	return t.Format("2006-01-02 15:04")
}

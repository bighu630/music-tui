package ui

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "00:00"},
		{59.4, "00:59"},
		{269, "04:29"},
		{3661, "01:01:01"},
		{-5, "00:00"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTrackDuration(t *testing.T) {
	cases := []struct {
		name     string
		in       float64
		want     string
		wantNote string
	}{
		{"未知时长归零", 0, "", "时长未知（Duration<=0）时不显示"},
		{"负值归零", -5, "", "负值同样视为未知"},
		{"不足一分钟", 59.4, "00:59", "四舍五入"},
		{"多分钟", 269, "04:29", "常规时长"},
		{"超一小时", 3661, "01:01:01", "小时格式"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatTrackDuration(c.in); got != c.want {
				t.Errorf("formatTrackDuration(%v) = %q, want %q（%s）", c.in, got, c.want, c.wantNote)
			}
		})
	}
}

func TestFormatPlayedAt(t *testing.T) {
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.Local)
	cases := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 8, 13, 20, 31, 0, 0, time.Local), "今天 20:31"},
		{time.Date(2026, 8, 12, 19, 2, 0, 0, time.Local), "昨天 19:02"},
		{time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local), "2026-08-01 10:00"},
	}
	for _, c := range cases {
		if got := formatPlayedAt(c.in, now); got != c.want {
			t.Errorf("formatPlayedAt(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTrackLine(t *testing.T) {
	cases := []struct {
		name, title, artist, meta, want string
	}{
		{"正常", "晴天", "周杰伦", "03:45", "晴天 - 周杰伦 · 03:45"},
		{"空作者", "晴天", "", "03:45", "晴天 · 03:45"},
		{"空附加", "晴天", "周杰伦", "", "晴天 - 周杰伦"},
		{"空作者空附加", "晴天", "", "", "晴天"},
		{"标题含连字符", "A - B", "C", "01:00", "A - B - C · 01:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatTrackLine(c.title, c.artist, c.meta); got != c.want {
				t.Errorf("formatTrackLine(%q,%q,%q) = %q, want %q", c.title, c.artist, c.meta, got, c.want)
			}
		})
	}
}

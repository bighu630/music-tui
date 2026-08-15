package ui

import "testing"

// TestLyricViewportHeight 视口行数动态计算：min(21, midH−上下各 2 行留白)，至少 1 行。
func TestLyricViewportHeight(t *testing.T) {
	cases := []struct {
		midH, want int
	}{
		{60, 21}, // 上限 21
		{37, 21}, // min(21, 33)
		{25, 21},
		{24, 20}, // 24−4
		{21, 17},
		{10, 6},
		{6, 2},
		{5, 1}, // 5−4=1
		{4, 1}, // clamp 下限
		{3, 1},
		{1, 1},
	}
	for _, c := range cases {
		if got := lyricViewportHeight(c.midH); got != c.want {
			t.Errorf("lyricViewportHeight(%d) = %d, want %d", c.midH, got, c.want)
		}
	}
}

// TestLyricScrollOffset 滚动偏移：内容 = H/2 空白 + N 歌词 + H/2 空白，
// YOffset = clamp(idx, 0, N−1) → 歌词行 idx 恒显示在视口中央行 H/2。
// 等价验证：显示行 = H/2 + idx − YOffset 必须恒等于 H/2。
func TestLyricScrollOffset(t *testing.T) {
	cases := []struct {
		name       string
		idx, n, h  int
		wantOffset int
	}{
		{"首行中央 N>H", 0, 60, 21, 0},
		{"中间行中央", 29, 60, 21, 29},
		{"末行中央 N>H", 59, 60, 21, 59},
		{"首行中央 N<H", 0, 5, 21, 0},
		{"末行中央 N<H", 4, 5, 21, 4},
		{"单行歌词", 0, 1, 21, 0},
		{"H=1", 0, 5, 1, 0},
		{"H=2", 0, 5, 2, 0},
		{"idx 越界下界", -3, 5, 21, 0},
		{"idx 越界上界", 99, 5, 21, 4},
		{"N=0", 0, 0, 21, 0},
	}
	for _, c := range cases {
		got := lyricScrollOffset(c.idx, c.n, c.h)
		if got != c.wantOffset {
			t.Errorf("%s: lyricScrollOffset(%d,%d,%d) = %d, want %d", c.name, c.idx, c.n, c.h, got, c.wantOffset)
			continue
		}
		// 核心不变量：当前行显示行 = h/2 + idx − offset 恒等于 h/2（视口中央）。
		// 仅对合法 idx 成立（越界 idx 是 clamp 场景，不是真实歌词行）。
		if c.n > 0 && c.idx >= 0 && c.idx < c.n {
			if row := c.h/2 + c.idx - got; row != c.h/2 {
				t.Errorf("%s: 当前行显示行 = %d, want %d（视口中央）", c.name, row, c.h/2)
			}
		}
	}
}

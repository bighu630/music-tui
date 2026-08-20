package ui

import "testing"

// TestLyricViewportHeight 视口行数动态计算：min(21, midH−上下各 2 行留白)，至少 1 行。
func TestLyricViewportHeight(t *testing.T) {
	cases := []struct {
		midH, want int
	}{
		{60, 21}, // 上限 21（奇数）
		{37, 21}, // min(21, 33)
		{25, 21},
		{24, 19}, // 24−4=20→19（强制奇数）
		{21, 17},
		{10, 5},  // 10−4=6→5
		{6, 1},   // 6−4=2→1
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
// 等价验证：显示行 = H/2 + idx − YOffset 必须恒等于 H/2（与 H 无关）。
func TestLyricScrollOffset(t *testing.T) {
	cases := []struct {
		name       string
		idx, n     int
		wantOffset int
	}{
		{"首行中央 N>H", 0, 60, 0},
		{"中间行中央", 29, 60, 29},
		{"末行中央 N>H", 59, 60, 59},
		{"首行中央 N<H", 0, 5, 0},
		{"末行中央 N<H", 4, 5, 4},
		{"单行歌词", 0, 1, 0},
		{"H=1", 0, 5, 0},
		{"H=2", 0, 5, 0},
		{"idx 越界下界", -3, 5, 0},
		{"idx 越界上界", 99, 5, 4},
		{"N=0", 0, 0, 0},
	}
	for _, c := range cases {
		got := lyricScrollOffset(c.idx, c.n)
		if got != c.wantOffset {
			t.Errorf("%s: lyricScrollOffset(%d,%d) = %d, want %d", c.name, c.idx, c.n, got, c.wantOffset)
			continue
		}
		// 核心不变量：当前行显示行 = h/2 + idx − offset 恒等于 h/2（视口中央），
		// 任意 h 均成立（偏移与视口高无关，以 lyricMaxLines 验证）。
		// 仅对合法 idx 成立（越界 idx 是 clamp 场景，不是真实歌词行）。
		if c.n > 0 && c.idx >= 0 && c.idx < c.n {
			if row := lyricMaxLines/2 + c.idx - got; row != lyricMaxLines/2 {
				t.Errorf("%s: 当前行显示行 = %d, want %d（视口中央）", c.name, row, lyricMaxLines/2)
			}
		}
	}
}

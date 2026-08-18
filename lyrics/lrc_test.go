package lyrics

import (
	"testing"
)

func TestParseLRCMultiTimestamps(t *testing.T) {
	lrc := "[00:12.00][00:45.00]副歌"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(ly.Lines))
	}
	if ly.Lines[0].Time != 12.0 || ly.Lines[1].Time != 45.0 {
		t.Errorf("times = %v %v, want 12 45", ly.Lines[0].Time, ly.Lines[1].Time)
	}
	if ly.Lines[0].Text != "副歌" || ly.Lines[1].Text != "副歌" {
		t.Errorf("texts = %q %q", ly.Lines[0].Text, ly.Lines[1].Text)
	}
}

func TestParseLRCTimeFormats(t *testing.T) {
	lrc := "[00:01.5]十分之一秒\n[00:02.50]百分秒\n[00:03.123]毫秒\n[00:04]整秒\n[01:02:03]冒号分隔百分秒"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(ly.Lines))
	}
	want := []float64{1.5, 2.5, 3.123, 4, 62.03}
	for i, w := range want {
		if ly.Lines[i].Time != w {
			t.Errorf("Lines[%d].Time = %v, want %v", i, ly.Lines[i].Time, w)
		}
	}
}

func TestParseLRCSortsByTime(t *testing.T) {
	lrc := "[00:30.00]后\n[00:10.00]前\n[00:20.00]中"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	got := []float64{ly.Lines[0].Time, ly.Lines[1].Time, ly.Lines[2].Time}
	want := []float64{10, 20, 30}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序错误: %v, want %v", got, want)
		}
	}
}

func TestParseLRCSkipsMetadataAndPlainLines(t *testing.T) {
	lrc := "[ti:晴天]\n[ar:周杰倫]\n[al:叶惠美]\n\n[00:10.00]只有这行有效\n无时间戳的普通行"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(ly.Lines))
	}
	if ly.Lines[0].Text != "只有这行有效" {
		t.Errorf("text = %q", ly.Lines[0].Text)
	}
}

func TestParseLRCCarriageReturns(t *testing.T) {
	lrc := "[00:10.00]第一行\r\n[00:20.00]第二行\r\n"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(ly.Lines))
	}
	if ly.Lines[0].Text != "第一行" || ly.Lines[1].Text != "第二行" {
		t.Errorf("\\r 未正确处理: %q %q", ly.Lines[0].Text, ly.Lines[1].Text)
	}
}

func TestParseLRCEqualTimesKeepBoth(t *testing.T) {
	lrc := "[00:10.00]A\n[00:10.00]B"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(ly.Lines))
	}
}

func TestLineAt(t *testing.T) {
	ly, err := ParseLRC([]byte("[00:10.00]第一行\n[00:20.00]第二行\n[00:30.00]第三行\n"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pos      float64
		wantIdx  int
		wantText string
	}{
		{0, -1, ""},      // 早于第一行
		{10, 0, "第一行"},   // 恰好命中
		{15.9, 0, "第一行"}, // 区间内
		{20, 1, "第二行"},   // 恰好命中
		{29.999, 1, "第二行"},
		{30, 2, "第三行"},  // 最后一行
		{999, 2, "第三行"}, // 超出末尾
	}
	for _, c := range cases {
		idx, text := ly.LineAt(c.pos)
		if idx != c.wantIdx || text != c.wantText {
			t.Errorf("LineAt(%v) = (%d, %q), want (%d, %q)", c.pos, idx, text, c.wantIdx, c.wantText)
		}
	}
}

func TestLineAtEmpty(t *testing.T) {
	ly, err := ParseLRC([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if idx, text := ly.LineAt(5); idx != -1 || text != "" {
		t.Errorf("LineAt = (%d, %q), want (-1, \"\")", idx, text)
	}
}

// TestShift 歌词时间整体平移：+0.5 全部平移；-0.5 且首行 0.3s 被 clamp 到 0；
// 空 Lines 安全；多次调用累加。
func TestShift(t *testing.T) {
	// +0.5 全部平移
	ly := &Lyrics{Lines: []LyricLine{
		{Time: 5.0, Text: "a"},
		{Time: 12.5, Text: "b"},
	}}
	ly.Shift(0.5)
	if ly.Lines[0].Time != 5.5 {
		t.Errorf("Shift(+0.5) Lines[0].Time = %v, want 5.5", ly.Lines[0].Time)
	}
	if ly.Lines[1].Time != 13.0 {
		t.Errorf("Shift(+0.5) Lines[1].Time = %v, want 13.0", ly.Lines[1].Time)
	}

	// -0.5 且首行 0.3s 被 clamp 到 0
	ly2 := &Lyrics{Lines: []LyricLine{
		{Time: 0.3, Text: "a"},
		{Time: 2.0, Text: "b"},
	}}
	ly2.Shift(-0.5)
	if ly2.Lines[0].Time != 0 {
		t.Errorf("Shift(-0.5) 首行 .Time = %v, want 0（clamp 到 0）", ly2.Lines[0].Time)
	}
	if ly2.Lines[1].Time != 1.5 {
		t.Errorf("Shift(-0.5) Lines[1].Time = %v, want 1.5", ly2.Lines[1].Time)
	}

	// 空 Lines 安全
	empty := &Lyrics{}
	empty.Shift(-0.5)
	empty.Shift(+0.5)
	if empty.Lines != nil {
		t.Errorf("空 Lyrics 平移后 Lines 应仍为 nil")
	}

	// 多次调用累加
	ly3 := &Lyrics{Lines: []LyricLine{{Time: 1.0, Text: "a"}}}
	ly3.Shift(0.5)
	ly3.Shift(0.5)
	if ly3.Lines[0].Time != 2.0 {
		t.Errorf("两次 Shift(+0.5) = %v, want 2.0", ly3.Lines[0].Time)
	}
	ly3.Shift(-0.5)
	if ly3.Lines[0].Time != 1.5 {
		t.Errorf("累加后再 -0.5 = %v, want 1.5", ly3.Lines[0].Time)
	}
}

func TestParseLRCRejectsOverflowMinutes(t *testing.T) {
	// 19 位分钟数：超出 int64（Atoi 失败）或超过 MaxInt64/60 守卫，均视为非法时间标签；
	// 恰好等于 MaxInt64/60 的边界值（mm*60+ss 在 int 下溢出为负）必须由 float64
	// 计算兜底为巨大正数，绝不产生负时间行破坏 LineAt。
	lrc := "[9999999999999999999:00.00]Atoi溢出\n[9000000000000000000:00.00]守卫拒绝\n[153722867280912930:08]边界\n[00:10.00]正常"
	ly, err := ParseLRC([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(ly.Lines) != 2 {
		t.Fatalf("lines = %d, want 2（边界行 + 正常行）", len(ly.Lines))
	}
	if ly.Lines[0].Time != 10.0 || ly.Lines[0].Text != "正常" {
		t.Errorf("Lines[0] = %+v, want 正常行", ly.Lines[0])
	}
	if ly.Lines[1].Time < 9e18 || ly.Lines[1].Text != "边界" {
		t.Errorf("Lines[1] = %+v, want 巨大正时间边界行", ly.Lines[1])
	}
}

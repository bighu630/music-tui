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
		{0, -1, ""},        // 早于第一行
		{10, 0, "第一行"},    // 恰好命中
		{15.9, 0, "第一行"},  // 区间内
		{20, 1, "第二行"},    // 恰好命中
		{29.999, 1, "第二行"},
		{30, 2, "第三行"},    // 最后一行
		{999, 2, "第三行"},   // 超出末尾
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

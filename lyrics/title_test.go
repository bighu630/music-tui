package lyrics

import (
	"reflect"
	"strings"
	"testing"
)

func TestCleanCandidates(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"纯中文", "晴天", []string{"晴天"}},
		{"噪声后缀", "周杰倫 七里香 歌詞", []string{"周杰倫 七里香 歌詞", "周杰倫 七里香", "周杰倫", "七里香"}},
		{"空标题", "", nil},
		{"空白标题", "   ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanCandidates(tt.title)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("cleanCandidates(%q) = %#v, want %#v", tt.title, got, tt.want)
			}
		})
	}
}

func TestCleanCandidatesKeepsRawAndExtracts(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		substr string
	}{
		{"括号加官方后缀", "周杰倫 Jay Chou【晴天 Sunny Day】-Official Music Video", "晴天"},
		{"连字符分隔", "周杰伦-七里香", "七里香"},
		{"括号版本", "Taylor Swift - Love Story (Taylor's Version)", "Love Story"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cands := cleanCandidates(tt.title)
			if len(cands) == 0 || cands[0] != tt.title {
				t.Errorf("cands[0] = %q, want 原始标题 %q", cands[0], tt.title)
			}
			if !strings.Contains(strings.Join(cands, "\n"), tt.substr) {
				t.Errorf("candidates %q 不含 %q", cands, tt.substr)
			}
		})
	}
}

func TestCleanCandidatesLimit(t *testing.T) {
	cands := cleanCandidates("晴天 七里香 稻香 夜曲 龙卷风 安静 简单爱")
	if len(cands) == 0 || cands[0] != "晴天 七里香 稻香 夜曲 龙卷风 安静 简单爱" {
		t.Fatalf("cands[0] = %q, want 原始标题", cands[0])
	}
	if len(cands) > maxCandidates {
		t.Errorf("len(cands) = %d, want ≤ %d", len(cands), maxCandidates)
	}
}

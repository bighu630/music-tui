package ui

import "testing"

func TestFilterMatches(t *testing.T) {
	cases := []struct {
		name    string
		keyword string
		value   string
		want    bool
	}{
		{"空关键词匹配一切", "", "anything", true},
		{"空白关键词匹配一切", "   ", "anything", true},
		{"子串命中", "晴天", "周杰伦 - 晴天", true},
		{"大小写不敏感", "Jay", "周杰伦 - jay chou", true},
		{"关键词大小写不敏感", "JAY", "周杰伦 - jay chou", true},
		{"歌手命中", "周杰伦", "周杰伦 - 晴天", true},
		{"未命中", "林俊杰", "周杰伦 - 晴天", false},
		{"关键词 Trim", "  晴天  ", "周杰伦 - 晴天", true},
		{"值 Trim 不受影响（子串语义）", "伦 -", "周杰伦 - 晴天", true},
		{"英文子串", "chou", "jay chou", true},
		{"大小写敏感值（关键词小写）", "CHOU", "jay chou", true},
		{"完全不匹配", "xyz", "abc", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := filterMatches(c.keyword, c.value); got != c.want {
				t.Errorf("filterMatches(%q, %q) = %v, want %v", c.keyword, c.value, got, c.want)
			}
		})
	}
}

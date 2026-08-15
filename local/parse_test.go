package local

import "testing"

func TestParseFilename(t *testing.T) {
	cases := []struct {
		name       string
		wantTitle  string
		wantArtist string
	}{
		// 标准 "歌手 - 歌名" 格式
		{"周杰伦 - 晴天.mp3", "晴天", "周杰伦"},
		// 无分隔符：整个文件名作标题
		{"晴天.mp3", "晴天", ""},
		// 多个分隔符：按最后一个分割（左侧多余部分丢弃）
		{"a - b - c.mp3", "c", "b"},
		// 两侧带空白：TrimSpace 后解析
		{" 标题  .FLAC", "标题", ""},
		{"周杰伦 - 晴天  .mp3", "晴天", "周杰伦"},
		// 空串与纯扩展名
		{"", "", ""},
		{".mp3", "", ""},
		// 无扩展名
		{"周杰伦 - 晴天", "晴天", "周杰伦"},
		// 分隔符一侧为空白：不算分隔符，整体作标题
		{" - 晴天.mp3", "- 晴天", ""},
		{"周杰伦 - .mp3", "周杰伦 -", ""},
		// 分隔符两侧均为空白：整体作标题
		{"  -  .mp3", "-", ""},
		// 多级扩展名：只去掉最后一个
		{"a.b.mp3", "a.b", ""},
	}
	for _, c := range cases {
		title, artist := ParseFilename(c.name)
		if title != c.wantTitle || artist != c.wantArtist {
			t.Errorf("ParseFilename(%q) = (%q, %q)，期望 (%q, %q)", c.name, title, artist, c.wantTitle, c.wantArtist)
		}
	}
}

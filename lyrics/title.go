package lyrics

import (
	"strings"
)

// maxCandidates 限制每个标题生成的查询候选数量。
const maxCandidates = 4

// cleanCandidates 按优先级生成 lrclib 查询候选标题：
// [原始标题, 去噪标题, 分隔符切分片段, 连续 CJK 词元]。
// 原始标题无条件加入（即使 1 字符）；派生候选要求 ≥2 字符；
// 结果去重、去空白，上限 maxCandidates。
func cleanCandidates(title string) []string {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	var out []string
	add := func(s string, minLen int) {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < minLen || contains(out, s) {
			return
		}
		out = append(out, s)
	}
	add(title, 1) // 原始标题无条件加入
	cleaned := stripNoise(title)
	add(cleaned, 2)
	for _, part := range splitSegments(cleaned) {
		add(part, 2)
	}
	for _, token := range cjkTokens(title) {
		add(token, 2)
	}
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// contains 判断 ss 中是否已含 s。
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// bracketPairs 需要剥离的括号对。
var bracketPairs = [][2]string{
	{"【", "】"}, {"[", "]"}, {"(", ")"}, {"（", "）"}, {"〔", "〕"},
}

// suffixNoise 常见 YouTube 标题噪声后缀（匹配时不区分大小写，
// 可带前置 " -"）。
var suffixNoise = []string{
	"official music video", "official mv", "official audio", "official",
	"music video", "mv", "lyrics", "歌词", "歌詞", "字幕", "karaoke",
	"hd", "hq", "4k", "1080p", "720p", "60fps", "高清", "完整版",
}

// stripNoise 迭代剥离标题噪声：括号内容 → 常见后缀 → 尾部残留
// 分隔符与空白，重复直至稳定。
func stripNoise(s string) string {
	for {
		orig := s
		// 剥离括号内容（迭代处理嵌套）
		for _, p := range bracketPairs {
			for {
				next := removeBracket(s, p[0], p[1])
				if next == s {
					break
				}
				s = next
			}
		}
		// 剥离常见后缀（可带前置 " -"），可重复
		lower := strings.ToLower(s)
		for _, suf := range suffixNoise {
			for {
				trimmed := trimSuffixWithDash(s, lower, suf)
				if trimmed == s {
					break
				}
				s = trimmed
				lower = strings.ToLower(s)
			}
		}
		// 去尾部残留分隔符与空白
		s = strings.TrimRight(s, " -–—|·\t\r\n")
		s = strings.TrimSpace(s)
		if s == orig {
			break
		}
	}
	return s
}

// removeBracket 删除 s 中第一对 open/close 括号及其内容；无括号对时
// 原样返回。重复调用可处理嵌套。
func removeBracket(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return s
	}
	j := strings.Index(s[i+len(open):], close)
	if j < 0 {
		return s
	}
	return s[:i] + s[i+len(open)+j+len(close):]
}

// trimSuffixWithDash 若 s（lower 为其小写形式）以 suffix 结尾，则连同
// 其前的空白/连字符（" -"）一并去除；否则原样返回。
func trimSuffixWithDash(s, lower, suffix string) string {
	if len(lower) < len(suffix) || lower[len(lower)-len(suffix):] != suffix {
		return s
	}
	rest := s[:len(s)-len(suffix)]
	return strings.TrimRight(rest, " -")
}

// splitSegments 按常见标题分隔符切分片段。
func splitSegments(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '-', '–', '—', '|', '·':
			return true
		}
		return false
	})
}

// cjkTokens 提取标题中长度 ≥2 的连续 CJK 词元。
func cjkTokens(s string) []string {
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			tokens = append(tokens, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range s {
		if isCJK(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// isCJK 判断 rune 是否属于 CJK 统一表意文字区。
func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0xF900 && r <= 0xFAFF)
}

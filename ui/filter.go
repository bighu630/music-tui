package ui

import "strings"

// filterMatches 判断 value 是否命中过滤词 keyword：
// Trim 后大小写不敏感子串匹配；空关键词匹配一切。
// 队列/历史页 / 过滤共用（匹配对象为 FilterValue：标题 + " " + 歌手）。
func filterMatches(keyword, value string) bool {
	kw := strings.TrimSpace(keyword)
	if kw == "" {
		return true
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(kw))
}

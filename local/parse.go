// Package local 提供本地音频文件的扫描与 Track 映射：从本地路径
// 递归发现支持的音频文件，解析文件名得到歌名/歌手，映射为可播放的 Track。
package local

import (
	"path/filepath"
	"strings"
)

// ParseFilename 从文件名解析歌名与歌手。
//
// 去掉扩展名后，按最后一个 " - "（空格-连字符-空格）分割，且分隔符
// 两侧都非空白才算有效：左侧 = 歌手，右侧 = 歌名（两侧均 TrimSpace）。
// 无有效分隔符时，整个（去扩展名）文件名作为歌名，歌手为空。
func ParseFilename(name string) (title, artist string) {
	base := strings.TrimSuffix(name, filepath.Ext(name))

	// 按 " - " 切分后，取最后一个两侧都非空白的相邻分隔：
	// "a - b - c" → 最后一个有效分隔在 b 与 c 之间 → 歌手=b、歌名=c。
	parts := strings.Split(base, " - ")
	last := -1
	for i := 0; i+1 < len(parts); i++ {
		if strings.TrimSpace(parts[i]) != "" && strings.TrimSpace(parts[i+1]) != "" {
			last = i
		}
	}
	if last == -1 {
		return strings.TrimSpace(base), ""
	}
	return strings.TrimSpace(parts[last+1]), strings.TrimSpace(parts[last])
}

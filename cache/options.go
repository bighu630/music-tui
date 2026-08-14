// Package cache 实现音频文件 LRU 缓存：后台异步下载副本、命中优先本地播放。
package cache

// Options 是缓存配置项（config 包嵌入此类型；json tag 即配置文件格式，
// 本类型是缓存配置的唯一真源）。
type Options struct {
	// Enabled 缓存总开关（默认开）。
	Enabled bool `json:"enabled"`
	// MaxEntries 缓存歌曲数上限，超出按最后播放时间淘汰最久未播放的（默认 100）。
	MaxEntries int `json:"max_entries"`
	// Dir 缓存目录（默认 ~/.cache/music-tui）。
	Dir string `json:"dir"`
}

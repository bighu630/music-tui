// Package model 定义所有模块共享的数据结构，为叶子包，不含任何逻辑。
package model

// Track 描述一首可播放的歌曲。
type Track struct {
	ID       string  // 来源内唯一 ID（YouTube video ID）——历史去重依据
	Title    string  // 标题
	Artist   string  // 歌手 / 频道名
	Duration float64 // 时长（秒）
	URL      string  // 可直接交给 mpv 播放的地址（YouTube 页面 URL）
	Source   string  // 来源标识："youtube"（未来可扩展 "netease" 等）
	CoverURL string  // 封面图 URL（maxresdefault 优先）
}

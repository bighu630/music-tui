package model

// PlaybackState 是全局播放状态，由 root model 持有、各页面共享。
type PlaybackState struct {
	Track    *Track  // 当前歌曲；nil = 未播放
	Position float64 // 当前进度（秒）
	Duration float64 // 总时长（秒）
	Playing  bool    // 是否播放中
}

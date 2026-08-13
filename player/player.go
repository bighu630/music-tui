// Package player 定义播放器接口与事件类型；mpv 实现见 mpv.go。
package player

// Event 是播放器事件接口，由各具体事件类型实现（封闭接口）。
type Event interface {
	isEvent()
}

// ProgressEvent 播放进度更新（mpv observe_property 推送，约每 50ms 一次）。
type ProgressEvent struct {
	Position float64 // 当前进度（秒）
	Duration float64 // 总时长（秒）
}

func (ProgressEvent) isEvent() {}

// StateEvent 暂停/继续状态变化。
type StateEvent struct {
	Playing bool
}

func (StateEvent) isEvent() {}

// TrackStartedEvent 新歌曲加载完成（mpv file-loaded）。
type TrackStartedEvent struct {
	Duration float64
}

func (TrackStartedEvent) isEvent() {}

// TrackEndedEvent 当前歌曲播放结束（mpv eof-reached）。
type TrackEndedEvent struct{}

func (TrackEndedEvent) isEvent() {}

// ErrorEvent 播放器异常（播放出错、mpv 崩溃或 socket 断开）。
type ErrorEvent struct {
	Err error
}

func (ErrorEvent) isEvent() {}

// Player 是播放器接口，ui 层只依赖此接口。
type Player interface {
	// Play 开始播放指定 URL，替换当前歌曲。
	Play(url string) error
	// Pause 暂停播放。
	Pause() error
	// Resume 继续播放。
	Resume() error
	// Seek 跳转到指定秒数（绝对位置）。
	Seek(seconds float64) error
	// Events 返回播放器事件流，由内部 goroutine 推送。
	Events() <-chan Event
}

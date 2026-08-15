// Package player 定义播放器接口与事件类型；mpv 实现见 mpv.go。
package player

import (
	"fmt"
	"time"
)

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

// TrackEndedEvent 当前歌曲播放结束（mpv end-file reason=eof）。
// keep-open=no 下 EOF 时 eof-reached 属性立即变 unavailable（data=null），
// 可靠信号是 end-file 事件，故结束信号统一以此为准。
type TrackEndedEvent struct{}

func (TrackEndedEvent) isEvent() {}

// ErrorEvent 播放器异常（播放出错、mpv 崩溃或 socket 断开）。
type ErrorEvent struct {
	Err error
}

func (ErrorEvent) isEvent() {}

// LoadFailedError 表示文件加载/播放失败（mpv end-file reason=error）。
// FileError 是 mpv IPC file_error 字段的诊断文本（如 "no audio or video data played"），
// 旧版 mpv 可能缺失该字段（为空串）。
type LoadFailedError struct{ FileError string }

func (e *LoadFailedError) Error() string {
	if e.FileError != "" {
		return "mpv 播放出错（end-file reason=error）: " + e.FileError
	}
	return "mpv 播放出错（end-file reason=error）"
}

// LoadTimeoutError 表示加载超时：loadfile 后限时未收到 file-loaded/end-file
// （mpv 取流悬挂：yt-dlp 卡死/网络黑洞/403 重试退避），看门狗主动报错，
// UI 据此走现有重试/跳过链路，不再无限期卡住。
type LoadTimeoutError struct{ Timeout time.Duration }

func (e *LoadTimeoutError) Error() string {
	return fmt.Sprintf("加载超时（%s 内未就绪）", e.Timeout)
}

// Player 是播放器接口，ui 层只依赖此接口。
type Player interface {
	// Play 开始播放指定 URL，替换当前歌曲。
	Play(url string) error
	// PlayPaused 加载指定 URL 但保持暂停（续播恢复用，不发声）；start 为恢复
	// 起点（秒），>0 时随 loadfile 的 start= 选项原子定位（mpv ≥0.38 语法），
	// ≤0 时从头加载（保持 2 参 loadfile 兼容）。
	PlayPaused(url string, start float64) error
	// Pause 暂停播放。
	Pause() error
	// Resume 继续播放。
	Resume() error
	// Seek 跳转到指定秒数（绝对位置）。
	Seek(seconds float64) error
	// SetLoop 设置单曲循环（mpv loop-file 无缝循环）。loop-file 是 per-file
	// 属性：换文件（loadfile）自动重置为不循环，无需显式关闭。
	SetLoop(loop bool) error
	// Events 返回播放器事件流，由内部 goroutine 推送。
	Events() <-chan Event
}

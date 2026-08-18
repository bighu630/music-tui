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

// StalledEvent 播放无进展（卡住）：file-loaded 已到（TrackStarted 已发）但
// 检测窗口内（stallWindow）未见 position 推进（无 ProgressEvent 且 Position>0）。
// UI 据此重启 mpv 进程并重播同曲（自动恢复“卡住不播放”）；重试上限与其它兜底
// 协调在 UI 层。注意与 LoadTimeoutError 互补：后者=一直没 file-loaded（加载看门狗），
// 前者=已加载但未开始推进。
type StalledEvent struct{}

func (StalledEvent) isEvent() {}

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
	// Restart 强制重启 mpv 进程并重连（卡住恢复）：kill 旧进程 → 清理 socket →
	// 重启 mpv（--idle --input-ipc-server）→ 重新连接并注册属性观察。重启后
	// 事件流（Events()/Subscribe()）自动恢复（同一实例），调用方重新 Play 即可。
	// 与自动重连共用单飞机制，故意 kill 不触发虚假断开事件。
	Restart() error
	// SetLoop 设置单曲循环（mpv loop-file 无缝循环）。loop-file 是 per-file
	// 属性：换文件（loadfile）自动重置为不循环，无需显式关闭。
	SetLoop(loop bool) error
	// Events 返回播放器事件流，由内部 goroutine 推送。
	Events() <-chan Event
}

package ui

import (
	"time"

	"music-tui/player"
)

// progressThrottle 合并连续 ProgressEvent 的滚动窗口节流器：
//
//   - Push(ProgressEvent)：进入窗口，窗口内只保留最后一个（旧值被替换）；
//     返回 false（调用方不立即放行）。
//   - Push(其他事件)：丢弃窗口内缓存的进度，返回 true（调用方立即放行该事件，
//     其他事件类型不被吞）。
//   - Fired()：窗口计时到点（首个进度事件 + window）返回通知 channel；
//     无活跃窗口时返回 nil（select 阻塞安全）。
//   - Take()：取出窗口内最后一个 ProgressEvent（取后复位，下一事件重开窗口）。
//
// 行为等价于：连续进度事件流下每秒最多放行 1/window 个事件，且放行的是窗口内
// 最新的一个——进度展示最多滞后 window（对进度条/歌词行切换可接受；歌词行切换
// 由窗口内最后一次事件驱动，不会漏行）。
type progressThrottle struct {
	window  time.Duration
	pending *player.ProgressEvent
	fired   chan struct{}
}

func newProgressThrottle(window time.Duration) *progressThrottle {
	return &progressThrottle{window: window}
}

// Push 输入一个事件：ProgressEvent 进入合并窗口（返回 false）；其他事件类型
// 丢弃缓存的进度并返回 true（调用方应立即放行该事件）。
func (t *progressThrottle) Push(ev player.Event) bool {
	pe, ok := ev.(player.ProgressEvent)
	if !ok {
		t.pending = nil
		t.fired = nil
		return true
	}
	t.pending = &pe
	if t.fired == nil {
		// 窗口起点：从首个进度事件起计时（窗口内后续事件只替换 pending，
		// 不重置计时——重置会在连续事件流下造成永不触发）。
		// 闭包必须捕获 channel 值而非 t.fired：触发时可能已被 Take 置 nil。
		t.fired = make(chan struct{})
		fired := t.fired
		time.AfterFunc(t.window, func() { close(fired) })
	}
	return false
}

// Fired 返回窗口到点的通知 channel；无活跃窗口时返回 nil。
func (t *progressThrottle) Fired() <-chan struct{} {
	return t.fired
}

// Take 取出窗口内最后一个 ProgressEvent 并复位；无缓存时返回 ok=false。
func (t *progressThrottle) Take() (player.ProgressEvent, bool) {
	if t.pending == nil {
		return player.ProgressEvent{}, false
	}
	pe := *t.pending
	t.pending = nil
	t.fired = nil
	return pe, true
}

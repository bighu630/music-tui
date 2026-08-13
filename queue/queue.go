// Package queue 实现播放队列纯逻辑：顺序/随机模式、自动连播推进。
// 不依赖 player 与任何 IO，仅依赖 model；UI 层负责把事件翻译成调用。
package queue

import (
	"math/rand"

	"music-tui/model"
)

// Mode 播放模式。
type Mode int

const (
	// Sequential 顺序播放：按队列显示顺序逐首推进。
	Sequential Mode = iota
	// Shuffle 随机播放：切入时一次性洗牌"当前曲之后"，
	// 洗牌后数组顺序即 UI 显示顺序，也即实际播放顺序。
	Shuffle
)

// Queue 播放队列。tracks 的下标即 UI 展示的序号（currentIdx 高亮）；
// 顺序/随机播完列表均停止（不循环）。
type Queue struct {
	tracks     []model.Track
	currentIdx int // 当前曲目下标；-1 = 无当前曲目
	mode       Mode
}

// New 创建空队列（无当前曲目，顺序模式）。
func New() *Queue {
	return &Queue{currentIdx: -1}
}

// Add 追加到队尾。不改变当前曲目；队列从未播放过时仅入队、不自动播放。
func (q *Queue) Add(t model.Track) {
	q.tracks = append(q.tracks, t)
}

// Replace 替换语义：清空队列后把 t 作为唯一曲目并设为当前。
// 手动播放（搜索/历史/队列页）统一走此语义。
func (q *Queue) Replace(t model.Track) {
	q.tracks = []model.Track{t}
	q.currentIdx = 0
}

// Remove 删除指定下标的曲目。删除当前曲目时顺延下一首成为当前；
// 当前曲目为末位（无顺延）时变为无当前曲目（-1）。非法下标忽略。
func (q *Queue) Remove(i int) {
	if i < 0 || i >= len(q.tracks) {
		return
	}
	q.tracks = append(q.tracks[:i], q.tracks[i+1:]...)
	switch {
	case i == q.currentIdx:
		if q.currentIdx >= len(q.tracks) {
			q.currentIdx = -1
		}
	case i < q.currentIdx:
		q.currentIdx--
	}
}

// Clear 清空队列并清除当前曲目；模式保留（用户偏好）。
func (q *Queue) Clear() {
	q.tracks = nil
	q.currentIdx = -1
}

// Next 推进到下一首并返回（当前曲目不变时返回 false 且不移动）。
// 播完列表停止（不循环）；无当前曲目（如删除了末位当前曲）时从头开始。
func (q *Queue) Next() (model.Track, bool) {
	if len(q.tracks) == 0 {
		return model.Track{}, false
	}
	if q.currentIdx == -1 {
		q.currentIdx = 0
		return q.tracks[0], true
	}
	if q.currentIdx+1 >= len(q.tracks) {
		return model.Track{}, false
	}
	q.currentIdx++
	return q.tracks[q.currentIdx], true
}

// SetMode 切换播放模式。切入随机时只洗牌"当前曲之后"的曲目
// （不打断当前播放）；切回顺序保持当前数组顺序。同模式调用为 no-op。
func (q *Queue) SetMode(m Mode) {
	if m == q.mode {
		return
	}
	q.mode = m
	if m != Shuffle {
		return
	}
	if q.currentIdx == -1 {
		rand.Shuffle(len(q.tracks), func(i, j int) {
			q.tracks[i], q.tracks[j] = q.tracks[j], q.tracks[i]
		})
		return
	}
	tail := q.tracks[q.currentIdx+1:]
	rand.Shuffle(len(tail), func(i, j int) {
		tail[i], tail[j] = tail[j], tail[i]
	})
}

// JumpTo 跳转到指定下标的曲目并设为当前（队列页 Enter 语义：
// 保留队列其余曲目，仅移动当前指针）。下标非法返回 false。
func (q *Queue) JumpTo(i int) bool {
	if i < 0 || i >= len(q.tracks) {
		return false
	}
	q.currentIdx = i
	return true
}

// Current 返回当前曲目；无当前曲目时返回 false。
func (q *Queue) Current() (model.Track, bool) {
	if q.currentIdx < 0 || q.currentIdx >= len(q.tracks) {
		return model.Track{}, false
	}
	return q.tracks[q.currentIdx], true
}

// Tracks 返回队列副本（供 UI 展示；修改返回值不影响队列）。
func (q *Queue) Tracks() []model.Track {
	return append([]model.Track(nil), q.tracks...)
}

// CurrentIndex 返回当前曲目下标；-1 = 无当前曲目。
func (q *Queue) CurrentIndex() int { return q.currentIdx }

// Mode 返回当前播放模式。
func (q *Queue) Mode() Mode { return q.mode }

// Len 返回队列长度。
func (q *Queue) Len() int { return len(q.tracks) }

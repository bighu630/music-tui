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

// shuffleFn 洗牌实现（包级变量：测试可注入确定性置换断言精确顺序，
// 模式同 root.go 的 retryBackoff）。
var shuffleFn = rand.Shuffle

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

// ReplaceAll 替换语义：清空队列后用整个播放列表填充，当前指针指向
// startIdx（播放列表页 Enter：从选中曲目开始连播整个列表）。
// startIdx 越界时 clamp 到合法范围；空列表清空队列（无当前曲目）。
// 随机模式下 currentIdx 之后的尾部一并洗牌（同 SetMode 语义），
// 保证“加载播放列表后随机直接可用”。
func (q *Queue) ReplaceAll(tracks []model.Track, startIdx int) {
	q.tracks = append([]model.Track(nil), tracks...)
	q.currentIdx = -1
	if len(q.tracks) == 0 {
		return
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(q.tracks) {
		startIdx = len(q.tracks) - 1
	}
	q.currentIdx = startIdx
	// 随机模式：复用 SetMode 的 tail-shuffle 语义——选中曲及之前的曲目
	// 保持原序（不打断刚选中的曲目），只洗牌其后的尾部。
	if q.mode == Shuffle {
		tail := q.tracks[q.currentIdx+1:]
		shuffleFn(len(tail), func(i, j int) {
			tail[i], tail[j] = tail[j], tail[i]
		})
	}
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
	shuffleFn(len(tail), func(i, j int) {
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

// Snapshot 是队列状态的不可变快照（供会话持久化/恢复）。
type Snapshot struct {
	Tracks     []model.Track `json:"tracks"`
	CurrentIdx int           `json:"current_index"`
	Mode       Mode          `json:"mode"`
}

// Snapshot 返回当前队列状态的副本（修改返回值不影响队列）。
func (q *Queue) Snapshot() Snapshot {
	return Snapshot{
		Tracks:     append([]model.Track(nil), q.tracks...),
		CurrentIdx: q.currentIdx,
		Mode:       q.mode,
	}
}

// Restore 用快照覆盖当前队列状态。CurrentIdx 越界（损坏/手改数据）
// 时降级为无当前曲目（-1），避免恢复出不可用状态。
func (q *Queue) Restore(s Snapshot) {
	q.tracks = append([]model.Track(nil), s.Tracks...)
	q.mode = s.Mode
	if s.CurrentIdx < 0 || s.CurrentIdx >= len(q.tracks) {
		q.currentIdx = -1
		return
	}
	q.currentIdx = s.CurrentIdx
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

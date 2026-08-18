// Package queue 实现播放队列纯逻辑：顺序/随机模式、自动连播推进。
// 不依赖 player 与任何 IO，仅依赖 model；UI 层负责把事件翻译成调用。
package queue

import (
	"errors"
	"math/rand"
	"strings"
	"sync"

	"music-tui/model"
)

// Mode 播放模式。
type Mode int

const (
	// Sequential 列表循环：按队列显示顺序逐首推进，Next 在末尾回绕到队首。
	Sequential Mode = iota
	// Shuffle 随机播放：切入时一次性洗牌"当前曲之后"，
	// 洗牌后数组顺序即 UI 显示顺序，也即实际播放顺序；
	// Next 在末尾回绕到队首（不重洗）。
	Shuffle
	// RepeatOne 单曲循环：队列推进语义同 Sequential（手动下一首正常推进）；
	// 无缝循环在 player 层实现，queue 不特殊处理。
	RepeatOne
)

// ErrEmpty 队列为空时 MPRIS 控制器操作的哨兵错误（mpris 映射 NotSupported）。
var ErrEmpty = errors.New("queue: empty")

// Queue 播放队列。tracks 的下标即 UI 展示的序号（currentIdx 高亮）；
// 三种模式下 Next 播完列表均回绕到队首（循环）。
// 并发安全：UI 在 bubbletea 循环内写；MPRIS D-Bus goroutine 经 RLock 读
// （Mode/Len/Current/Tracks/Snapshot/CurrentIndex）。
type Queue struct {
	mu         sync.RWMutex
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

// isEmpty 判定"全空死条目"：无标题、无歌手、且无时长（Title/Artist 去空白为空、
// Duration==0）。三者全空才算死条目（AND），只有标题/歌手/任一时长的正常歌曲
// 一律保留（本地音乐 Title 恒为文件名基名非空，天然不受影响）。
func isEmpty(t model.Track) bool {
	return strings.TrimSpace(t.Title) == "" &&
		strings.TrimSpace(t.Artist) == "" &&
		t.Duration == 0
}

// Add 追加到队尾。不改变当前曲目；队列从未播放过时仅入队、不自动播放。
// 全空死条目（无标题+无歌手+无时长）直接跳过，不入队。
func (q *Queue) Add(t model.Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if isEmpty(t) {
		return
	}
	q.tracks = append(q.tracks, t)
}

// InsertNext 插入到当前曲目之后（下一首播放）。不改变当前曲目、不自动开播；
// 无当前曲目（currentIdx=-1，如从未播放/清空后）时插入到队首（index 0）。
// 随机模式不重洗牌：插入位即实际下一首（"下一首播放"语义优先）。
// 全空死条目直接跳过，不入队。
func (q *Queue) InsertNext(t model.Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if isEmpty(t) {
		return
	}
	pos := 0
	if q.currentIdx >= 0 {
		pos = q.currentIdx + 1
	}
	q.tracks = append(q.tracks, model.Track{})
	copy(q.tracks[pos+1:], q.tracks[pos:])
	q.tracks[pos] = t
}

// Replace 替换语义：清空队列后把 t 作为唯一曲目并设为当前。
// 手动播放（搜索/历史/队列页）统一走此语义。
// t 为全空死条目时等价于清空队列（不入队、无当前曲目）。
func (q *Queue) Replace(t model.Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if isEmpty(t) {
		q.tracks = nil
		q.currentIdx = -1
		return
	}
	q.tracks = []model.Track{t}
	q.currentIdx = 0
}

// ReplaceAll 替换语义：清空队列后用整个播放列表填充，当前指针指向
// startIdx（播放列表页 Enter：从选中曲目开始连播整个列表）。
// startIdx 越界时 clamp 到合法范围；空列表清空队列（无当前曲目）。
// 随机模式下 currentIdx 之后的尾部一并洗牌（同 SetMode 语义），
// 保证“加载播放列表后随机直接可用”。
func (q *Queue) ReplaceAll(tracks []model.Track, startIdx int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// 先过滤全空死条目再替换（保持相对顺序，不改调用方切片）。
	// deadBefore 记录 startIdx 之前被过滤的条数：startIdx 指向正常曲时，
	// 过滤后下标平移该量，仍命中用户选中的同一首（选中曲为当前）；
	// startIdx 越界/指向死条目时沿用现有 clamp 到合法区间。
	kept := make([]model.Track, 0, len(tracks))
	deadBefore := 0
	for i, t := range tracks {
		if isEmpty(t) {
			if i < startIdx {
				deadBefore++
			}
			continue
		}
		kept = append(kept, t)
	}
	q.tracks = kept
	q.currentIdx = -1
	if len(q.tracks) == 0 {
		return
	}
	idx := startIdx - deadBefore
	validSel := startIdx >= 0 && startIdx < len(tracks) && !isEmpty(tracks[startIdx])
	if !validSel {
		idx = startIdx
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(q.tracks) {
		idx = len(q.tracks) - 1
	}
	q.currentIdx = idx
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
	q.mu.Lock()
	defer q.mu.Unlock()
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

// Move 把下标 from 的曲目移到最终下标 to（其余曲目相对顺序不变）。
// currentIdx 跟随同一首歌：被移曲是当前曲 → currentIdx = to；
// 移动跨越当前曲时相应 ±1。非法（from/to 越界、from==to、空/单曲队列）返回 false 且不改状态。
func (q *Queue) Move(from, to int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if from < 0 || from >= len(q.tracks) || to < 0 || to >= len(q.tracks) || from == to {
		return false
	}
	t := q.tracks[from]
	movedIsCurrent := from == q.currentIdx
	// 移除 from；被移曲是当前曲时先临时置 -1（插入阶段再落到 to）
	q.tracks = append(q.tracks[:from], q.tracks[from+1:]...)
	if movedIsCurrent {
		q.currentIdx = -1
	} else if q.currentIdx > from {
		q.currentIdx--
	}
	// 插入 to（新数组最终下标）
	q.tracks = append(q.tracks[:to], append([]model.Track{t}, q.tracks[to:]...)...)
	if movedIsCurrent {
		q.currentIdx = to
	} else if q.currentIdx >= to {
		q.currentIdx++
	}
	return true
}

// Clear 清空队列并清除当前曲目；模式保留（用户偏好）。
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = nil
	q.currentIdx = -1
}

// Next 推进到下一首并返回（当前曲目不变时返回 false 且不移动）。
// 播完列表回绕到队首（三种模式一致）；无当前曲目（如删除了末位当前曲）时从头开始。
func (q *Queue) Next() (model.Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tracks) == 0 {
		return model.Track{}, false
	}
	if q.currentIdx == -1 {
		q.currentIdx = 0
		return q.tracks[0], true
	}
	if q.currentIdx+1 >= len(q.tracks) {
		q.currentIdx = 0
		return q.tracks[0], true
	}
	q.currentIdx++
	return q.tracks[q.currentIdx], true
}

// PeekNext 返回 Next 将推进到的下一首但不改变状态（预加载预读用：
// 播放当前曲时提前看“下一首是谁”，无需推进队列）。与 Next 同语义：
// 空队列 false；无当前曲目（currentIdx==-1）返回队首；末尾回绕到队首；
// 单曲队列回绕返回自身（此时目标已缓存，CacheAsync no-op，天然安全）。
func (q *Queue) PeekNext() (model.Track, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if len(q.tracks) == 0 {
		return model.Track{}, false
	}
	if q.currentIdx == -1 {
		return q.tracks[0], true
	}
	if q.currentIdx+1 >= len(q.tracks) {
		return q.tracks[0], true
	}
	return q.tracks[q.currentIdx+1], true
}

// Prev 回退到上一首并返回；空队列返回 false。
// 无当前曲目时指向末尾；当前为首位时回绕到末尾；否则逐首回退。
func (q *Queue) Prev() (model.Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tracks) == 0 {
		return model.Track{}, false
	}
	if q.currentIdx == -1 || q.currentIdx == 0 {
		q.currentIdx = len(q.tracks) - 1
		return q.tracks[q.currentIdx], true
	}
	q.currentIdx--
	return q.tracks[q.currentIdx], true
}

// SetMode 切换播放模式。切入随机时只洗牌"当前曲之后"的曲目
// （不打断当前播放）；切回顺序保持当前数组顺序。同模式调用为 no-op。
func (q *Queue) SetMode(m Mode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if m == q.mode {
		return
	}
	q.mode = m
	if m != Shuffle {
		return
	}
	if q.currentIdx == -1 {
		// 无当前曲目：洗牌全部（同 ReplaceAll/尾部洗牌用 shuffleFn，
		// 可注入确定性置换供测试）
		shuffleFn(len(q.tracks), func(i, j int) {
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
	q.mu.Lock()
	defer q.mu.Unlock()
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
	q.mu.RLock()
	defer q.mu.RUnlock()
	return Snapshot{
		Tracks:     append([]model.Track(nil), q.tracks...),
		CurrentIdx: q.currentIdx,
		Mode:       q.mode,
	}
}

// Restore 用快照覆盖当前队列状态。先过滤全空死条目（保持相对顺序）。
// 当前曲位置按 ID 保留（ID 唯一性：youtube video id / 本地绝对路径）：
// 过滤会平移下标，故依 s.Tracks[s.CurrentIdx].ID 在新列表重新定位；
// 当前曲恰是死条目被过滤（找不到）或 CurrentIdx 越界（损坏/手改数据）
// 时降级为无当前曲目（-1），避免恢复出不可用状态。
func (q *Queue) Restore(s Snapshot) {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := make([]model.Track, 0, len(s.Tracks))
	for _, t := range s.Tracks {
		if !isEmpty(t) {
			kept = append(kept, t)
		}
	}
	q.tracks = kept
	q.mode = s.Mode
	q.currentIdx = -1
	if s.CurrentIdx < 0 || s.CurrentIdx >= len(s.Tracks) {
		return
	}
	curID := s.Tracks[s.CurrentIdx].ID
	for i, t := range q.tracks {
		if t.ID == curID {
			q.currentIdx = i
			return
		}
	}
}

// Current 返回当前曲目；无当前曲目时返回 false。
func (q *Queue) Current() (model.Track, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.currentIdx < 0 || q.currentIdx >= len(q.tracks) {
		return model.Track{}, false
	}
	return q.tracks[q.currentIdx], true
}

// Tracks 返回队列副本（供 UI 展示；修改返回值不影响队列）。
func (q *Queue) Tracks() []model.Track {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return append([]model.Track(nil), q.tracks...)
}

// CurrentIndex 返回当前曲目下标；-1 = 无当前曲目。
func (q *Queue) CurrentIndex() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.currentIdx
}

// Mode 返回当前播放模式。
func (q *Queue) Mode() Mode {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.mode
}

// Len 返回队列长度。
func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.tracks)
}

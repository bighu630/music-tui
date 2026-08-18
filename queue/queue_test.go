package queue

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"music-tui/model"
)

func testTrack(id string) model.Track {
	return model.Track{
		ID:       id,
		Title:    "曲 " + id,
		Artist:   "歌手",
		Duration: 180,
		URL:      "https://youtu.be/" + id,
		Source:   "youtube",
	}
}

func ids(ts []model.Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func sortedIDs(ts []model.Track) []string {
	s := ids(ts)
	sort.Strings(s)
	return s
}

func eq(a, b []string) bool { return reflect.DeepEqual(a, b) }

// ---- 初始状态 ----

func TestNewQueueEmpty(t *testing.T) {
	q := New()
	if q.Len() != 0 {
		t.Errorf("Len = %d, want 0", q.Len())
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("空队列 Current 应返回 false")
	}
	if len(q.Tracks()) != 0 {
		t.Error("空队列 Tracks 应为空")
	}
	if q.Mode() != Sequential {
		t.Errorf("Mode = %v, want Sequential", q.Mode())
	}
	if _, ok := q.Next(); ok {
		t.Error("空队列 Next 应返回 false")
	}
}

// ---- Add / Replace ----

func TestAddAppendsWithoutBecomingCurrent(t *testing.T) {
	q := New()
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	if q.Len() != 2 {
		t.Errorf("Len = %d, want 2", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("Tracks = %v, want [a b]", got)
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("仅 Add 不应产生当前曲目: CurrentIndex = %d", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("仅 Add 后 Current 应返回 false")
	}
}

func TestReplaceClearsAndSetsCurrent(t *testing.T) {
	q := New()
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	q.Replace(testTrack("c"))
	if q.Len() != 1 {
		t.Errorf("Len = %d, want 1", q.Len())
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.ID != "c" {
		t.Errorf("Current = %+v, want c", cur)
	}
}

func TestReplaceAllFillsFromStartIdx(t *testing.T) {
	q := New()
	q.Add(testTrack("old"))
	q.Add(testTrack("old2"))
	q.ReplaceAll([]model.Track{testTrack("a"), testTrack("b"), testTrack("c")}, 1)
	if q.Len() != 3 {
		t.Errorf("Len = %d, want 3", q.Len())
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.ID != "b" {
		t.Errorf("Current = %+v, want b", cur)
	}
	// 修改返回值不影响内部（复制语义）
	q.Tracks()[0] = testTrack("mutated")
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("ReplaceAll 未复制 tracks: Current = %s", cur.ID)
	}
}

func TestReplaceAllClampsStartIdx(t *testing.T) {
	q := New()
	tracks := []model.Track{testTrack("a"), testTrack("b")}
	q.ReplaceAll(tracks, -5)
	if q.CurrentIndex() != 0 {
		t.Errorf("负 startIdx 应 clamp 到 0, got %d", q.CurrentIndex())
	}
	q.ReplaceAll(tracks, 99)
	if q.CurrentIndex() != 1 {
		t.Errorf("超大 startIdx 应 clamp 到末位, got %d", q.CurrentIndex())
	}
}

func TestReplaceAllEmptyClears(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.ReplaceAll(nil, 0)
	if q.Len() != 0 || q.CurrentIndex() != -1 {
		t.Errorf("空列表应清空队列: Len=%d CurrentIndex=%d", q.Len(), q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("空队列不应有当前曲目")
	}
}

// TestReplaceAllShuffleShufflesTail 回归：随机模式下整列表替换后，当前曲之后的
// 尾部同样洗牌（复用 SetMode 的 tail-shuffle 语义）——模式保留、currentIdx 正确、
// 选中曲及之前的曲目保持原序、尾部被确定性置换（注入 shuffleFn 断言精确顺序）。
func TestReplaceAllShuffleShufflesTail(t *testing.T) {
	orig := shuffleFn
	// 确定性置换：逆序——若 ReplaceAll 不洗牌，顺序保持 [c..g] 测试即失败
	shuffleFn = func(n int, swap func(i, j int)) {
		for i := 0; i < n/2; i++ {
			swap(i, n-1-i)
		}
	}
	defer func() { shuffleFn = orig }()

	q := New()
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	q.SetMode(Shuffle) // 有曲目但无当前曲：洗牌全部并进入随机模式
	tracks := []model.Track{
		testTrack("a"), testTrack("b"), testTrack("c"),
		testTrack("d"), testTrack("e"), testTrack("f"), testTrack("g"),
	}
	q.ReplaceAll(tracks, 1) // b 当前：尾部 [c..g] 应被置换为 [g f e d c]
	if q.Mode() != Shuffle {
		t.Errorf("ReplaceAll 不应改变模式: Mode = %v, want Shuffle", q.Mode())
	}
	if q.CurrentIndex() != 1 {
		t.Fatalf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	got := ids(q.Tracks())
	if !eq(got[:2], []string{"a", "b"}) {
		t.Errorf("选中曲及之前曲目应保持原序: %v", got)
	}
	if !eq(got[2:], []string{"g", "f", "e", "d", "c"}) {
		t.Errorf("当前曲之后应被确定性置换（逆序）: %v, want [g f e d c]", got[2:])
	}
}

// TestShuffleFnNotAffectedByDefaultMode 顺序模式下 ReplaceAll 不洗牌（防御断言）。
func TestReplaceAllSequentialKeepsOrder(t *testing.T) {
	q := New()
	tracks := []model.Track{testTrack("a"), testTrack("b"), testTrack("c")}
	q.ReplaceAll(tracks, 0)
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("顺序模式不应洗牌: %v", got)
	}
}

func TestTracksReturnsCopy(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Tracks()[0] = testTrack("mutated")
	if cur, _ := q.Current(); cur.ID != "a" {
		t.Errorf("修改 Tracks() 返回值不应影响队列: Current = %s", cur.ID)
	}
}

// ---- Next ----

func TestNextSequentialOrderAndWrapsAtEnd(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	for i, want := range []string{"b", "c"} {
		tr, ok := q.Next()
		if !ok || tr.ID != want {
			t.Fatalf("Next[%d] = %v/%v, want %s", i, tr.ID, ok, want)
		}
	}
	// 末尾回绕到队首（列表循环）
	tr, ok := q.Next()
	if !ok || tr.ID != "a" {
		t.Fatalf("末尾 Next = %v/%v, want a（回绕）", tr.ID, ok)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("回绕后 CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	// 回绕后可继续按顺序推进
	tr, ok = q.Next()
	if !ok || tr.ID != "b" {
		t.Errorf("回绕后 Next = %v/%v, want b", tr.ID, ok)
	}
}

func TestNextWithNoCurrentStartsAtHead(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next()    // b
	q.Next()    // c 当前（2，末位）
	q.Remove(2) // 删除末位当前曲目 → 无当前曲目
	if q.CurrentIndex() != -1 {
		t.Fatalf("CurrentIndex = %d, want -1", q.CurrentIndex())
	}
	tr, ok := q.Next()
	if !ok || tr.ID != "a" {
		t.Errorf("无当前曲目时 Next 应从头开始: %v/%v", tr.ID, ok)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("Next 后 CurrentIndex = %d, want 0", q.CurrentIndex())
	}
}

func TestNextEmptyQueue(t *testing.T) {
	q := New()
	if _, ok := q.Next(); ok {
		t.Error("空队列 Next 应返回 false")
	}
}

func TestNextWrapsInShuffleMode(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.SetMode(Shuffle)
	order := ids(q.Tracks()) // 洗牌后显示顺序即播放顺序
	if order[0] != "a" {
		t.Fatalf("洗牌不应动当前曲: order[0] = %s, want a", order[0])
	}
	q.Next() // order[1]
	q.Next() // order[2]（末位）
	tr, ok := q.Next()
	if !ok || tr.ID != order[0] {
		t.Errorf("Shuffle 末尾 Next = %v/%v, want %s（回绕，不重洗）", tr.ID, ok, order[0])
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("回绕后 CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if got := ids(q.Tracks()); !eq(got, order) {
		t.Errorf("回绕不应重洗队列: %v vs %v", got, order)
	}
}

func TestNextAdvancesNormallyInRepeatOne(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.SetMode(RepeatOne)
	for i, want := range []string{"b", "c"} {
		tr, ok := q.Next()
		if !ok || tr.ID != want {
			t.Fatalf("RepeatOne Next[%d] = %v/%v, want %s", i, tr.ID, ok, want)
		}
	}
	// 单曲循环模式下队列推进同 Sequential：末尾回绕
	tr, ok := q.Next()
	if !ok || tr.ID != "a" {
		t.Errorf("RepeatOne 末尾 Next = %v/%v, want a（回绕）", tr.ID, ok)
	}
	if q.Mode() != RepeatOne {
		t.Errorf("Next 不应改变模式: %v", q.Mode())
	}
}

// ---- Prev ----

func TestPrevWrapsAround(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	// 首位 Prev 回绕到末尾
	tr, ok := q.Prev()
	if !ok || tr.ID != "c" {
		t.Fatalf("首位 Prev = %v/%v, want c（回绕到末尾）", tr.ID, ok)
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("Prev 后 CurrentIndex = %d, want 2", q.CurrentIndex())
	}
	// 继续逐首回退
	for i, want := range []string{"b", "a"} {
		tr, ok = q.Prev()
		if !ok || tr.ID != want {
			t.Fatalf("Prev[%d] = %v/%v, want %s", i, tr.ID, ok, want)
		}
	}
}

func TestPrevWithoutCurrentPointsToTail(t *testing.T) {
	q := New()
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	tr, ok := q.Prev()
	if !ok || tr.ID != "c" {
		t.Errorf("无当前曲目 Prev = %v/%v, want c（指向末尾）", tr.ID, ok)
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("Prev 后 CurrentIndex = %d, want 2", q.CurrentIndex())
	}
}

func TestPrevEmptyQueue(t *testing.T) {
	q := New()
	if _, ok := q.Prev(); ok {
		t.Error("空队列 Prev 应返回 false")
	}
}

func TestPrevSingleTrack(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	tr, ok := q.Prev()
	if !ok || tr.ID != "a" {
		t.Errorf("单曲 Prev = %v/%v, want a（回绕到自身）", tr.ID, ok)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("Prev 后 CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	// 无当前曲目的单曲队列：Prev 也应指向该曲
	q2 := New()
	q2.Add(testTrack("a"))
	tr, ok = q2.Prev()
	if !ok || tr.ID != "a" {
		t.Errorf("无当前曲目单曲 Prev = %v/%v, want a", tr.ID, ok)
	}
}

// ---- Remove ----

func TestRemoveTrackBeforeCurrent(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Remove(1) // 删 b（当前 a 之后… 注意 1 > 0 是删当前之后）
	if got := ids(q.Tracks()); !eq(got, []string{"a", "c"}) {
		t.Errorf("Tracks = %v, want [a c]", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", q.CurrentIndex())
	}
}

func TestRemoveTrackAfterCurrentKeepsIndex(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 成为当前（1）
	q.Remove(2)
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("Tracks = %v, want [a b]", got)
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
}

func TestRemoveBeforeCurrentDecrementsIndex(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前（1）
	q.Remove(0)
	if got := ids(q.Tracks()); !eq(got, []string{"b", "c"}) {
		t.Errorf("Tracks = %v, want [b c]", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
}

func TestRemoveCurrentSlidesNextIntoPlace(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Remove(0) // 删当前 a → 顺延 b 成为当前
	if got := ids(q.Tracks()); !eq(got, []string{"b", "c"}) {
		t.Errorf("Tracks = %v, want [b c]", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b（顺延下一首）", cur.ID)
	}
}

func TestRemoveCurrentTailLeavesNoCurrent(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Next() // b 当前（1，末位）
	q.Remove(1)
	if q.Len() != 1 {
		t.Errorf("Len = %d, want 1", q.Len())
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1（无顺延）", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("删除末位当前曲目后 Current 应返回 false")
	}
	tr, ok := q.Next()
	if !ok || tr.ID != "a" {
		t.Errorf("删除末位当前后 Next 应从头: %v/%v", tr.ID, ok)
	}
}

func TestRemoveCurrentFromSingleTrackQueue(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Remove(0)
	if q.Len() != 0 || q.CurrentIndex() != -1 {
		t.Errorf("Len/CurrentIndex = %d/%d, want 0/-1", q.Len(), q.CurrentIndex())
	}
	if _, ok := q.Next(); ok {
		t.Error("空队列 Next 应返回 false")
	}
}

func TestRemoveInvalidIndexIgnored(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Remove(-1)
	q.Remove(5)
	if q.Len() != 2 || q.CurrentIndex() != 0 {
		t.Errorf("非法下标应被忽略: Len/CurrentIndex = %d/%d", q.Len(), q.CurrentIndex())
	}
}

// ---- Move（队列页 m 键移动模式：把 from 移到最终下标 to，currentIdx 跟随同一首歌） ----

// 普通曲跨当前曲移动：被移曲在当前位置之后、前移到当前曲之前。
// [a b c d] 当前 a(0)，Move(3,0)：d 移到队首 → [d a b c]，currentIdx 仍指向 a(1)。
func TestMoveTrackAfterCurrentToFront(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	if !q.Move(3, 0) {
		t.Fatal("Move(3,0) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"d", "a", "b", "c"}) {
		t.Errorf("Tracks = %v, want [d a b c]", got)
	}
	if q.Len() != 4 {
		t.Errorf("Len = %d, want 4", q.Len())
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "a" {
		t.Errorf("Current = %s, want a（当前曲应跟随原歌）", cur.ID)
	}
}

// 普通曲跨当前曲移动：被移曲在当前位置之前、后移到当前曲之后。
// [a b c d] 当前 c(2)，Move(0,3)：a 移到队尾 → [b c d a]，currentIdx 仍指向 c(1)。
func TestMoveTrackBeforeCurrentToTail(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.Next() // b
	q.Next() // c 当前（2）
	if !q.Move(0, 3) {
		t.Fatal("Move(0,3) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"b", "c", "d", "a"}) {
		t.Errorf("Tracks = %v, want [b c d a]", got)
	}
	if q.Len() != 4 {
		t.Errorf("Len = %d, want 4", q.Len())
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "c" {
		t.Errorf("Current = %s, want c（当前曲应跟随原歌）", cur.ID)
	}
}

// 移动当前曲本身：currentIdx = 新位置，Current() 仍是同一首歌。
// [a b c d] 当前 b(1)，Move(1,2) → [a c b d]，currentIdx = 2。
func TestMoveCurrentTrackToMiddle(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.Next() // b 当前（1）
	if !q.Move(1, 2) {
		t.Fatal("Move(1,2) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "c", "b", "d"}) {
		t.Errorf("Tracks = %v, want [a c b d]", got)
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("CurrentIndex = %d, want 2", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
}

// 移动当前曲到队首：[a b c d] 当前 b(1)，Move(1,0) → [b a c d]，currentIdx = 0。
func TestMoveCurrentTrackToFront(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.Next() // b 当前（1）
	if !q.Move(1, 0) {
		t.Fatal("Move(1,0) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"b", "a", "c", "d"}) {
		t.Errorf("Tracks = %v, want [b a c d]", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
}

// 移动当前曲到队尾：[a b c d] 当前 b(1)，Move(1,3) → [a c d b]，currentIdx = 3。
func TestMoveCurrentTrackToTail(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.Next() // b 当前（1）
	if !q.Move(1, 3) {
		t.Fatal("Move(1,3) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "c", "d", "b"}) {
		t.Errorf("Tracks = %v, want [a c d b]", got)
	}
	if q.CurrentIndex() != 3 {
		t.Errorf("CurrentIndex = %d, want 3", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
}

// 无当前曲目（仅 Add 入队）时移动：currentIdx 保持 -1，顺序正确。
func TestMoveWithoutCurrentKeepsNone(t *testing.T) {
	q := New()
	for _, id := range []string{"a", "b", "c", "d"} {
		q.Add(testTrack(id))
	}
	if !q.Move(0, 2) {
		t.Fatal("Move(0,2) 应成功")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"b", "c", "a", "d"}) {
		t.Errorf("Tracks = %v, want [b c a d]", got)
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1（无当前曲目）", q.CurrentIndex())
	}
}

func TestMoveSameIndexRejected(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	if q.Move(1, 1) {
		t.Error("Move(1,1) 应返回 false")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("失败移动不应改变队列: %v", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("失败移动不应改变指针: %d", q.CurrentIndex())
	}
}

func TestMoveOutOfRangeRejected(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	if q.Move(-1, 0) || q.Move(3, 0) || q.Move(0, -1) || q.Move(0, 3) {
		t.Error("越界移动应返回 false")
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("失败移动不应改变队列: %v", got)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("失败移动不应改变指针: %d", q.CurrentIndex())
	}
}

func TestMoveEmptyQueueRejected(t *testing.T) {
	q := New()
	if q.Move(0, 1) || q.Move(-1, 0) {
		t.Error("空队列 Move 应返回 false")
	}
	if q.Len() != 0 || q.CurrentIndex() != -1 {
		t.Errorf("空队列状态不应改变: Len=%d CurrentIndex=%d", q.Len(), q.CurrentIndex())
	}
}

func TestMoveSingleTrackQueueRejected(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	if q.Move(0, 1) || q.Move(1, 0) || q.Move(0, 0) {
		t.Error("单曲队列 Move 应返回 false")
	}
	if q.Len() != 1 || q.CurrentIndex() != 0 {
		t.Errorf("单曲队列状态不应改变: Len=%d CurrentIndex=%d", q.Len(), q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "a" {
		t.Errorf("Current = %s, want a", cur.ID)
	}
}

// 连播顺序回归：移动后依次 Next() 的顺序 = 新数组顺序（含末尾回绕）。
// [a b c d e] 当前 b(1)，Move(1,4) → [a c d e b]，currentIdx = 4（b 在队尾）；
// 从 b 连播：回绕到 a → c → d → e → b，即新数组完整一轮。
func TestMovePreservesPlaybackOrder(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d", "e"} {
		q.Add(testTrack(id))
	}
	q.Next() // b 当前（1）
	if !q.Move(1, 4) {
		t.Fatal("Move(1,4) 应成功")
	}
	want := []string{"a", "c", "d", "e", "b"}
	if got := ids(q.Tracks()); !eq(got, want) {
		t.Fatalf("Tracks = %v, want %v", got, want)
	}
	if q.CurrentIndex() != 4 {
		t.Fatalf("CurrentIndex = %d, want 4", q.CurrentIndex())
	}
	for i, wantID := range want {
		tr, ok := q.Next()
		if !ok {
			t.Fatalf("Next[%d] 失败", i)
		}
		if tr.ID != wantID {
			t.Fatalf("Next[%d] = %s, want %s（连播顺序 = 新数组顺序）", i, tr.ID, wantID)
		}
	}
}

// ---- Clear ----

func TestClearEmptiesAndKeepsMode(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.SetMode(Shuffle)
	q.Clear()
	if q.Len() != 0 || q.CurrentIndex() != -1 {
		t.Errorf("Clear 后 Len/CurrentIndex = %d/%d, want 0/-1", q.Len(), q.CurrentIndex())
	}
	if q.Mode() != Shuffle {
		t.Errorf("Clear 应保留模式, Mode = %v", q.Mode())
	}
	if _, ok := q.Next(); ok {
		t.Error("清空后 Next 应返回 false")
	}
}

// ---- JumpTo（队列页 Enter 跳转语义） ----

func TestJumpToMovesCurrentKeepingQueue(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	if !q.JumpTo(2) {
		t.Fatal("JumpTo(2) 应成功")
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("CurrentIndex = %d, want 2", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "c" {
		t.Errorf("Current = %s, want c", cur.ID)
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("跳转不应改变队列内容: %v", got)
	}
	// 跳转到末位后 Next 回绕到第一首
	tr, ok := q.Next()
	if !ok || tr.ID != "a" {
		t.Fatalf("跳转到末位后 Next = %v/%v, want a（回绕）", tr.ID, ok)
	}
	// 跳回中间后从跳转处继续
	if !q.JumpTo(0) {
		t.Fatal("JumpTo(0) 应成功")
	}
	tr, ok = q.Next()
	if !ok || tr.ID != "b" {
		t.Errorf("跳回后 Next = %s/%v, want b", tr.ID, ok)
	}
}

func TestJumpToInvalidIndex(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	if q.JumpTo(-1) || q.JumpTo(1) {
		t.Error("非法下标应返回 false")
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("失败跳转不应移动指针: CurrentIndex = %d", q.CurrentIndex())
	}
	if New().JumpTo(0) {
		t.Error("空队列 JumpTo 应失败")
	}
}

// ---- Snapshot / Restore（会话持久化） ----

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前（1）
	q.SetMode(Shuffle)

	s := q.Snapshot()
	if got := ids(s.Tracks); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("Snapshot.Tracks = %v", got)
	}
	if s.CurrentIdx != 1 || s.Mode != Shuffle {
		t.Errorf("Snapshot = %+v, want current=1 mode=Shuffle", s)
	}

	nq := New()
	nq.Restore(s)
	if nq.Len() != 3 || nq.CurrentIndex() != 1 || nq.Mode() != Shuffle {
		t.Errorf("Restore 后 Len/Current/Mode = %d/%d/%v", nq.Len(), nq.CurrentIndex(), nq.Mode())
	}
	if cur, ok := nq.Current(); !ok || cur.ID != "b" {
		t.Errorf("Restore 后 Current = %s/%v, want b", cur.ID, ok)
	}
	// 恢复后播放行为一致：Next 从 b 推进到 c
	tr, ok := nq.Next()
	if !ok || tr.ID != "c" {
		t.Errorf("Restore 后 Next = %s/%v, want c", tr.ID, ok)
	}
}

func TestSnapshotReturnsCopy(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	s := q.Snapshot()
	s.Tracks[0] = testTrack("mutated")
	s.CurrentIdx = 0
	if cur, _ := q.Current(); cur.ID != "a" {
		t.Errorf("修改 Snapshot 不应影响队列: Current = %s", cur.ID)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex = %d, want 0（Restore 前原值）", q.CurrentIndex())
	}
}

func TestRestoreInvalidCurrentIndexClampedToNone(t *testing.T) {
	q := New()
	q.Add(testTrack("a"))
	q.Restore(Snapshot{Tracks: []model.Track{testTrack("a")}, CurrentIdx: 5, Mode: Sequential})
	if q.CurrentIndex() != -1 {
		t.Errorf("越界 CurrentIdx 应降级为无当前曲目: %d", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("越界 CurrentIdx 后 Current 应返回 false")
	}
	q.Restore(Snapshot{Tracks: []model.Track{testTrack("a")}, CurrentIdx: -2, Mode: Sequential})
	if q.CurrentIndex() != -1 {
		t.Errorf("负数越界 CurrentIdx 应降级为无当前曲目: %d", q.CurrentIndex())
	}
}

// ---- SetMode ----

func TestSetModeShuffleKeepsCurrentAndPrefix(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d", "e", "f", "g"} {
		q.Add(testTrack(id))
	}
	before := sortedIDs(q.Tracks())
	q.SetMode(Shuffle)
	after := ids(q.Tracks())
	if !eq(sortedIDs(q.Tracks()), before) {
		t.Errorf("洗牌不应改变曲目集合: %v vs %v", after, before)
	}
	if after[0] != "a" {
		t.Errorf("洗牌不应动当前曲: after[0] = %s, want a", after[0])
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("洗牌后 CurrentIndex = %d, want 0", q.CurrentIndex())
	}
	if q.Mode() != Shuffle {
		t.Errorf("Mode = %v, want Shuffle", q.Mode())
	}
	// 播放顺序 = 显示顺序（洗牌后数组顺序即实际播放顺序）
	for i := 1; i < len(after); i++ {
		tr, ok := q.Next()
		if !ok || tr.ID != after[i] {
			t.Fatalf("Next[%d] = %s/%v, want 显示顺序 %s", i, tr.ID, ok, after[i])
		}
	}
	// 随机模式播完回绕到队首（不重洗）
	tr, ok := q.Next()
	if !ok || tr.ID != after[0] {
		t.Errorf("随机模式末尾 Next = %v/%v, want %s（回绕）", tr.ID, ok, after[0])
	}
}

func TestSetModeShuffleWithoutCurrentShufflesAll(t *testing.T) {
	q := New()
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		q.Add(testTrack(id))
	}
	before := sortedIDs(q.Tracks())
	q.SetMode(Shuffle)
	if !eq(sortedIDs(q.Tracks()), before) {
		t.Error("无当前曲目时洗牌不应改变曲目集合")
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1", q.CurrentIndex())
	}
}

func TestSetModeShuffleAtTailIsNoop(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Next() // b 当前（1，末位，无可洗牌的尾部）
	q.SetMode(Shuffle)
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("末位时洗牌应为 no-op: %v", got)
	}
	if q.Mode() != Shuffle {
		t.Errorf("Mode = %v, want Shuffle", q.Mode())
	}
}

func TestSetModeBackToSequentialKeepsOrder(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.SetMode(Shuffle)
	shuffled := ids(q.Tracks())
	q.SetMode(Sequential)
	if got := ids(q.Tracks()); !eq(got, shuffled) {
		t.Errorf("切回顺序应保持洗牌后的顺序: %v vs %v", got, shuffled)
	}
	if q.Mode() != Sequential {
		t.Errorf("Mode = %v, want Sequential", q.Mode())
	}
}

// ---- PeekNext（预加载预读：返回 Next 将推进到的下一首，不改变状态） ----

func TestPeekNextEmptyQueue(t *testing.T) {
	q := New()
	if _, ok := q.PeekNext(); ok {
		t.Error("空队列 PeekNext 应返回 false")
	}
}

func TestPeekNextWithoutCurrentReturnsHead(t *testing.T) {
	q := New()
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	tr, ok := q.PeekNext()
	if !ok || tr.ID != "a" {
		t.Errorf("无当前曲目 PeekNext = %v/%v, want a（队首）", tr.ID, ok)
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("PeekNext 不应改变状态: CurrentIndex = %d, want -1", q.CurrentIndex())
	}
}

func TestPeekNextMiddle(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前（1）
	tr, ok := q.PeekNext()
	if !ok || tr.ID != "c" {
		t.Errorf("中间 PeekNext = %v/%v, want c", tr.ID, ok)
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("PeekNext 不应推进: CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	// 与 Next 一致性：PeekNext 所见即 Next 所达
	tr2, _ := q.Next()
	if tr2.ID != tr.ID {
		t.Errorf("PeekNext 与 Next 不一致: peek %s, next %s", tr.ID, tr2.ID)
	}
}

func TestPeekNextWrapsAtEnd(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b
	q.Next() // c（末位，2）
	tr, ok := q.PeekNext()
	if !ok || tr.ID != "a" {
		t.Errorf("末尾 PeekNext = %v/%v, want a（回绕到队首）", tr.ID, ok)
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("PeekNext 不应推进: CurrentIndex = %d, want 2", q.CurrentIndex())
	}
	// 与 Next 一致性：回绕后 Next 到达队首
	tr2, _ := q.Next()
	if tr2.ID != "a" {
		t.Errorf("回绕后 Next = %s, want a（与 PeekNext 一致）", tr2.ID)
	}
}

func TestPeekNextSingleTrackWrapsToSelf(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	tr, ok := q.PeekNext()
	if !ok || tr.ID != "a" {
		t.Errorf("单曲 PeekNext = %v/%v, want a（回绕到自身）", tr.ID, ok)
	}
	if q.CurrentIndex() != 0 {
		t.Errorf("PeekNext 不应推进: CurrentIndex = %d, want 0", q.CurrentIndex())
	}
}

// 多次 PeekNext 调用后队列状态（快照）完全不变：预读不推进、不回绕、不洗牌。
func TestPeekNextStateUnchanged(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b 当前（1）
	before := q.Snapshot()
	for i := 0; i < 3; i++ {
		if _, ok := q.PeekNext(); !ok {
			t.Fatal("非空队列 PeekNext 应返回 true")
		}
	}
	after := q.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("多次 PeekNext 不应改变队列状态:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSetModeSameModeIsNoop(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	for _, id := range []string{"b", "c", "d"} {
		q.Add(testTrack(id))
	}
	q.SetMode(Shuffle)
	first := ids(q.Tracks())
	q.SetMode(Shuffle) // 重复切随机不应再次洗牌
	if got := ids(q.Tracks()); !eq(got, first) {
		t.Errorf("同模式调用应 no-op: %v vs %v", got, first)
	}
}

// ---- InsertNext ----

func TestInsertNextAfterCurrent(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(1) // 当前 t2
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"t1", "t2", "tx", "t3"}) {
		t.Fatalf("Tracks = %v, want [t1 t2 tx t3]", got)
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "t2" {
		t.Errorf("Current = %s, want t2", cur.ID)
	}
	if next, ok := q.Next(); !ok || next.ID != "tx" {
		t.Errorf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}

func TestInsertNextWithoutCurrentInsertsAtHead(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	// currentIdx 仍为 -1（Add 不改变当前曲）
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"tx", "t1", "t2", "t3"}) {
		t.Fatalf("Tracks = %v, want [tx t1 t2 t3]", got)
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("插入不应改变当前曲目")
	}
}

func TestInsertNextEmptyQueue(t *testing.T) {
	q := New()
	q.InsertNext(testTrack("t1"))
	if q.Len() != 1 || q.CurrentIndex() != -1 {
		t.Errorf("Len/CurrentIndex = %d/%d, want 1/-1", q.Len(), q.CurrentIndex())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"t1"}) {
		t.Fatalf("Tracks = %v, want [t1]", got)
	}
	if _, ok := q.Current(); ok {
		t.Error("空队列插入不应有当前曲目")
	}
}

func TestInsertNextKeepsPositionInShuffle(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(0)
	q.SetMode(Shuffle) // 洗牌 t1 之后
	q.InsertNext(testTrack("tx"))
	// InsertNext 不重洗牌：插入位即实际下一首
	next, ok := q.Next()
	if !ok || next.ID != "tx" {
		t.Fatalf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}

func TestInsertNextAfterRemoveCurrent(t *testing.T) {
	q := New()
	for _, id := range []string{"t1", "t2", "t3"} {
		q.Add(testTrack(id))
	}
	q.JumpTo(0) // 当前 t1
	q.Remove(0) // 顺延：当前变 t2（index 0）
	q.InsertNext(testTrack("tx"))
	if got := ids(q.Tracks()); !eq(got, []string{"t2", "tx", "t3"}) {
		t.Fatalf("Tracks = %v, want [t2 tx t3]", got)
	}
	if next, ok := q.Next(); !ok || next.ID != "tx" {
		t.Errorf("Next = %s/%v, want tx/true", next.ID, ok)
	}
}

// TestConcurrentAccess 并发读+写队列不产生数据竞争（-race 下运行；
// 服务 MPRIS 从 D-Bus goroutine 同步读 Len/Mode 的前提）。
func TestConcurrentAccess(t *testing.T) {
	q := New()
	for i := 0; i < 50; i++ {
		q.Add(model.Track{ID: fmt.Sprintf("t%d", i), Title: "t"})
	}
	q.SetMode(Shuffle)
	var wg sync.WaitGroup
	// 读者：D-Bus goroutine 模拟（Mode/Len/Tracks/Current/Snapshot）
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = q.Mode()
				_ = q.Len()
				_ = q.CurrentIndex()
				_, _ = q.Current()
				_ = q.Tracks()
				_ = q.Snapshot()
			}
		}()
	}
	// 写者：UI goroutine 模拟
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			q.Add(model.Track{ID: "x", Title: "x"})
			q.Remove(0)
			q.Next()
			q.Prev()
			q.SetMode(Sequential)
			q.SetMode(Shuffle)
			q.JumpTo(3)
		}
	}()
	wg.Wait()
}

// ---- 全空死条目过滤（无标题+无歌手+无时长 AND 条件） ----

// 连续 Add 死条目（三字段全空）→ 都不入队；正常曲目照常入队。
func TestAddFiltersFullDeadEntry(t *testing.T) {
	q := New()
	for i := 0; i < 3; i++ {
		q.Add(model.Track{ID: fmt.Sprintf("dead%d", i)}) // Title/Artist/Duration 全空
	}
	if q.Len() != 0 {
		t.Errorf("连续 Add 死条目后 Len = %d, want 0", q.Len())
	}
	if _, ok := q.Current(); ok {
		t.Error("死条目不应成为当前曲")
	}
	q.Add(testTrack("a"))
	q.Add(testTrack("b"))
	if q.Len() != 2 {
		t.Errorf("正常曲目 Add 后 Len = %d, want 2", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("Tracks = %v, want [a b]", got)
	}
}

// 部分字段缺失 → 保留（反误删：只任一字段有值的正常歌曲一律不过滤）。
func TestAddKeepsPartialFields(t *testing.T) {
	// 只 Title 有值（Artist="", Duration=0）
	titleOnly := testTrack("title")
	titleOnly.Artist = ""
	titleOnly.Duration = 0
	// 只 Artist 有值（Title="", Duration=0）
	artistOnly := testTrack("artist")
	artistOnly.Title = ""
	artistOnly.Duration = 0
	// 只时长 >0（Title=Artist=""）
	durationOnly := testTrack("dur")
	durationOnly.Title = ""
	durationOnly.Artist = ""

	q := New()
	q.Add(titleOnly)
	q.Add(artistOnly)
	q.Add(durationOnly)
	if q.Len() != 3 {
		t.Errorf("部分字段缺失应保留: Len = %d, want 3", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"title", "artist", "dur"}) {
		t.Errorf("Tracks = %v, want [title artist dur]", got)
	}
}

// 本地音乐风格：Title=文件名（非空）、Artist="", Duration=0 → 保留（核心场景）。
func TestAddKeepsLocalTrackStyle(t *testing.T) {
	local := model.Track{
		ID:     "/home/user/Music/Blessed.mp3",
		Title:  "Blessed",
		Artist: "",
		Source: "local",
	}
	q := New()
	q.Add(local)
	q.Add(model.Track{ID: "dead"})
	if q.Len() != 1 {
		t.Errorf("本地音乐风格应保留、死条目应过滤: Len = %d, want 1", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"/home/user/Music/Blessed.mp3"}) {
		t.Errorf("Tracks = %v, want 仅本地曲", got)
	}
}

// 空白串视为缺失：Title/Artist 全空白且时长 0 → 过滤；去空白后非空 → 保留。
func TestAddTrimsWhitespaceBeforeFiltering(t *testing.T) {
	q := New()
	wsOnly := model.Track{ID: "ws", Title: "   ", Artist: " \t ", Duration: 0}
	q.Add(wsOnly)
	if q.Len() != 0 {
		t.Errorf("全空白串应视为缺失并过滤: Len = %d, want 0", q.Len())
	}
	// 标题带首尾空白但去空白非空 → 与全空不同，保留
	padded := model.Track{ID: "pad", Title: "  歌名  ", Artist: " ", Duration: 0}
	q.Add(padded)
	if q.Len() != 1 {
		t.Errorf("去空白非空的标题应保留: Len = %d, want 1", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"pad"}) {
		t.Errorf("Tracks = %v, want [pad]", got)
	}
}

// InsertNext 死条目 → 跳过（队列不变）；正常曲照常插入当前之后。
func TestInsertNextFiltersDeadEntry(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.InsertNext(model.Track{ID: "dead"})
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("InsertNext 死条目应被过滤: %v, want [a b]", got)
	}
	if q.Len() != 2 {
		t.Errorf("Len = %d, want 2", q.Len())
	}
	q.InsertNext(testTrack("x"))
	if got := ids(q.Tracks()); !eq(got, []string{"a", "x", "b"}) {
		t.Errorf("正常 InsertNext = %v, want [a x b]", got)
	}
	if next, _ := q.Next(); next.ID != "x" {
		t.Errorf("Next = %s, want x", next.ID)
	}
}

// Replace 死条目 → 等价清空队列（Len 0 + 无当前曲目）。
func TestReplaceDeadEntryClearsQueue(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Replace(model.Track{ID: "dead"})
	if q.Len() != 0 {
		t.Errorf("Replace 死条目后 Len = %d, want 0（等价清空）", q.Len())
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("Replace 死条目后 CurrentIndex = %d, want -1", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("Replace 死条目后不应有当前曲目")
	}
	// 之后正常曲恢复照常
	q.Replace(testTrack("c"))
	if q.Len() != 1 || q.CurrentIndex() != 0 {
		t.Errorf("Replace 正常曲后 Len/CurrentIndex = %d/%d, want 1/0", q.Len(), q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "c" {
		t.Errorf("Current = %s, want c", cur.ID)
	}
}

// ReplaceAll 过滤死条目、保持其余相对顺序、不修改调用方切片。
func TestReplaceAllFiltersDeadKeepingOrder(t *testing.T) {
	q := New()
	q.Replace(testTrack("old"))
	tracks := []model.Track{
		testTrack("a"),
		model.Track{ID: "d1"},
		testTrack("b"),
		model.Track{ID: "d2"},
		testTrack("c"),
	}
	q.ReplaceAll(tracks, 0)
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("ReplaceAll 应过滤死条目保持顺序: %v, want [a b c]", got)
	}
	if q.Len() != 3 {
		t.Errorf("Len = %d, want 3", q.Len())
	}
	if got := ids(tracks); !eq(got, []string{"a", "d1", "b", "d2", "c"}) {
		t.Errorf("调用方切片不应被修改: %v", got)
	}
	// 连播顺序 = 过滤后顺序（顺序模式不洗牌）：当前 a → b → c → 回绕 a
	if tr, ok := q.Next(); !ok || tr.ID != "b" {
		t.Errorf("Next = %v/%v, want b", tr.ID, ok)
	}
	if tr, ok := q.Next(); !ok || tr.ID != "c" {
		t.Errorf("Next = %v/%v, want c", tr.ID, ok)
	}
	if tr, ok := q.Next(); !ok || tr.ID != "a" {
		t.Errorf("回绕 Next = %v/%v, want a", tr.ID, ok)
	}
}

// startIdx 选中正常曲且其前有死条目：过滤后当前曲下标平移，仍是同一首为当前。
func TestReplaceAllStartIdxPreservesSelectedTrack(t *testing.T) {
	q := New()
	tracks := []model.Track{
		model.Track{ID: "d1"},
		testTrack("a"),
		model.Track{ID: "d2"},
		testTrack("b"),
		testTrack("c"),
	}
	// startIdx=3 选中 b（其前 2 个死条目 → 过滤后 b 移到下标 1，仍为当前）
	q.ReplaceAll(tracks, 3)
	if q.Len() != 3 {
		t.Fatalf("Len = %d, want 3", q.Len())
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1（平移后的 b）", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.ID != "b" {
		t.Errorf("Current = %s/%v, want b（选中曲仍为当前）", cur.ID, ok)
	}
}

// startIdx 在过滤后列表上仍生效：超大/负数照常 clamp 到合法区间。
func TestReplaceAllStartIdxClampAfterFilter(t *testing.T) {
	q := New()
	tracks := []model.Track{model.Track{ID: "d1"}, testTrack("a"), testTrack("b")}
	q.ReplaceAll(tracks, 99)
	if q.CurrentIndex() != 1 {
		t.Errorf("超大 startIdx 应 clamp 到过滤后末位: %d, want 1", q.CurrentIndex())
	}
	if cur, _ := q.Current(); cur.ID != "b" {
		t.Errorf("Current = %s, want b", cur.ID)
	}
	q2 := New()
	q2.ReplaceAll(tracks, -5)
	if q2.CurrentIndex() != 0 {
		t.Errorf("负 startIdx 应 clamp 到 0: %d", q2.CurrentIndex())
	}
	if cur, _ := q2.Current(); cur.ID != "a" {
		t.Errorf("Current = %s, want a", cur.ID)
	}
}

// ReplaceAll 全部死条目 → 空队列、无当前曲目。
func TestReplaceAllAllDeadClears(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.ReplaceAll([]model.Track{{ID: "d1"}, {ID: "d2"}}, 1)
	if q.Len() != 0 || q.CurrentIndex() != -1 {
		t.Errorf("全部死条目应清空: Len/CurrentIndex = %d/%d, want 0/-1", q.Len(), q.CurrentIndex())
	}
	if _, ok := q.Next(); ok {
		t.Error("空队列 Next 应 false")
	}
}

// Restore 过滤死条目、当前曲按下标平移保留（存活相对序不变，落在其前存活条数处）。
func TestRestoreFiltersDeadAndPreservesCurrentByShift(t *testing.T) {
	src := Snapshot{
		Tracks: []model.Track{
			testTrack("a"),
			model.Track{ID: "d1"},
			testTrack("b"),
			model.Track{ID: "d2"},
		},
		CurrentIdx: 2, // 指向 b（其前有一死条目）
		Mode:       Sequential,
	}
	q := New()
	q.Restore(src)
	if q.Len() != 2 {
		t.Errorf("Restore 过滤死条目后 Len = %d, want 2", q.Len())
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b"}) {
		t.Errorf("Tracks = %v, want [a b]", got)
	}
	if q.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1（b 平移后仍为当前）", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.ID != "b" {
		t.Errorf("Current = %s/%v, want b", cur.ID, ok)
	}
	// 恢复后连播正常：b → 回绕 a
	if tr, _ := q.Next(); tr.ID != "a" {
		t.Errorf("Restore 后 Next = %s, want a（回绕）", tr.ID)
	}
}

// ---- Restore 定位回归（ae245ba 后续修复：按 ID 定位的两个边界 bug） ----

// 回归 bug1（空 ID 碰撞）：死条目可带 ID（乃至 ""），isEmpty 不看 ID；
// 存活曲 ID=""（有标题无 ID）与死条目 ID="" 并存时，旧按 ID 定位循环会命中
// 存活曲把 currentIdx 设为它而非 -1。下标平移对此天然免疫 → 降级 -1。
func TestRestoreEmptyCurrentIDDegrades(t *testing.T) {
	// 存活曲：有标题+有时长，但 ID 为空串（与死条目同 ID，会碰撞旧实现）
	alive := model.Track{Title: "local 曲", Artist: "", Duration: 180, ID: ""}
	// 死条目：ID 也为空串（isEmpty 只看 Title/Artist/Duration，不看 ID）
	dnr := model.Track{ID: ""}
	q := New()
	q.Restore(Snapshot{
		Tracks:     []model.Track{alive, dnr},
		CurrentIdx: 1, // 当前曲是死条目（应降级 -1，不得因 ID 碰撞命中 alive 的 0）
		Mode:       Sequential,
	})
	if q.Len() != 1 {
		t.Errorf("Len = %d, want 1（仅存活曲保留）", q.Len())
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("CurrentIndex = %d, want -1（当前曲是死条目、ID 碰撞不应命中存活曲）", q.CurrentIndex())
	}
	if _, ok := q.Current(); ok {
		t.Error("降级后不应有当前曲")
	}
}

// 回归 bug2（重复 ID 误定位）：队列可含重复 ID（Add 不去重/播放列表不去重）。
// 快照当前指向第 2 处重复 ID 时，旧按 ID 循环命中第 1 处。下标平移按存活
// 相对序定位 → 精确命中快照所指的同一处。
func TestRestoreDuplicateIDPointsToCorrectOccurrence(t *testing.T) {
	mk := func(title string) model.Track {
		return model.Track{ID: "dup", Title: title, Artist: "歌手", Duration: 180}
	}
	tracks := []model.Track{
		mk("第一处"), // ID=dup
		testTrack("other"),
		mk("第二处"), // ID=dup（Title 各异以区分）
	}
	// CurrentIdx=2 → 第 2 个 dup，恢复后仍为下标 2（不得误命中第 1 处为 0）
	q := New()
	q.Restore(Snapshot{Tracks: tracks, CurrentIdx: 2, Mode: Sequential})
	if q.CurrentIndex() != 2 {
		t.Errorf("CurrentIndex = %d, want 2（第 2 处 dup；不得误定位到第 1 处）", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.Title != "第二处" {
		t.Errorf("Current = %s/%v, want 第二处", cur.Title, ok)
	}
	// CurrentIdx=1 → other，恢复后仍为下标 1（顺序复验）
	q2 := New()
	q2.Restore(Snapshot{Tracks: tracks, CurrentIdx: 1, Mode: Sequential})
	if q2.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex = %d, want 1（other）", q2.CurrentIndex())
	}
	if cur, ok := q2.Current(); !ok || cur.ID != "other" {
		t.Errorf("Current = %+v/%v, want other", cur, ok)
	}
}

// 增强回归：当前曲前有多个死条目（下标平移多格）且当前曲存活时，
// 新下标 = 其前存活条数；同时复验首位存活/单曲存活场景（起始 0 无 off-by-one）。
func TestRestoreShiftsCurrentAfterDeadBefore(t *testing.T) {
	tracks := []model.Track{
		testTrack("a"), // 正常
		model.Track{ID: "d1"},
		testTrack("b"), // 正常
		model.Track{ID: "d2"},
		testTrack("c"), // 当前（其前存活 a、b 共 2 条）
	}
	q := New()
	q.Restore(Snapshot{Tracks: tracks, CurrentIdx: 4, Mode: Sequential})
	if q.CurrentIndex() != 2 {
		t.Errorf("CurrentIndex = %d, want 2（a,b 存活前置 2 条 → 平移后 c）", q.CurrentIndex())
	}
	if cur, ok := q.Current(); !ok || cur.ID != "c" {
		t.Errorf("Current = %s/%v, want c", cur.ID, ok)
	}
	if got := ids(q.Tracks()); !eq(got, []string{"a", "b", "c"}) {
		t.Errorf("Tracks = %v, want [a b c]", got)
	}
	// 单曲存活场景（起始必须为 0 而不是 -1，否则 off-by-one 得 0 而非预期 0）
	singleton := []model.Track{model.Track{ID: "d"}, testTrack("x")}
	qs := New()
	qs.Restore(Snapshot{Tracks: singleton, CurrentIdx: 1})
	if qs.Len() != 1 || qs.CurrentIndex() != 0 {
		t.Errorf("单曲存活: Len/CurrentIndex = %d/%d, want 1/0", qs.Len(), qs.CurrentIndex())
	}
	if cur, ok := qs.Current(); !ok || cur.ID != "x" {
		t.Errorf("单曲存活 Current = %s/%v, want x", cur.ID, ok)
	}
}

// 当前曲恰是死条目（被过滤）或 CurrentIdx 非法 → 降级 -1，其余正常曲保留。
func TestRestoreDeadOrInvalidCurrentDegrades(t *testing.T) {
	// 当前曲是死条目 → 被过滤 → 降级 -1
	q := New()
	q.Restore(Snapshot{
		Tracks:     []model.Track{testTrack("a"), model.Track{ID: "d1"}},
		CurrentIdx: 1, // 指向死条目
	})
	if q.Len() != 1 {
		t.Errorf("Len = %d, want 1（正常曲保留）", q.Len())
	}
	if q.CurrentIndex() != -1 {
		t.Errorf("当前曲为死条目应降级 -1: %d", q.CurrentIndex())
	}
	// CurrentIdx 越界（快照损坏）→ 降级 -1
	q2 := New()
	q2.Restore(Snapshot{
		Tracks:     []model.Track{testTrack("a")},
		CurrentIdx: 5,
	})
	if q2.Len() != 1 || q2.CurrentIndex() != -1 {
		t.Errorf("越界 CurrentIdx 应降级 -1 且正常曲保留: Len/CurrentIndex = %d/%d", q2.Len(), q2.CurrentIndex())
	}
	if _, ok := q2.Current(); ok {
		t.Error("降级后不应有当前曲")
	}
}

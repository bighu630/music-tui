package queue

import (
	"reflect"
	"sort"
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

func TestNextSequentialOrderAndStopAtEnd(t *testing.T) {
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
	if _, ok := q.Next(); ok {
		t.Error("播完应停止（不循环）")
	}
	if q.CurrentIndex() != 2 {
		t.Errorf("播完后 CurrentIndex = %d, want 2（停在末位）", q.CurrentIndex())
	}
}

func TestNextWithNoCurrentStartsAtHead(t *testing.T) {
	q := New()
	q.Replace(testTrack("a"))
	q.Add(testTrack("b"))
	q.Add(testTrack("c"))
	q.Next() // b
	q.Next() // c 当前（2，末位）
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
	// 跳转到末位后无下一首
	if _, ok := q.Next(); ok {
		t.Error("跳转到末位后 Next 应返回 false")
	}
	// 跳回中间后从跳转处继续
	if !q.JumpTo(0) {
		t.Fatal("JumpTo(0) 应成功")
	}
	tr, ok := q.Next()
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
	if _, ok := q.Next(); ok {
		t.Error("随机模式播完也应停止（不循环）")
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

package ui

import (
	"errors"
	"testing"
	"time"

	"music-tui/model"
	"music-tui/queue"
)

// 队列三首曲：t1 当前，t2/t3 后续。
func mprisTestQueue(t *testing.T, m *Model) {
	t.Helper()
	m.queue.Replace(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Add(model.Track{ID: "t2", Title: "T2", Duration: 10})
	m.queue.Add(model.Track{ID: "t3", Title: "T3", Duration: 10})
}

// TestMprisReqNextPlaysNext 请求下一首：编排路径与 nextTrackMsg 一致。
func TestMprisReqNextPlaysNext(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	mprisTestQueue(t, &m)

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqNext, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqNext 应成功: %v", err)
	}
	if cur, ok := m2.queue.Current(); !ok || cur.ID != "t2" {
		t.Errorf("当前曲 = %v/%v, want t2", cur.ID, ok)
	}
	if len(m2.failedTracks) != 0 || m2.queueSkip {
		t.Error("reqNext 应重置重试预算与解耦标记（同 nextTrackMsg）")
	}
}

// TestMprisReqPrevPlaysPrev 请求上一首：编排路径与 prevTrackMsg 一致（含回绕）。
func TestMprisReqPrevPlaysPrev(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	mprisTestQueue(t, &m)
	m.queue.JumpTo(0) // 当前 t1，队首回绕到末尾 t3

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqPrev, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqPrev 应成功: %v", err)
	}
	if cur, ok := m2.queue.Current(); !ok || cur.ID != "t3" {
		t.Errorf("当前曲 = %v/%v, want t3（队首回绕）", cur.ID, ok)
	}
}

// TestMprisReqEmptyQueueErrEmpty 空队列 → queue.ErrEmpty（MPRIS 映射 NotSupported）。
func TestMprisReqEmptyQueueErrEmpty(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqNext, reply: reply}})

	if err := <-reply; !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列应回 queue.ErrEmpty, got %v", err)
	}
	_ = m2
}

// TestMprisReqSetModeSwitchesAndNotifies SetMode 切换模式并通知 sink。
func TestMprisReqSetModeSwitchesAndNotifies(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	reply := make(chan error, 1)
	m2, _ := update(m, mprisReqMsg{req: mprisReq{kind: reqSetMode, mode: queue.Shuffle, reply: reply}})

	if err := <-reply; err != nil {
		t.Fatalf("reqSetMode 应成功: %v", err)
	}
	if got := m2.queue.Mode(); got != queue.Shuffle {
		t.Errorf("模式 = %v, want Shuffle", got)
	}
	if len(notified) != 1 || notified[0] != queue.Shuffle {
		t.Errorf("sink 通知 = %v, want [Shuffle]", notified)
	}
	// SetLoop 同步：切入 Shuffle（非 RepeatOne）→ SetLoop(false)
	fp := m2.player.(*fakePlayer)
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.loops) == 0 || fp.loops[len(fp.loops)-1] != false {
		t.Errorf("SetLoop 调用 = %v, want 末尾 false", fp.loops)
	}
}

// TestMprisReqSetModeRepliesBeforeSync 死锁回归（对应 mpris 包
// TestLoopStatusSetNoDeadlock 的 ui 侧）：reqSetMode 必须先回包再执行
// applyMode。模拟真实接线：D-Bus 侧 prop.Set 持锁等待 reply，sink
//（SyncMode→SetMust）在 reply 被消费前无法完成——修复前 applyMode 先于
// 回包，sink 永等 reply → 循环等待死锁；修复后回包先行，sink 立即放行。
// 修复前本测试 3 秒超时失败（handleMprisReq 永不返回）。
func TestMprisReqSetModeRepliesBeforeSync(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	reply := make(chan error, 1)
	lockReleased := make(chan struct{}) // 模拟 D-Bus 侧消费 reply 后释放 prop 锁
	sinkDone := make(chan struct{})

	m.mprisCtrl.SetModeSink(func(mode queue.Mode) {
		<-lockReleased // 模拟 SetMust 等待 prop 锁（锁在 reply 消费后才释放）
		close(sinkDone)
	})

	// D-Bus 侧：消费 reply（dispatch 返回 → callback 返回 → prop.Set 释放 p.mut）
	go func() {
		<-reply
		close(lockReleased)
	}()

	done := make(chan struct{})
	go func() {
		update(m, mprisReqMsg{req: mprisReq{kind: reqSetMode, mode: queue.Shuffle, reply: reply}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("死锁：reqSetMode 在回包前执行了 applyMode（sink 等待 reply 消费）")
	}
	<-sinkDone
	if got := m.queue.Mode(); got != queue.Shuffle {
		t.Errorf("模式 = %v, want Shuffle", got)
	}
}

// TestMprisControllerPreCheck 控制器侧空队列预检查返回 ErrEmpty（不投递）。
func TestMprisControllerPreCheck(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	c := m.mprisCtrl
	if err := c.PlayNext(); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列 PlayNext 应返回 ErrEmpty, got %v", err)
	}
	if err := c.PlayPrevious(); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("空队列 PlayPrevious 应返回 ErrEmpty, got %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if c.Mode() != queue.Sequential {
		t.Errorf("Mode = %v, want Sequential（默认）", c.Mode())
	}
}

// TestMprisControllerDispatchRoundTrip 非空队列投递-执行-回包闭环。
func TestMprisControllerDispatchRoundTrip(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	m.queue.Add(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Replace(model.Track{ID: "t1", Title: "T1", Duration: 10})
	m.queue.Add(model.Track{ID: "t2", Title: "T2", Duration: 10})

	// 模拟 bubbletea 消费循环：goroutine 读 reqs 并 Update。
	// 注意：不把 update 结果写回共享 m（-race 会报测试自身数据竞争）；
	// queue 为共享指针，主 goroutine 直接断言其状态即可（dispatch 的 reply
	// 回包保证 happens-before）。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for req := range m.mprisCtrl.reqs {
			update(m, mprisReqMsg{req: req})
		}
	}()

	if err := m.mprisCtrl.PlayNext(); err != nil {
		t.Fatalf("PlayNext: %v", err)
	}
	if cur, ok := m.queue.Current(); !ok || cur.ID != "t2" {
		t.Errorf("当前曲 = %v/%v, want t2", cur.ID, ok)
	}
	close(m.mprisCtrl.reqs)
	<-done
}

// TestApplyModeNotifiesSink 绝对模式切换（MPRIS SetMode 路径）通知 sink 且
// SetLoop 与模式同步（RepeatOne → true，其余 → false）。
func TestApplyModeNotifiesSink(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	m2, _ := m.applyMode(queue.RepeatOne)
	if got := m2.queue.Mode(); got != queue.RepeatOne {
		t.Errorf("模式 = %v, want RepeatOne", got)
	}
	if len(notified) != 1 || notified[0] != queue.RepeatOne {
		t.Errorf("sink 通知 = %v, want [RepeatOne]", notified)
	}
	fp := m2.player.(*fakePlayer)
	fp.mu.Lock()
	if len(fp.loops) == 0 || fp.loops[len(fp.loops)-1] != true {
		t.Errorf("SetLoop 调用 = %v, want 末尾 true", fp.loops)
	}
	fp.mu.Unlock()

	// 同模式切换为 no-op：不重复通知（SetMode 同模式直接返回）
	notified = nil
	m2, _ = m2.applyMode(queue.RepeatOne)
	if len(notified) != 0 {
		t.Errorf("同模式切换不应通知, got %v", notified)
	}
}

// TestCycleModeStillNotifiesSink UI 三态循环切换也通知 sink（回归：toggleModeMsg 路径）。
func TestCycleModeStillNotifiesSink(t *testing.T) {
	m := newTestModel(t, newFakePlayer(), &fakeSearchAdapter{}, nil)
	var notified []queue.Mode
	m.mprisCtrl.SetModeSink(func(mode queue.Mode) { notified = append(notified, mode) })

	m2, _ := update(m, toggleModeMsg{}) // Sequential → Shuffle
	if got := m2.queue.Mode(); got != queue.Shuffle {
		t.Errorf("模式 = %v, want Shuffle", got)
	}
	if len(notified) != 1 || notified[0] != queue.Shuffle {
		t.Errorf("sink 通知 = %v, want [Shuffle]", notified)
	}
}

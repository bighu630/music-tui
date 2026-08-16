package ui

import (
	"errors"
	"testing"
	"time"

	"music-tui/player"
)

// waitThrottleFired 等待节流窗口触发（超时报错）。
func waitThrottleFired(t *testing.T, th *progressThrottle) {
	t.Helper()
	select {
	case <-th.Fired():
	case <-time.After(2 * time.Second):
		t.Fatal("节流窗口超时未触发")
	}
}

// 窗口内连续多个 ProgressEvent：全部进入窗口不立即放行，触发时只放行最后一个。
// 窗口 100ms、事件间隔 20ms：宽松余量，慢机器/CI 负载下也不越窗。
func TestProgressThrottleBurstEmitsOnlyLast(t *testing.T) {
	th := newProgressThrottle(100 * time.Millisecond)
	// 窗口内连续 3 个进度事件（间隔 20ms，远小于窗口）
	for _, pos := range []float64{1, 2, 3} {
		if th.Push(player.ProgressEvent{Position: pos, Duration: 200}) {
			t.Fatalf("窗口内 ProgressEvent 不应立即放行（应合并）")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitThrottleFired(t, th)
	pe, ok := th.Take()
	if !ok {
		t.Fatal("窗口触发后应可取到缓存的进度事件")
	}
	if pe.Position != 3 {
		t.Fatalf("窗口内只放行最后一个: Position=%v, want 3", pe.Position)
	}
	if pe.Duration != 200 {
		t.Fatalf("事件字段应原样保留: Duration=%v, want 200", pe.Duration)
	}
	if th.Fired() != nil {
		t.Error("Take 后窗口应复位（Fired 为 nil）")
	}
}

// 单个进度事件：窗口到点后放行，事件内容不变。
func TestProgressThrottleSingleEmitsAfterWindow(t *testing.T) {
	th := newProgressThrottle(20 * time.Millisecond)
	if th.Fired() != nil {
		t.Fatal("初始状态不应有窗口")
	}
	if th.Push(player.ProgressEvent{Position: 42, Duration: 200}) {
		t.Fatal("ProgressEvent 不应立即放行")
	}
	waitThrottleFired(t, th)
	pe, ok := th.Take()
	if !ok || pe.Position != 42 {
		t.Fatalf("Take = (%+v, %v), want Position=42", pe, ok)
	}
}

// 非进度事件（StateEvent 等）：立即放行，不被节流吞掉。
func TestProgressThrottleNonProgressPassesThrough(t *testing.T) {
	th := newProgressThrottle(20 * time.Millisecond)
	if !th.Push(player.StateEvent{Playing: false}) {
		t.Fatal("非进度事件应立即放行")
	}
	if !th.Push(player.TrackStartedEvent{Duration: 200}) {
		t.Fatal("TrackStartedEvent 应立即放行")
	}
	if !th.Push(player.TrackEndedEvent{}) {
		t.Fatal("TrackEndedEvent 应立即放行")
	}
	if !th.Push(player.ErrorEvent{Err: errors.New("test")}) {
		t.Fatal("ErrorEvent 应立即放行")
	}
}

// 非进度事件到达时丢弃窗口内缓存进度（事件本身放行）。
func TestProgressThrottleNonProgressDropsPending(t *testing.T) {
	th := newProgressThrottle(20 * time.Millisecond)
	if th.Push(player.ProgressEvent{Position: 1, Duration: 200}) {
		t.Fatal("ProgressEvent 不应立即放行")
	}
	if !th.Push(player.StateEvent{Playing: false}) {
		t.Fatal("非进度事件应立即放行")
	}
	if th.Fired() != nil {
		t.Error("非进度事件应丢弃缓存的进度窗口")
	}
	if _, ok := th.Take(); ok {
		t.Error("非进度事件后不应残留缓存进度")
	}
}

// 窗口边界：Take 复位后新一轮事件重新开窗口。
func TestProgressThrottleNewWindowAfterTake(t *testing.T) {
	th := newProgressThrottle(20 * time.Millisecond)
	if th.Push(player.ProgressEvent{Position: 1, Duration: 200}) {
		t.Fatal("第一窗口 ProgressEvent 不应立即放行")
	}
	waitThrottleFired(t, th)
	if _, ok := th.Take(); !ok {
		t.Fatal("第一窗口 Take 应有值")
	}
	// 新一轮事件：重新开窗口计时
	if th.Push(player.ProgressEvent{Position: 2, Duration: 200}) {
		t.Fatal("新窗口 ProgressEvent 不应立即放行")
	}
	waitThrottleFired(t, th)
	pe, ok := th.Take()
	if !ok || pe.Position != 2 {
		t.Fatalf("新窗口 Take = (%+v, %v), want Position=2", pe, ok)
	}
}

// 无缓存进度时 Take 返回 ok=false（防御，不 panic）。
func TestProgressThrottleIdleTakeEmpty(t *testing.T) {
	th := newProgressThrottle(20 * time.Millisecond)
	if _, ok := th.Take(); ok {
		t.Fatal("无缓存进度时 Take 应返回 ok=false")
	}
}

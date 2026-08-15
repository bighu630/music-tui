package ui

// 预加载（preload）UI 集成测试：refreshPreload 门控语义与各调用点联动。
// Target() 由 SetTarget 同步更新目标槽位（互斥锁内），断言无需等待异步下载；
// 预下载本身走 newTestModel 的"不存在的 yt-dlp"→ 后台静默失败，不干扰断言。

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/cache"
	"music-tui/player"
	"music-tui/queue"
)

// init 把 cache 后台下载的重试预算压到最小：默认 5 次 × 2s 退避 ≈ 8s 的重试
// 突发会让失败下载的 goroutine 贯穿整个测试套件（每触发一次预载/预热下载就
// 多一条长命 goroutine）。改为单次尝试：exec 立即失败 → goroutine 毫秒级
// 退出（attempts=1 时退避不会被读），并顺带加速全包 ui 测试（含 root 预热
// 类测试的后台下载）。
//
// 为何用 init 一次性设置而非“每测试 set + defer 恢复”：download 的重试循环
// 在无同步的情况下读取这两个包级变量（cache.go 的 for 条件与 time.After），
// 测试内修改后恢复必然与调度延迟的下载 goroutine 读操作竞争（实测 -race
// 报 DATA RACE：cleanup 恢复写 vs 下载 goroutine 读循环条件）。init 在任何
// 测试/下载 goroutine 存在之前写入，此后不再写——从构造上消除竞争。cache
// 包自身测试在独立二进制内各自 set/恢复，互不影响。
func init() {
	cache.MaxDownloadAttempts = 1                 // 单次尝试即失败退出：无退避循环
	cache.DownloadRetryBackoff = time.Millisecond // 防御性调短（attempts=1 时不读）
}

// targetID 返回当前预加载目标曲目 ID（无目标时返回空串）。测试断言用。
func targetID(m Model) string {
	if t := m.preloader.Target(); t != nil {
		return t.ID
	}
	return ""
}

// preloadTestModel 创建预加载测试模型：真实 cache.Manager + 不存在的
// yt-dlp（预下载后台静默失败）；退出时停止调度器 worker（防跨测试泄漏）。
func preloadTestModel(t *testing.T, fp *fakePlayer) Model {
	t.Helper()
	m := newTestModel(t, fp, &fakeSearchAdapter{}, nil)
	t.Cleanup(m.preloader.Stop)
	return m
}

// startPlayingT1T2 播放 t1 并追加 t2、触发 TrackStarted：预加载目标应为 t2。
// 多个测试共用的前置状态（"有当前曲目、未结束、非单曲循环"）。
func startPlayingT1T2(t *testing.T, fp *fakePlayer) Model {
	t.Helper()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if got := targetID(m); got != "t2" {
		t.Fatalf("前置: TrackStarted 后预加载目标 = %q, want t2", got)
	}
	return m
}

// TrackStarted（当前曲确认开始）与 trackAppendMsg（新增下一首候选）联动：
// 有当前曲目、未结束、非单曲循环 → 目标 = 队列下一首。
func TestPreloadTrackStartedTargetsNext(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	// 追加即联动：startPlay 已置当前曲 t1（虽未 TrackStarted），目标 = t2
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	if got := targetID(m); got != "t2" {
		t.Fatalf("trackAppendMsg 后预加载目标 = %q, want t2", got)
	}
	// TrackStarted（mpv 取流成功）→ 目标保持下一首 t2
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Fatalf("TrackStarted 后 state.Track = %+v, want t1", m.state.Track)
	}
	if got := targetID(m); got != "t2" {
		t.Fatalf("TrackStarted 后预加载目标 = %q, want t2", got)
	}
}

// startPlay 单曲：PeekNext 回绕返回自身——预载当前曲无意义（TrackStarted
// 预热已覆盖同 ID 曲目），且 TrackStarted 之前预载会与 mpv 取流并发访问
// 同一 URL 放大 403 风控（与预热移后同一回归动机），故目标保持空。
func TestPreloadStartPlaySingleClearsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	if got := targetID(m); got != "" {
		t.Fatalf("startPlay 单曲后预加载目标 = %q, want 空（回绕自身跳过）", got)
	}
	// TrackStarted 后同样保持空（回绕自身，预热已覆盖）
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if got := targetID(m); got != "" {
		t.Fatalf("TrackStarted 后预加载目标 = %q, want 空", got)
	}
}

// 仅追加、从未播放：state.Track == nil → 门控不满足，不设目标。
func TestPreloadNoCurrentTrackClearsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, _ = update(m, trackAppendMsg{track: testTrack("t1")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	if got := targetID(m); got != "" {
		t.Fatalf("无当前曲目时预加载目标 = %q, want 空", got)
	}
}

// 模式切换（toggleModeMsg/queueModeMsg 共用 cycleMode）：切入 RepeatOne →
// 目标清空（mpv 无缝循环同曲，预载下一首浪费）；切回 Sequential → 恢复。
func TestPreloadRepeatOneSkipsAndRestores(t *testing.T) {
	fp := newFakePlayer()
	m := startPlayingT1T2(t, fp) // Sequential：目标 t2
	// Sequential→Shuffle→RepeatOne
	m, _ = update(m, toggleModeMsg{})
	m, _ = update(m, toggleModeMsg{})
	if m.queue.Mode() != queue.RepeatOne {
		t.Fatalf("前置: 模式 = %v, want RepeatOne", m.queue.Mode())
	}
	if got := targetID(m); got != "" {
		t.Fatalf("RepeatOne 下预加载目标 = %q, want 空（同曲循环无需预载）", got)
	}
	// RepeatOne→Sequential
	m, _ = update(m, toggleModeMsg{})
	if got := targetID(m); got != "t2" {
		t.Fatalf("切回 Sequential 后预加载目标 = %q, want t2", got)
	}
}

// 队列清空（queueClearMsg）：无下一首可预载 → 目标清空。
func TestPreloadQueueClearClearsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := startPlayingT1T2(t, fp)
	m, _ = update(m, queueClearMsg{})
	if got := targetID(m); got != "" {
		t.Fatalf("队列清空后预加载目标 = %q, want 空", got)
	}
}

// 删除下一首（queueDeleteMsg）：目标顺延为新的下一首。
func TestPreloadQueueDeleteRefreshesTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")}) // 队列 [t1,t2,t3]
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if got := targetID(m); got != "t2" {
		t.Fatalf("前置: 预加载目标 = %q, want t2", got)
	}
	// 删除下一首 t2（下标 1）→ 目标顺延为新的下一首 t3
	m, _ = update(m, queueDeleteMsg{index: 1})
	if got := targetID(m); got != "t3" {
		t.Fatalf("删除 t2 后预加载目标 = %q, want t3", got)
	}
}

// 队列页跳转（queuePlayMsg）：当前曲变更 → 目标 = 跳转曲之后的一首
//（末位跳转回绕队首）。
func TestPreloadJumpToTargetsFollowing(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, _ = update(m, trackAppendMsg{track: testTrack("t1")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")}) // 队列 [t1,t2,t3] 未播放
	m, _ = update(m, queuePlayMsg{index: 2})
	if m.state.Track == nil || m.state.Track.ID != "t3" {
		t.Fatalf("跳转后 state.Track = %+v, want t3", m.state.Track)
	}
	if got := targetID(m); got != "t1" {
		t.Fatalf("跳转 t3 后预加载目标 = %q, want t1（末尾回绕队首）", got)
	}
}

// 上一首/下一首（prevTrackMsg/nextTrackMsg）：切歌后目标随当前曲顺延。
func TestPreloadNextPrevRefreshTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	m, cmd := m.startPlay(testTrack("t1"))
	_ = execCmds(cmd)
	m, _ = update(m, trackAppendMsg{track: testTrack("t2")})
	m, _ = update(m, trackAppendMsg{track: testTrack("t3")})
	m, _ = update(m, playerEventMsg{ev: player.TrackStartedEvent{Duration: 200}})
	if got := targetID(m); got != "t2" {
		t.Fatalf("前置: 预加载目标 = %q, want t2", got)
	}
	// 下一首 → t2 播放中：目标顺延为 t3
	m, _ = update(m, nextTrackMsg{})
	if m.state.Track == nil || m.state.Track.ID != "t2" {
		t.Fatalf("nextTrackMsg 后 state.Track = %+v, want t2", m.state.Track)
	}
	if got := targetID(m); got != "t3" {
		t.Fatalf("nextTrackMsg 后预加载目标 = %q, want t3", got)
	}
	// 上一首 → 回到 t1：目标恢复为 t2
	m, _ = update(m, prevTrackMsg{})
	if m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Fatalf("prevTrackMsg 后 state.Track = %+v, want t1", m.state.Track)
	}
	if got := targetID(m); got != "t2" {
		t.Fatalf("prevTrackMsg 后预加载目标 = %q, want t2", got)
	}
}

// 播放列表加载（plLoadMsg）：整列表替换 → 目标 = 当前曲之后的下一首。
func TestPreloadPlLoadRefreshesTarget(t *testing.T) {
	fp := newFakePlayer()
	m := preloadTestModel(t, fp)
	if _, err := m.pl.Create("列表"); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("列表", testTrack("t1")); err != nil {
		t.Fatal(err)
	}
	if err := m.pl.AddTrack("列表", testTrack("t2")); err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, plLoadMsg{name: "列表", index: 0})
	if m.state.Track == nil || m.state.Track.ID != "t1" {
		t.Fatalf("plLoadMsg 后 state.Track = %+v, want t1", m.state.Track)
	}
	if got := targetID(m); got != "t2" {
		t.Fatalf("plLoadMsg 后预加载目标 = %q, want t2", got)
	}
}

// 非取流类错误（ErrorEvent 通用分支）：进入 ended 停止态 → 门控清空目标。
func TestPreloadEndedClearsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := startPlayingT1T2(t, fp)
	m, _ = update(m, playerEventMsg{ev: player.ErrorEvent{Err: errors.New("连接断开")}})
	if !m.ended {
		t.Fatal("非取流错误后 ended 应为 true")
	}
	if got := targetID(m); got != "" {
		t.Fatalf("ended 后预加载目标 = %q, want 空", got)
	}
}

// 取流失败重试耗尽：队列有下一首 → 跳过继续连播（目标顺延为跳过曲之后）；
// 回绕撞回已失败曲目 → 无曲可跳 → 停止（ended）→ 目标清空。
func TestPreloadRetryExhaustedStopClearsTarget(t *testing.T) {
	old := retryBackoff
	retryBackoff = 10 * time.Millisecond
	defer func() { retryBackoff = old }()

	fp := newFakePlayer()
	m := startPlayingT1T2(t, fp) // 队列 [t1,t2]，目标 t2

	loadErr := player.ErrorEvent{Err: &player.LoadFailedError{FileError: "no audio or video data played"}}
	// t1 两次自动重试均失败 → 重试耗尽：跳过 t2 继续连播
	var cmd tea.Cmd
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	m, cmd = update(m, playerEventMsg{ev: loadErr})
	_ = cmd
	if fp.playCount() != 4 {
		t.Fatalf("跳过 t2 后 playCount = %d, want 4", fp.playCount())
	}
	if m.state.Track == nil || m.state.Track.ID != "t2" {
		t.Fatalf("跳过后续播应为 t2, got %+v", m.state.Track)
	}
	// 跳过 = 播放状态变更：目标立即顺延为 t2 之后的队首 t1（不必等 TrackStarted）
	if got := targetID(m); got != "t1" {
		t.Fatalf("跳过 t2 后续播，预加载目标 = %q, want t1（末尾回绕队首）", got)
	}

	// t2 同样重试耗尽：回绕撞回已失败 t1（failedTracks）→ 无曲可跳 → 停止
	for i := 0; i < 2; i++ {
		m, cmd = update(m, playerEventMsg{ev: loadErr})
		m = execRetryBatch(m, cmd, fp)
	}
	m, cmd = update(m, playerEventMsg{ev: loadErr})
	_ = cmd
	if !m.ended {
		t.Fatal("重试耗尽且无曲可跳后 ended 应为 true")
	}
	if got := targetID(m); got != "" {
		t.Fatalf("ended 停止后预加载目标 = %q, want 空", got)
	}
}

// stopAfterEnd（播放结束且无下一首）：ended 门控清空目标。
func TestPreloadStopAfterEndClearsTarget(t *testing.T) {
	fp := newFakePlayer()
	m := startPlayingT1T2(t, fp)
	m, cmd := m.stopAfterEnd()
	_ = cmd
	if !m.ended {
		t.Fatal("stopAfterEnd 后 ended 应为 true")
	}
	if got := targetID(m); got != "" {
		t.Fatalf("播放停止后预加载目标 = %q, want 空", got)
	}
}

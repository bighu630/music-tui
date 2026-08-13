//go:build linux

package mpris

import (
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"music-tui/model"
	"music-tui/player"
)

// ---- fake 依赖 ----

// fakePlayer 实现 playerLike：记录调用并支持事件注入。
type fakePlayer struct {
	mu     sync.Mutex
	calls  []string
	volume float64
	volErr error
	events chan player.Event
}

func newFakePlayer() *fakePlayer {
	return &fakePlayer{volume: 80, events: make(chan player.Event, 64)}
}

func (f *fakePlayer) Subscribe() (<-chan player.Event, func()) { return f.events, func() {} }

func (f *fakePlayer) record(args ...interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := ""
	for i, a := range args {
		if i > 0 {
			s += ","
		}
		s += toString(a)
	}
	f.calls = append(f.calls, s)
	return nil
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%g", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func (f *fakePlayer) Play(url string) error           { return f.record("Play", url) }
func (f *fakePlayer) Pause() error                    { return f.record("Pause") }
func (f *fakePlayer) Resume() error                   { return f.record("Resume") }
func (f *fakePlayer) Seek(seconds float64) error      { return f.record("Seek", seconds) }
func (f *fakePlayer) SetVolume(percent float64) error { return f.record("SetVolume", percent) }
func (f *fakePlayer) Volume() (float64, error)        { return f.volume, f.volErr }

func (f *fakePlayer) push(ev player.Event) { f.events <- ev }

func (f *fakePlayer) hasCall(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// fakeProps 实现 propsStore。
type fakeProps struct {
	mu sync.Mutex
	m  map[string]map[string]interface{}
}

func newFakeProps() *fakeProps {
	return &fakeProps{m: map[string]map[string]interface{}{
		ifacePlayer: {"PlaybackStatus": "Stopped", "Position": int64(0), "Metadata": map[string]dbus.Variant{}},
	}}
}

func (p *fakeProps) SetMust(iface, name string, v interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m[iface] == nil {
		p.m[iface] = map[string]interface{}{}
	}
	p.m[iface][name] = v
}

func (p *fakeProps) GetMust(iface, name string) interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[iface][name]
}

// fakeBus 实现 bus 接口，记录 Emit。
type fakeBus struct {
	mu    sync.Mutex
	emits []struct {
		path   dbus.ObjectPath
		name   string
		values []interface{}
	}
}

func (b *fakeBus) Emit(path dbus.ObjectPath, name string, values ...interface{}) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emits = append(b.emits, struct {
		path   dbus.ObjectPath
		name   string
		values []interface{}
	}{path, name, values})
	return nil
}

func (b *fakeBus) Close() error { return nil }

// newTestServer 组装不连总线的 Server（单测用）。
func newTestServer() *Server {
	return &Server{p: newFakePlayer(), conn: &fakeBus{}, props: newFakeProps()}
}

// ---- 纯函数测试 ----

func TestMetadataFor(t *testing.T) {
	tr := &model.Track{ID: "abc123", Title: "标题", Artist: "歌手", Duration: 217.5, CoverURL: "http://x/y.jpg"}
	m := metadataFor(tr)
	if m["mpris:trackid"] != dbus.MakeVariant(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/abc123")) {
		t.Errorf("trackid 错误: %v", m["mpris:trackid"])
	}
	if m["mpris:length"] != dbus.MakeVariant(int64(217500000)) {
		t.Errorf("length 错误: %v", m["mpris:length"])
	}
	if m["xesam:title"] != dbus.MakeVariant("标题") {
		t.Errorf("title 错误: %v", m["xesam:title"])
	}
	// []string 不可比较，用 DeepEqual 对比 Variant。
	if !reflect.DeepEqual(m["xesam:artist"], dbus.MakeVariant([]string{"歌手"})) {
		t.Errorf("artist 错误: %v", m["xesam:artist"])
	}
	if m["mpris:artUrl"] != dbus.MakeVariant("http://x/y.jpg") {
		t.Errorf("artUrl 错误: %v", m["mpris:artUrl"])
	}
}

func TestMetadataForNoCover(t *testing.T) {
	m := metadataFor(&model.Track{ID: "x", Title: "t", Artist: "a", Duration: 10})
	if _, ok := m["mpris:artUrl"]; ok {
		t.Error("无封面时不应有 artUrl")
	}
}

func TestMetadataForEmptyArtist(t *testing.T) {
	m := metadataFor(&model.Track{ID: "x", Title: "t", Duration: 10})
	if !reflect.DeepEqual(m["xesam:artist"], dbus.MakeVariant([]string{})) {
		t.Errorf("空歌手应给空数组: %v", m["xesam:artist"])
	}
}

func TestMetadataForNil(t *testing.T) {
	m := metadataFor(nil)
	if len(m) != 0 {
		t.Errorf("nil 曲目应返回空字典: %v", m)
	}
}

func TestShouldEmitSeeked(t *testing.T) {
	cases := []struct {
		last, cur float64
		want      bool
	}{
		{0, 0.05, false},  // 正常播放推进
		{10, 12.1, true},  // 正向跳变 > 2s
		{10, 7.9, true},   // 负向跳变 < -2s
		{10, 12.0, false}, // 恰好 2s：不触发（严格大于）
		{0, 0, false},
	}
	for _, c := range cases {
		if got := shouldEmitSeeked(c.last, c.cur); got != c.want {
			t.Errorf("shouldEmitSeeked(%v,%v)=%v, want %v", c.last, c.cur, got, c.want)
		}
	}
}

func TestPlaybackStatus(t *testing.T) {
	if playbackStatus(true) != "Playing" || playbackStatus(false) != "Paused" {
		t.Error("playbackStatus 映射错误")
	}
}

func TestClamp01(t *testing.T) {
	if clamp01(-0.1) != 0 || clamp01(1.5) != 1 || clamp01(0.5) != 0.5 {
		t.Error("clamp01 错误")
	}
}

// ---- handleEvent 测试 ----

func TestHandleEventProgress(t *testing.T) {
	s := newTestServer()
	fp := s.p.(*fakePlayer)
	fb := s.conn.(*fakeBus)
	fpr := s.props.(*fakeProps)

	fp.push(player.ProgressEvent{Position: 1.0, Duration: 217})
	s.handleEvent(<-fp.events)
	if got := fpr.GetMust(ifacePlayer, "Position"); got != int64(1000000) {
		t.Errorf("Position = %v, want 1000000", got)
	}
	if len(fb.emits) != 0 {
		t.Errorf("正常推进不应发 Seeked: %v", fb.emits)
	}

	fp.push(player.ProgressEvent{Position: 6.0, Duration: 217}) // 跳变 5s
	s.handleEvent(<-fp.events)
	if got := fpr.GetMust(ifacePlayer, "Position"); got != int64(6000000) {
		t.Errorf("Position = %v, want 6000000", got)
	}
	if len(fb.emits) != 1 || fb.emits[0].name != ifacePlayer+".Seeked" ||
		fb.emits[0].values[0] != int64(6000000) {
		t.Errorf("Seeked 信号异常: %#v", fb.emits)
	}
}

func TestHandleEventStateAndLifecycle(t *testing.T) {
	s := newTestServer()
	fp := s.p.(*fakePlayer)
	fpr := s.props.(*fakeProps)

	fp.push(player.StateEvent{Playing: true})
	s.handleEvent(<-fp.events)
	if got := fpr.GetMust(ifacePlayer, "PlaybackStatus"); got != "Playing" {
		t.Errorf("status = %v, want Playing", got)
	}
	fp.push(player.StateEvent{Playing: false})
	s.handleEvent(<-fp.events)
	if got := fpr.GetMust(ifacePlayer, "PlaybackStatus"); got != "Paused" {
		t.Errorf("status = %v, want Paused", got)
	}

	// TrackStarted：metadata 来自缓存，Position 归零
	s.SetTrack(&model.Track{ID: "vid1", Title: "T", Artist: "A", Duration: 100, CoverURL: "http://c"})
	fp.push(player.TrackStartedEvent{Duration: 100})
	s.handleEvent(<-fp.events)
	md := fpr.GetMust(ifacePlayer, "Metadata").(map[string]dbus.Variant)
	if md["xesam:title"] != dbus.MakeVariant("T") {
		t.Errorf("Metadata 未整包替换: %v", md)
	}
	if got := fpr.GetMust(ifacePlayer, "PlaybackStatus"); got != "Playing" {
		t.Errorf("TrackStarted 后 status = %v, want Playing", got)
	}
	if got := fpr.GetMust(ifacePlayer, "Position"); got != int64(0) {
		t.Errorf("TrackStarted 后 Position = %v, want 0", got)
	}

	// 新曲目首帧进度不触发 Seeked（基准已重置）
	fb := s.conn.(*fakeBus)
	fp.push(player.ProgressEvent{Position: 0.3, Duration: 100})
	s.handleEvent(<-fp.events)
	if len(fb.emits) != 0 {
		t.Errorf("新曲目首帧不应发 Seeked: %v", fb.emits)
	}

	// TrackEnded：状态 Stopped、Metadata 保留
	fp.push(player.TrackEndedEvent{})
	s.handleEvent(<-fp.events)
	if got := fpr.GetMust(ifacePlayer, "PlaybackStatus"); got != "Stopped" {
		t.Errorf("TrackEnded 后 status = %v, want Stopped", got)
	}
	if got := fpr.GetMust(ifacePlayer, "Metadata").(map[string]dbus.Variant)["xesam:title"]; got != dbus.MakeVariant("T") {
		t.Errorf("TrackEnded 后 Metadata 应保留: %v", got)
	}
}

// ---- 方法测试 ----

func TestMethodsForwardToPlayer(t *testing.T) {
	s := newTestServer()
	fp := s.p.(*fakePlayer)

	if err := s.Play(); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}
	// PlayPause：当前 Paused → Resume
	if err := s.PlayPause(); err != nil {
		t.Fatal(err)
	}
	// PlayPause：当前 Playing → Pause
	s.props.(*fakeProps).SetMust(ifacePlayer, "PlaybackStatus", "Playing")
	if err := s.PlayPause(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Seek(5e6); err != nil { // +5s
		t.Fatal(err)
	}
	if !fp.hasCall("Resume") || !fp.hasCall("Pause") {
		t.Error("Resume/Pause 未转调")
	}
	// Stop = Pause + Seek(0)
	if !fp.hasCall("Seek,0") {
		t.Error("Stop 未 Seek(0)")
	}
	// Seek 目标 = 当前 Position(0) + 5s
	if !fp.hasCall("Seek,5") {
		t.Error("Seek 换算错误，需要 Seek,5")
	}
}

func TestSetPosition(t *testing.T) {
	s := newTestServer()
	fp := s.p.(*fakePlayer)

	// 无曲目时任何 trackId 都拒绝
	err := s.SetPosition(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/vid1"), 3e6)
	if err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Fatalf("无曲目应返回 InvalidArgs: %v", err)
	}

	s.SetTrack(&model.Track{ID: "vid1", Title: "T"})
	// 匹配 trackId → Seek(3)
	if err := s.SetPosition(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/vid1"), 3e6); err != nil {
		t.Fatalf("合法 SetPosition 失败: %v", err)
	}
	if !fp.hasCall("Seek,3") {
		t.Fatal("SetPosition 未转调 Seek(3)")
	}
	// 不匹配 trackId → InvalidArgs
	if err := s.SetPosition(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/other"), 3e6); err == nil ||
		err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Fatalf("不匹配 trackId 应返回 InvalidArgs: %v", err)
	}
}

func TestUnsupportedMethods(t *testing.T) {
	s := newTestServer()
	bad := []*dbus.Error{s.Next(), s.Previous(), s.OpenUri("http://x")}
	for _, err := range bad {
		if err == nil || err.Name != "org.freedesktop.DBus.Error.NotSupported" {
			t.Errorf("应返回 NotSupported: %v", err)
		}
	}
	// Raise/Quit：CanRaise/CanQuit=false，no-op 返回 nil
	if err := s.Raise(); err != nil {
		t.Errorf("Raise 应为 no-op nil: %v", err)
	}
	if err := s.Quit(); err != nil {
		t.Errorf("Quit 应为 no-op nil: %v", err)
	}
}

func TestVolumeCallback(t *testing.T) {
	s := newTestServer()
	fp := s.p.(*fakePlayer)
	fpr := s.props.(*fakeProps)

	// 合法：0.5 → SetVolume(50)，返回 nil
	if err := s.volumeCallback(&prop.Change{Value: 0.5}); err != nil {
		t.Fatalf("合法音量被拒: %v", err)
	}
	if !fp.hasCall("SetVolume,50") {
		t.Fatal("音量未转调 SetVolume(50)")
	}
	// 越界 → InvalidArgs 且不转调
	fp.mu.Lock()
	fp.calls = nil
	fp.mu.Unlock()
	if err := s.volumeCallback(&prop.Change{Value: 1.5}); err == nil {
		t.Fatal("越界音量应报错")
	}
	if fp.hasCall("SetVolume") {
		t.Fatal("越界音量不应转调 mpv")
	}
	// 类型错误
	if err := s.volumeCallback(&prop.Change{Value: "x"}); err == nil {
		t.Fatal("非数值音量应报错")
	}
	// 注意：props 未变化（回调失败时 prop 包不会写入）
	if got := fpr.GetMust(ifacePlayer, "Volume"); got != nil && got != 1.0 {
		t.Logf("Volume 属性当前值: %v（本测试不校验）", got)
	}
}

// ---- 集成测试（真实 session bus，无总线时 Skip）----

// TestIntegrationMPRIS 在真实 session bus 上验证服务发现、属性读写、
// 方法调用与 Seeked 信号。CI 无总线时自动跳过。
func TestIntegrationMPRIS(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("无 session bus（DBUS_SESSION_BUS_ADDRESS 未设置），跳过集成测试")
	}

	fp := newFakePlayer()
	s := NewServer(fp)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	client, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatalf("连接客户端: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	obj := client.Object(serviceName, objectPath)

	// 服务名已注册
	if err := obj.Call("org.freedesktop.DBus.Peer.Ping", 0).Store(); err != nil {
		// Peer.Ping 可能未实现（godbus 自动处理），改用 NameHasOwner
		var has bool
		if err := client.Object("org.freedesktop.DBus", "/org/freedesktop/DBus").
			Call("org.freedesktop.DBus.NameHasOwner", 0, serviceName).Store(&has); err != nil || !has {
			t.Fatalf("服务名 %s 未被持有: %v", serviceName, err)
		}
	}

	// GetAll：Player 接口属性
	var all map[string]dbus.Variant
	if err := obj.Call("org.freedesktop.DBus.Properties.GetAll", 0, ifacePlayer).Store(&all); err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if all["PlaybackStatus"].Value() != "Stopped" {
		t.Errorf("初始 PlaybackStatus = %v, want Stopped", all["PlaybackStatus"])
	}
	if all["Rate"].Value() != 1.0 {
		t.Errorf("Rate = %v, want 1.0", all["Rate"])
	}
	if all["CanControl"].Value() != true {
		t.Errorf("CanControl = %v, want true", all["CanControl"])
	}
	if all["CanGoNext"].Value() != false {
		t.Errorf("CanGoNext = %v, want false", all["CanGoNext"])
	}
	// 根接口属性（另一个 GetAll）
	var rootAll map[string]dbus.Variant
	if err := obj.Call("org.freedesktop.DBus.Properties.GetAll", 0, ifaceRoot).Store(&rootAll); err != nil {
		t.Fatalf("GetAll(root): %v", err)
	}
	if rootAll["CanQuit"].Value() != false {
		t.Errorf("CanQuit = %v, want false", rootAll["CanQuit"])
	}
	if rootAll["Identity"].Value() != "music-tui" {
		t.Errorf("Identity = %v, want music-tui", rootAll["Identity"])
	}
	if rootAll["SupportedUriSchemes"].Value() == nil {
		t.Errorf("SupportedUriSchemes 缺失")
	}

	// 方法调用：Pause → fake 记录
	if err := obj.Call("org.mpris.MediaPlayer2.Player.Pause", 0).Err; err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !fp.hasCall("Pause") {
		t.Fatal("Pause 未转调播放器")
	}

	// 不支持方法 → NotSupported
	if err := obj.Call("org.mpris.MediaPlayer2.Player.Next", 0).Err; err == nil {
		t.Fatal("Next 应返回错误")
	}

	// 音量写入（0-1）：Set → SetVolume(50)
	if err := obj.Call("org.freedesktop.DBus.Properties.Set", 0,
		ifacePlayer, "Volume", dbus.MakeVariant(0.5)).Err; err != nil {
		t.Fatalf("Set Volume: %v", err)
	}
	if !fp.hasCall("SetVolume,50") {
		t.Fatal("Volume 写入未转调 SetVolume(50)")
	}
	// 越界拒绝
	if err := obj.Call("org.freedesktop.DBus.Properties.Set", 0,
		ifacePlayer, "Volume", dbus.MakeVariant(2.0)).Err; err == nil {
		t.Fatal("越界音量应被拒绝")
	}

	// 事件 → 属性 + Seeked 信号
	sigCh := make(chan *dbus.Signal, 16)
	client.Signal(sigCh)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(ifacePlayer), dbus.WithMatchMember("Seeked")); err != nil {
		t.Fatalf("AddMatchSignal: %v", err)
	}
	s.SetTrack(&model.Track{ID: "vid1", Title: "T", Artist: "A", Duration: 100})
	fp.push(player.TrackStartedEvent{Duration: 100})
	fp.push(player.ProgressEvent{Position: 1.0, Duration: 100})
	fp.push(player.ProgressEvent{Position: 6.0, Duration: 100}) // 跳变 5s → Seeked

	waitFor(t, 3*time.Second, func() bool {
		var pos int64
		if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0,
			ifacePlayer, "Position").Store(&pos); err != nil {
			return false
		}
		return pos == 6e6
	})
	select {
	case sig := <-sigCh:
		if sig.Name != ifacePlayer+".Seeked" {
			t.Fatalf("信号名 = %v", sig.Name)
		}
		if len(sig.Body) != 1 || sig.Body[0] != int64(6e6) {
			t.Fatalf("Seeked 参数 = %v, want [6000000]", sig.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 Seeked 信号")
	}

	// Metadata 可见
	var md map[string]dbus.Variant
	if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0,
		ifacePlayer, "Metadata").Store(&md); err != nil {
		t.Fatalf("Get Metadata: %v", err)
	}
	if md["xesam:title"].Value() != "T" {
		t.Errorf("Metadata title = %v, want T", md["xesam:title"])
	}

	// SetPosition：先错误 trackId，再正确 trackId
	if err := obj.Call("org.mpris.MediaPlayer2.Player.SetPosition", 0,
		dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/wrong"), int64(3e6)).Err; err == nil {
		t.Fatal("错误 trackId 应被拒绝")
	}
	if err := obj.Call("org.mpris.MediaPlayer2.Player.SetPosition", 0,
		dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/vid1"), int64(3e6)).Err; err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	if !fp.hasCall("Seek,3") {
		t.Fatal("SetPosition 未转调 Seek(3)")
	}
}

// waitFor 轮询直到 cond 为真或超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestIntegrationServiceNameConflict 第二个实例抢名失败（降级路径）。
func TestIntegrationServiceNameConflict(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("无 session bus，跳过集成测试")
	}
	s1 := NewServer(newFakePlayer())
	if err := s1.Start(); err != nil {
		t.Fatalf("第一个实例 Start: %v", err)
	}
	defer s1.Close()
	s2 := NewServer(newFakePlayer())
	if err := s2.Start(); err == nil {
		t.Fatal("第二个实例应抢名失败")
	}
}

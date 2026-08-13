package player

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMpvServer 模拟 mpv 的 JSON IPC：接收命令并回复 success，
// 测试可主动推送事件行。协议：请求/响应与事件均为换行分隔的 JSON。
type fakeMpvServer struct {
	t            *testing.T
	ln           net.Listener
	mu           sync.Mutex
	conn         net.Conn
	commands     [][]interface{} // 收到的所有命令（已解码）
	pushCh       chan string     // 待推送的事件行
	duration     float64         // get_property("duration") 的返回值（可配置）
	volume       float64         // get_property("volume") 的返回值（可配置）
	respondToGet bool            // false 时对 get_property 不响应（模拟 mpv 挂死）
	silent       bool            // true 时对所有命令不响应（模拟 mpv 挂死；需用 setSilent 设置避免竞态）
}

func newFakeMpvServer(t *testing.T) *fakeMpvServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mpv.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeMpvServer{t: t, ln: ln, pushCh: make(chan string, 32), duration: 217.0, volume: 100.0, respondToGet: true}
	go f.acceptLoop()
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func (f *fakeMpvServer) Path() string { return f.ln.Addr().String() }

func (f *fakeMpvServer) acceptLoop() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	go f.readLoop(conn)
	go f.writeLoop(conn)
}

func (f *fakeMpvServer) readLoop(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var req struct {
			Command []interface{} `json:"command"`
			ID      int64         `json:"request_id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		f.mu.Lock()
		f.commands = append(f.commands, req.Command)
		silent := f.silent
		f.mu.Unlock()
		if silent {
			continue // 模拟 mpv 挂死：不回复任何命令，客户端 Call 永久阻塞
		}

		status := "success"
		var data interface{}
		if len(req.Command) > 0 && req.Command[0] == "get_property" {
			f.mu.Lock()
			respond := f.respondToGet
			dur := f.duration
			vol := f.volume
			f.mu.Unlock()
			if !respond {
				continue // 模拟 mpv 挂死：不回复，客户端 Call 永久阻塞
			}
			if prop, ok := req.Command[1].(string); ok {
				switch prop {
				case "duration":
					data = dur
				case "volume":
					data = vol
				default:
					status = "property unavailable"
				}
			} else {
				status = "property unavailable"
			}
		}
		resp, err := json.Marshal(struct {
			Error string      `json:"error"`
			Data  interface{} `json:"data"`
			ID    int64       `json:"request_id"`
		}{Error: status, Data: data, ID: req.ID})
		if err != nil {
			continue
		}
		if _, err := conn.Write(append(resp, '\n')); err != nil {
			return
		}
	}
}

func (f *fakeMpvServer) writeLoop(conn net.Conn) {
	for line := range f.pushCh {
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			return
		}
	}
}

// pushEvent 向客户端推送一条原始 mpv 事件（单行 JSON）。
func (f *fakeMpvServer) pushEvent(jsonLine string) {
	f.pushCh <- jsonLine
}

func (f *fakeMpvServer) recordedCommands() [][]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]interface{}(nil), f.commands...)
}

// setSilent 切换命令静默模式（连接建立后调用，线程安全）。
func (f *fakeMpvServer) setSilent(silent bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.silent = silent
}

// Close 关闭连接与监听，模拟 mpv 退出/崩溃。
func (f *fakeMpvServer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
	return f.ln.Close()
}

// connectTestPlayer 直接连接 fake server（跳过进程启动），并注册清理。
//
// mpvipc 的 hub 只向已注册的 EventListener 广播事件（无排队缓冲），而
// pump() 里 NewEventListener 的注册是异步完成的：connect() 返回后立刻
// pushEvent 可能被静默丢弃，导致 waitEvent 偶发超时。因此连接后先探测
// 监听者就绪，保证返回后测试事件可靠送达。
func connectTestPlayer(t *testing.T, fake *fakeMpvServer) *MpvPlayer {
	t.Helper()
	p := NewMpvPlayer("", fake.Path())
	if err := p.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	probeListenerReady(t, p, fake)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// probeListenerReady 等待 mpvipc 监听者注册完成。探测事件是 time-pos=0.0
// 的 property-change（转换为 ProgressEvent{Position:0, Duration:0}），与
// 测试事件同管道 FIFO 送达、先于测试事件被消费，不会干扰后续断言。
func probeListenerReady(t *testing.T, p *MpvPlayer, fake *fakeMpvServer) {
	t.Helper()
	const probe = `{"event":"property-change","id":1,"name":"time-pos","data":0.0}`
	deadline := time.After(2 * time.Second)
	for {
		fake.pushEvent(probe)
		select {
		case <-p.Events():
			// 监听者已就绪：丢弃探测事件，并排空可能仍在途的探测事件
			// （重试场景下可能累积多条），确保返回时事件通道为空。
			drainDeadline := time.Now().Add(200 * time.Millisecond)
			for {
				select {
				case <-p.Events():
					if time.Now().After(drainDeadline) {
						return
					}
				case <-time.After(20 * time.Millisecond):
					return // 20ms 无新事件 = 管道已排空
				}
			}
		case <-time.After(200 * time.Millisecond):
			// 未收到：监听者可能尚未注册（探测事件被丢弃），重试。
		case <-deadline:
			t.Fatalf("监听者未就绪：2s 内未收到探测事件（事件监听链路异常）")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitEvent 在超时时间内等待下一个事件。
func waitEvent(t *testing.T, p *MpvPlayer, timeout time.Duration) Event {
	t.Helper()
	select {
	case ev := <-p.Events():
		return ev
	case <-time.After(timeout):
		t.Fatalf("等待事件超时（%s）", timeout)
		return nil
	}
}

func TestMpvPlayerObservesProperties(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	// connect() 成功返回即意味着 3 条 observe_property 均已收到响应
	cmds := fake.recordedCommands()
	if len(cmds) != 3 {
		t.Fatalf("commands = %d, want 3", len(cmds))
	}
	want := [][]interface{}{
		{"observe_property", float64(1), "time-pos"},
		{"observe_property", float64(2), "duration"},
		{"observe_property", float64(3), "pause"},
	}
	for i := range want {
		for j := range want[i] {
			if cmds[i][j] != want[i][j] {
				t.Fatalf("cmd[%d] = %v, want %v", i, cmds[i], want[i])
			}
		}
	}
	_ = p
}

func TestMpvPlayerProgressAndStateEvents(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	fake.pushEvent(`{"event":"property-change","id":1,"name":"time-pos","data":10.5}`)

	ev := waitEvent(t, p, 2*time.Second)
	progress, ok := ev.(ProgressEvent)
	if !ok {
		t.Fatalf("事件类型 = %T, want ProgressEvent", ev)
	}
	if progress.Position != 10.5 || progress.Duration != 217.0 {
		t.Errorf("progress = %+v, want {10.5 217.0}", progress)
	}

	fake.pushEvent(`{"event":"property-change","id":3,"name":"pause","data":true}`)
	ev = waitEvent(t, p, 2*time.Second)
	state, ok := ev.(StateEvent)
	if !ok || state.Playing {
		t.Errorf("want StateEvent{Playing:false}, got %#v", ev)
	}

	fake.pushEvent(`{"event":"property-change","id":3,"name":"pause","data":false}`)
	ev = waitEvent(t, p, 2*time.Second)
	state, ok = ev.(StateEvent)
	if !ok || !state.Playing {
		t.Errorf("want StateEvent{Playing:true}, got %#v", ev)
	}
}

func TestMpvPlayerDropsInvalidPosition(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	fake.pushEvent(`{"event":"property-change","id":1,"name":"time-pos","data":999.0}`)

	select {
	case ev := <-p.Events():
		t.Fatalf("Position > Duration 的异常进度应被过滤，收到 %#v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMpvPlayerTrackLifecycleEvents(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	fake.pushEvent(`{"event":"file-loaded"}`)
	ev := waitEvent(t, p, 2*time.Second)
	started, ok := ev.(TrackStartedEvent)
	if !ok || started.Duration != 217.0 {
		t.Errorf("want TrackStartedEvent{Duration:217}, got %#v", ev)
	}

	// 播放结束信号：end-file reason=eof（keep-open=no 下 eof-reached 属性
	// EOF 瞬间即变 unavailable，data=null 不可用，见 TestMpvPlayerEofReachedDoesNotEmitTrackEnded）
	fake.pushEvent(`{"event":"end-file","reason":"eof"}`)
	ev = waitEvent(t, p, 2*time.Second)
	if _, ok := ev.(TrackEndedEvent); !ok {
		t.Errorf("want TrackEndedEvent, got %#v", ev)
	}
}

// duration property-change 晚于 file-loaded 到达时，pump 应通过
// get_property("duration") 兜底，保证 TrackStartedEvent.Duration 非零。
func TestMpvPlayerTrackStartedDurationFallsBackToGet(t *testing.T) {
	fake := newFakeMpvServer(t)
	fake.duration = 333.0 // 配置兜底值，验证 get_property 分支可配置
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"file-loaded"}`)
	ev := waitEvent(t, p, 2*time.Second)
	started, ok := ev.(TrackStartedEvent)
	if !ok {
		t.Fatalf("want TrackStartedEvent, got %#v", ev)
	}
	if started.Duration != 333.0 {
		t.Errorf("want Duration=333（get_property 兜底）, got %v", started.Duration)
	}

	// 确认兜底确实发出了 get_property duration 请求
	cmds := fake.recordedCommands()
	found := false
	for _, c := range cmds {
		if len(c) == 2 && c[0] == "get_property" && c[1] == "duration" {
			found = true
		}
	}
	if !found {
		t.Errorf("应发出 get_property duration 兜底请求, commands = %v", cmds)
	}

	// duration property-change 随后正常到达：不产生新事件，仅更新缓存
	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	fake.pushEvent(`{"event":"property-change","id":1,"name":"time-pos","data":5.0}`)
	ev = waitEvent(t, p, 2*time.Second)
	progress, ok := ev.(ProgressEvent)
	if !ok {
		t.Fatalf("want ProgressEvent, got %#v", ev)
	}
	if progress.Position != 5.0 || progress.Duration != 217.0 {
		t.Errorf("progress = %+v, want {5 217}", progress)
	}
}

// 语义守护：eof-reached=true 不再产生 TrackEndedEvent（结束信号
// 统一以 end-file reason=eof 为准，避免 keep-open=yes 下与 end-file 重复）。
func TestMpvPlayerEofReachedDoesNotEmitTrackEnded(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"property-change","id":4,"name":"eof-reached","data":true}`)
	select {
	case ev := <-p.Events():
		t.Fatalf("eof-reached=true 不应产生 TrackEndedEvent, got %#v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// pump 的 file-loaded Get 兜底有 500ms 超时：mpv 对 get_property 不响应
// （崩溃/卡死）时 pump 不应被永久阻塞，超时后照常处理后续事件。
func TestMpvPlayerFileLoadedGetTimeoutKeepsPumpAlive(t *testing.T) {
	fake := newFakeMpvServer(t)
	fake.respondToGet = false // 模拟 mpv 挂死：不响应 get_property
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"file-loaded"}`)
	ev := waitEvent(t, p, 2*time.Second) // 覆盖 500ms 超时 + 余量
	started, ok := ev.(TrackStartedEvent)
	if !ok {
		t.Fatalf("超时后仍应发出 TrackStartedEvent, got %#v", ev)
	}
	if started.Duration != 0 {
		t.Errorf("兜底超时后 Duration 应为 0, got %v", started.Duration)
	}

	// pump 必须仍存活：后续事件应照常处理（超时后不再有 get_property
	// 请求，respondToGet 保持 false 即可，勿再写它以免与 readLoop 竞态）
	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	fake.pushEvent(`{"event":"property-change","id":1,"name":"time-pos","data":5.0}`)
	ev = waitEvent(t, p, 2*time.Second)
	progress, ok := ev.(ProgressEvent)
	if !ok {
		t.Fatalf("want ProgressEvent, got %#v", ev)
	}
	if progress.Position != 5.0 || progress.Duration != 217.0 {
		t.Errorf("progress = %+v, want {5 217}", progress)
	}
}

func TestMpvPlayerErrorOnEndFileError(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"end-file","reason":"error"}`)
	ev := waitEvent(t, p, 2*time.Second)
	if _, ok := ev.(ErrorEvent); !ok {
		t.Errorf("want ErrorEvent, got %#v", ev)
	}
}

func TestMpvPlayerErrorOnDisconnect(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.Close()
	ev := waitEvent(t, p, 3*time.Second)
	if _, ok := ev.(ErrorEvent); !ok {
		t.Fatalf("want ErrorEvent（mpv 断开）, got %#v", ev)
	}
}

func TestMpvPlayerCommands(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://www.youtube.com/watch?v=abc"); err != nil {
		t.Fatal(err)
	}
	if err := p.Pause(); err != nil {
		t.Fatal(err)
	}
	if err := p.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.Seek(30); err != nil {
		t.Fatal(err)
	}

	cmds := fake.recordedCommands()
	got := cmds[3:] // 前 3 条是 observe_property
	want := [][]interface{}{
		{"loadfile", "https://www.youtube.com/watch?v=abc"},
		{"set_property", "pause", true},
		{"set_property", "pause", false},
		{"seek", 30.0, "absolute"},
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("cmd[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// mpv 挂死（对命令不响应）时 Play/Pause/Resume/Seek 应在 ~500ms 内
// 超时返回错误，而不是永久阻塞（否则同步调用会冻结整个 TUI）。
func TestMpvPlayerCommandsTimeoutWhenMpvHangs(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)
	fake.setSilent(true) // 之后不回复任何命令，模拟 mpv 挂死

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Play", func() error { return p.Play("https://example.com/a.mp3") }},
		{"Pause", func() error { return p.Pause() }},
		{"Resume", func() error { return p.Resume() }},
		{"Seek", func() error { return p.Seek(30) }},
	} {
		start := time.Now()
		err := tc.call()
		if err == nil {
			t.Errorf("%s: mpv 不响应时应超时报错", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "超时") {
			t.Errorf("%s: err = %v, want 超时消息", tc.name, err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("%s: 超时未及时生效: %v（超过 500ms 超时 + 余量）", tc.name, elapsed)
		}
	}
}

// 未连接（未 Start/connect）时所有命令都应报错而非 panic。
func TestMpvPlayerCommandsFailWhenNotConnected(t *testing.T) {
	p := NewMpvPlayer("", "")

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Play", func() error { return p.Play("https://example.com/a.mp3") }},
		{"Pause", func() error { return p.Pause() }},
		{"Resume", func() error { return p.Resume() }},
		{"Seek", func() error { return p.Seek(10) }},
	} {
		if err := tc.call(); err == nil {
			t.Errorf("%s: 未连接时应报错", tc.name)
		}
	}
}

// Start 失败（进程提前退出）后不残留脏状态：再次 Start 应重新走启动流程，
// 而不是报"播放器已在运行"；错误信息不应含 %!w 格式化残留。
func TestMpvPlayerStartFailureLeavesNoDirtyState(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-mpv.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewMpvPlayer(script, filepath.Join(dir, "mpv.sock"))

	if err := p.Start(); err == nil {
		t.Fatal("进程立即退出（不建 socket）时 Start 应报错")
	} else if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("错误信息不应含格式化残留: %v", err)
	}
	if p.cmd != nil || p.waitCh != nil || p.conn != nil {
		t.Fatalf("Start 失败后残留脏状态: cmd=%v waitCh=%v conn=%v",
			p.cmd != nil, p.waitCh != nil, p.conn != nil)
	}
	// 再次 Start 不应报"播放器已在运行"，应重新执行启动流程并再次失败
	if err := p.Start(); err == nil {
		t.Fatal("再次 Start 应仍失败（脚本仍立即退出）")
	}
}

// Close 后 pump 停止分发：再推事件不应收到任何 Event。
func TestMpvPlayerNoEventsAfterClose(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	// 前置：确认事件链路正常
	fake.pushEvent(`{"event":"end-file","reason":"eof"}`)
	if _, ok := waitEvent(t, p, 2*time.Second).(TrackEndedEvent); !ok {
		t.Fatalf("前置事件异常：want TrackEndedEvent")
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"eof"}`)
	fake.pushEvent(`{"event":"property-change","id":2,"name":"duration","data":217.0}`)
	select {
	case ev := <-p.Events():
		t.Fatalf("Close 后不应再收到事件, got %#v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestMpvPlayerCloseIsIdempotent(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil: %v", err)
	}
}

// ---- Subscribe 广播 ----

func TestMpvPlayerSubscribeReceivesBroadcast(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)
	defer p.Close()
	events, unsub := p.Subscribe()
	defer unsub()

	fake.pushEvent(`{"event":"property-change","id":1,"data":42.0}`)

	ev := waitEvent(t, p, 2*time.Second) // Events() 主通道不受影响
	pe, ok := ev.(ProgressEvent)
	if !ok || pe.Position != 42.0 {
		t.Fatalf("主通道事件异常: %#v", ev)
	}
	select {
	case ev := <-events:
		pe, ok := ev.(ProgressEvent)
		if !ok || pe.Position != 42.0 {
			t.Fatalf("订阅通道事件异常: %#v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("订阅通道未收到广播事件")
	}
}

func TestMpvPlayerSubscribeUnsubscribeStopsDelivery(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)
	defer p.Close()
	events, unsub := p.Subscribe()

	fake.pushEvent(`{"event":"property-change","id":1,"data":1.0}`)
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("退订前应能收到事件")
	}
	unsub()
	fake.pushEvent(`{"event":"property-change","id":1,"data":2.0}`)
	select {
	case ev := <-events:
		t.Fatalf("退订后仍收到事件: %#v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// ---- 音量 ----

func TestMpvPlayerSetVolume(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)
	defer p.Close()

	if err := p.SetVolume(55); err != nil {
		t.Fatalf("SetVolume: %v", err)
	}
	cmds := fake.recordedCommands()
	last := cmds[len(cmds)-1]
	if len(last) != 3 || last[0] != "set_property" || last[1] != "volume" || last[2] != 55.0 {
		t.Fatalf("set_property 命令异常: %#v", last)
	}

	// 越界钳制
	_ = p.SetVolume(-5)
	last = fake.recordedCommands()[len(fake.recordedCommands())-1]
	if last[2] != 0.0 {
		t.Fatalf("负值未钳制到 0: %#v", last)
	}
	_ = p.SetVolume(150)
	last = fake.recordedCommands()[len(fake.recordedCommands())-1]
	if last[2] != 100.0 {
		t.Fatalf("超界未钳制到 100: %#v", last)
	}
}

func TestMpvPlayerVolume(t *testing.T) {
	fake := newFakeMpvServer(t)
	fake.volume = 60 // 新增字段：get_property("volume") 的返回值
	p := connectTestPlayer(t, fake)
	defer p.Close()

	v, err := p.Volume()
	if err != nil {
		t.Fatalf("Volume: %v", err)
	}
	if v != 60 {
		t.Fatalf("Volume = %v, want 60", v)
	}
}

func TestMpvPlayerVolumeFailsWhenNotConnected(t *testing.T) {
	p := NewMpvPlayer("mpv", filepath.Join(t.TempDir(), "x.sock"))
	if _, err := p.Volume(); err == nil {
		t.Fatal("未连接时 Volume 应报错")
	}
	if err := p.SetVolume(50); err == nil {
		t.Fatal("未连接时 SetVolume 应报错")
	}
}

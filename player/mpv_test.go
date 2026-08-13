package player

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeMpvServer 模拟 mpv 的 JSON IPC：接收命令并回复 success，
// 测试可主动推送事件行。协议：请求/响应与事件均为换行分隔的 JSON。
type fakeMpvServer struct {
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	conn     net.Conn
	commands [][]interface{} // 收到的所有命令（已解码）
	pushCh   chan string     // 待推送的事件行
}

func newFakeMpvServer(t *testing.T) *fakeMpvServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mpv.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeMpvServer{t: t, ln: ln, pushCh: make(chan string, 32)}
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
		f.mu.Unlock()

		status := "success"
		var data interface{}
		if len(req.Command) > 0 && req.Command[0] == "get_property" {
			if prop, ok := req.Command[1].(string); ok && prop == "duration" {
				data = 217.0
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

	// connect() 成功返回即意味着 4 条 observe_property 均已收到响应
	cmds := fake.recordedCommands()
	if len(cmds) != 4 {
		t.Fatalf("commands = %d, want 4", len(cmds))
	}
	want := [][]interface{}{
		{"observe_property", float64(1), "time-pos"},
		{"observe_property", float64(2), "duration"},
		{"observe_property", float64(3), "pause"},
		{"observe_property", float64(4), "eof-reached"},
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

	fake.pushEvent(`{"event":"property-change","id":4,"name":"eof-reached","data":true}`)
	ev = waitEvent(t, p, 2*time.Second)
	if _, ok := ev.(TrackEndedEvent); !ok {
		t.Errorf("want TrackEndedEvent, got %#v", ev)
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
	got := cmds[4:] // 前 4 条是 observe_property
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

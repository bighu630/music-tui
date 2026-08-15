package player

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	loadID       int64           // loadfile 响应计数：playlist_entry_id（递增，持 f.mu）
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
		if len(req.Command) > 0 && req.Command[0] == "loadfile" {
			// 模拟 mpv ≥0.33：loadfile 命令响应携带 playlist_entry_id（递增计数），
			// 供 end-file error 陈旧过滤测试按 id 归属事件。
			f.mu.Lock()
			f.loadID++
			data = map[string]interface{}{"playlist_entry_id": float64(f.loadID)}
			f.mu.Unlock()
		}
		if err := writeFakeMpvResponse(conn, req.ID, status, data); err != nil {
			return
		}
	}
}

// writeFakeMpvResponse 按 mpv JSON IPC 协议回复一条命令结果
// （fakeMpvServer 与 fake mpv 进程共用，保持回复协议一致）。
func writeFakeMpvResponse(conn net.Conn, reqID int64, status string, data interface{}) error {
	resp, err := json.Marshal(struct {
		Error string      `json:"error"`
		Data  interface{} `json:"data"`
		ID    int64       `json:"request_id"`
	}{Error: status, Data: data, ID: reqID})
	if err != nil {
		return err
	}
	_, err = conn.Write(append(resp, '\n'))
	return err
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

// setLoadWatchdogTimeout 调小加载看门狗时限（t.Cleanup 恢复原值）。
// 包级变量测试隔离：同包测试顺序执行（无 t.Parallel），串行调整安全。
func setLoadWatchdogTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := loadWatchdogTimeout
	loadWatchdogTimeout = d
	t.Cleanup(func() { loadWatchdogTimeout = orig })
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

// end-file reason=error 时，ErrorEvent 应携带 mpv IPC file_error 字段的诊断
// 文本（如 "no audio or video data played"），供 UI 层给出可操作的中文提示。
func TestMpvPlayerLoadFailedErrorCarriesFileError(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"end-file","reason":"error","file_error":"no audio or video data played"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.FileError != "no audio or video data played" {
		t.Errorf("FileError = %q, want %q", le.FileError, "no audio or video data played")
	}
	if !strings.Contains(ev.Err.Error(), "mpv 播放出错（end-file reason=error）") {
		t.Errorf("错误消息应保留原前缀: %v", ev.Err)
	}
}

// 陈旧 end-file error 过滤（回归：恢复播放误删健康缓存）：end-file error 事件
// 携带 playlist_entry_id 且与最近一次成功 loadfile 的 id 不一致时，事件属于
// 更早的 loadfile（旧曲取流失败晚到）——必须丢弃，否则 UI 按“当前曲目”归属
// 会把健康缓存误删。两次 Play（id 1、2）后推旧 id 1 的错误事件：不得产生
// ErrorEvent，且事件通道照常存活（后续事件正常送达）。
func TestMpvPlayerDropsStaleEndFileError(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	// 两次成功 loadfile：playlist_entry_id 1、2；最近身份 = 2
	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	if err := p.Play("https://example.com/b.mp3"); err != nil {
		t.Fatal(err)
	}

	// 旧曲（id=1）的取流失败事件晚到：应被过滤丢弃
	fake.pushEvent(`{"event":"end-file","reason":"error","playlist_entry_id":1,"file_error":"no audio or video data played"}`)
	// 通道存活探测：后续事件应照常送达（陈旧错误已被丢弃，首个事件非 ErrorEvent）
	fake.pushEvent(`{"event":"property-change","id":1,"name":"time-pos","data":5.0}`)
	ev := waitEvent(t, p, 2*time.Second)
	if _, ok := ev.(ProgressEvent); !ok {
		t.Fatalf("陈旧 end-file error 应被过滤，首个事件 = %#v, want ProgressEvent", ev)
	}
	// 短窗口内不得再出现 ErrorEvent
	select {
	case ev := <-p.Events():
		t.Fatalf("陈旧 end-file error 不应产生 ErrorEvent, got %#v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// end-file error 携带的 playlist_entry_id 与最近一次 loadfile 一致 → 属于当前
// 曲目：照常产生 LoadFailedError（过滤不得吞掉真实失败）。
func TestMpvPlayerEndFileErrorWithCurrentLoadID(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"error","playlist_entry_id":1,"file_error":"no audio or video data played"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.FileError != "no audio or video data played" {
		t.Errorf("FileError = %q, want %q", le.FileError, "no audio or video data played")
	}
}

// 旧版 mpv（end-file 事件无 playlist_entry_id）或 lastLoadID 未知：过滤禁用，
// 事件保守放行（回归守护：不得因过滤逻辑吞掉真实失败）。
func TestMpvPlayerEndFileErrorWithoutPlaylistEntryIDStillEmits(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"error","file_error":"no audio or video data played"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.FileError != "no audio or video data played" {
		t.Errorf("FileError = %q, want %q", le.FileError, "no audio or video data played")
	}
}

// lastLoadID 未知（0：从未成功 loadfile，或重连后身份重置）时，即便事件携带
// playlist_entry_id 也不得过滤——归属无法确认，保守放行（回归守护：未知归属
// 误过滤会吞掉真实失败，例如恢复路径的事件先于任何 loadfile 响应到达）。
func TestMpvPlayerEndFileErrorWithUnknownLoadIDStillEmits(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	// 不调用 Play/PlayPaused：从未成功 loadfile，lastLoadID 保持 0（未知）。
	fake.pushEvent(`{"event":"end-file","reason":"error","playlist_entry_id":1,"file_error":"no audio or video data played"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.FileError != "no audio or video data played" {
		t.Errorf("FileError = %q, want %q", le.FileError, "no audio or video data played")
	}
}

// ---- 加载看门狗（回归：连播未缓存下一首卡住） ----

// 核心场景：loadfile 后 mpv 取流悬挂（yt-dlp 卡死/403 重试退避/网络黑洞），
// 不产生 file-loaded/end-file 任何事件 → 看门狗应在限时后主动 emit
// LoadTimeoutError（UI 据此走现有重试/跳过链路，不再无限期静默卡住）。
func TestLoadWatchdogTimesOutEmitsError(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	// 不推任何事件：加载悬挂，看门狗应主动报错
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadTimeoutError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadTimeoutError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.Timeout != 300*time.Millisecond {
		t.Errorf("Timeout = %v, want 300ms", le.Timeout)
	}
	if !strings.Contains(ev.Err.Error(), "加载超时") {
		t.Errorf("错误文案 = %q, want 含加载超时", ev.Err.Error())
	}
}

// file-loaded 认领加载：此后超过原时限数倍也不得出现 LoadTimeoutError。
func TestLoadWatchdogClearedOnFileLoaded(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"file-loaded"}`)
	if _, ok := waitEvent(t, p, 2*time.Second).(TrackStartedEvent); !ok {
		t.Fatalf("want TrackStartedEvent")
	}
	select {
	case ev := <-p.Events():
		t.Fatalf("file-loaded 后不应有超时错误, got %#v", ev)
	case <-time.After(2 * time.Second):
	}
}

// end-file reason=eof 认领加载（播完/失败均为加载有结论）。
func TestLoadWatchdogClearedOnEndFileEOF(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"eof"}`)
	if _, ok := waitEvent(t, p, 2*time.Second).(TrackEndedEvent); !ok {
		t.Fatalf("want TrackEndedEvent")
	}
	select {
	case ev := <-p.Events():
		t.Fatalf("end-file eof 后不应有超时错误, got %#v", ev)
	case <-time.After(2 * time.Second):
	}
}

// end-file reason=error（当前曲目）认领加载：发出 LoadFailedError 且不超时。
func TestLoadWatchdogClearedOnEndFileError(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"error","playlist_entry_id":1,"file_error":"x"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	select {
	case ev := <-p.Events():
		t.Fatalf("end-file error 后不应有超时错误, got %#v", ev)
	case <-time.After(2 * time.Second):
	}
}

// 陈旧 end-file error（旧曲 id=1 晚到）被过滤丢弃，不得解除新加载的看门狗：
// 新曲（id=2）仍在取流悬挂 → 超时错误必须照常触发。
func TestLoadWatchdogStaleEndFileErrorKeepsArmed(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	if err := p.Play("https://example.com/b.mp3"); err != nil {
		t.Fatal(err)
	}
	fake.pushEvent(`{"event":"end-file","reason":"error","playlist_entry_id":1,"file_error":"x"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadTimeoutError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("陈旧 end-file error 不应解除新加载的看门狗：want *LoadTimeoutError, got %T (%v)", ev.Err, ev.Err)
	}
}

// 新 loadfile 重置 deadline：第一次加载的 deadline 不得在第二次加载后误报，
// 且超时只触发一次（认领后清 deadline 防重复）。
func TestLoadWatchdogResetOnNewLoad(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(loadWatchdogTimeout / 2) // 未超时即切歌
	if err := p.Play("https://example.com/b.mp3"); err != nil {
		t.Fatal(err)
	}
	// 只应收到一次超时（第二次加载的）；第一次的 deadline 已被重置不误报
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadTimeoutError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadTimeoutError, got %T (%v)", ev.Err, ev.Err)
	}
	// 认领后不得重复触发
	select {
	case ev := <-p.Events():
		t.Fatalf("超时认领后不应重复触发, got %#v", ev)
	case <-time.After(2 * loadWatchdogTimeout):
	}
}

// 回归（审查 P1）：断开清理必须清除加载看门狗状态——若 onDisconnect 只清
// lastLoadID 而残留 loadDeadline，重连后新 watchdogLoop 继承旧 deadline
//（最多还有 30s）到期误报 LoadTimeoutError → UI 无用户操作自动重播断线前
// 曲目。断线时刻在途加载必然已死：deadline 归零 + loadResolved=true（重连后
// 无活跃加载）。
func TestOnDisconnectClearsLoadDeadline(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	p.stateMu.Lock()
	if p.loadDeadline.IsZero() || p.loadResolved {
		p.stateMu.Unlock()
		t.Fatal("前置：Play 后看门狗应武装（deadline 非零且 loadResolved=false）")
	}
	p.stateMu.Unlock()

	// 设 reconnecting=true：onDisconnect 只清理死状态、不启动 autoReconnect
	//（避免重连副作用干扰断言），与真实"重连进行中连接又死"路径一致。
	p.stateMu.Lock()
	p.reconnecting = true
	p.stateMu.Unlock()

	p.onDisconnect(p.currentConn())

	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if !p.loadDeadline.IsZero() {
		t.Errorf("onDisconnect 后 loadDeadline = %v, want 零值（重连后不得继承旧 deadline）", p.loadDeadline)
	}
	if !p.loadResolved {
		t.Error("onDisconnect 后 loadResolved 应为 true（断线时刻在途加载已死，无未决结论）")
	}
}

// 回归（审查 P1）：superseded 检查必须先于 expired 认领。旧实现先 claim
//（清 deadline）再查代际——重连后旧代际 loop 最后一片 tick 会先认领（吞掉
// 新代际的 deadline）再退出；emit 在 superseded 检查之后执行，不会真的误报
// 超时，真正的破坏是 deadline 被清空。本测试只推进代际（模拟重连已建立新连
// 接），不启动新 loop：若旧 loop 先认领，deadline 被清空；修复后旧 loop 直
// 接退出，deadline 保留给（尚不存在的）新代际 loop。
func TestWatchdogSupersededBeforeClaim(t *testing.T) {
	setLoadWatchdogTimeout(t, 300*time.Millisecond)
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatal(err)
	}
	p.stateMu.Lock()
	if p.loadDeadline.IsZero() {
		p.stateMu.Unlock()
		t.Fatal("前置：Play 后 loadDeadline 应为非零")
	}
	p.stateMu.Unlock()

	// 模拟重连：connect() 自增 watchdogSeq 启动新代际，旧 loop 的 seq 已过期。
	// 旧 loop 首个 tick 在 500ms（ticker 周期）后：deadline 已于 300ms 过期，
	// 该 tick 同时命中 superseded 与 expired——修复前会先认领（清掉新代际
	// deadline）再退出，不会误报。
	p.stateMu.Lock()
	p.watchdogSeq++
	p.stateMu.Unlock()

	// 等待远超一个 tick 周期：保证旧 loop 已处理（至少一片）tick 并退出。
	// 2×timeout(600ms) + 600ms 覆盖 ticker 500ms 周期与调度抖动。
	time.Sleep(2*loadWatchdogTimeout + 600*time.Millisecond)

	// 旧代际不得误报超时
	select {
	case ev := <-p.Events():
		t.Fatalf("旧代际看门狗不得误报超时, got %#v", ev)
	default:
	}
	// 旧代际不得认领：deadline 保留给新代际（仍非零——新 loop 才负责认领）
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.loadDeadline.IsZero() {
		t.Error("旧代际 loop 不得认领/清空 deadline（应保留给新代际）")
	}
}

// 旧版 mpv 可能缺失 file_error 字段：此时 FileError 为空串，
// 错误消息与原有版本完全一致（向后兼容）。
func TestMpvPlayerLoadFailedErrorEmptyFileError(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	fake.pushEvent(`{"event":"end-file","reason":"error"}`)
	ev := waitErrorEvent(t, p, 2*time.Second)
	var le *LoadFailedError
	if !errors.As(ev.Err, &le) {
		t.Fatalf("want *LoadFailedError, got %T (%v)", ev.Err, ev.Err)
	}
	if le.FileError != "" {
		t.Errorf("FileError = %q, want 空串", le.FileError)
	}
	if got := ev.Err.Error(); got != "mpv 播放出错（end-file reason=error）" {
		t.Errorf("错误消息 = %q, want 原消息", got)
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

// SetLoop 设置单曲循环：set_property loop-file（mpv 无缝循环）。
// loop-file 是 per-file 属性，新 loadfile 自动重置为 no。
func TestMpvPlayerSetLoop(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	if err := p.SetLoop(true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLoop(false); err != nil {
		t.Fatal(err)
	}

	cmds := fake.recordedCommands()
	got := cmds[3:] // 前 3 条是 observe_property
	want := [][]interface{}{
		{"set_property", "loop-file", true},
		{"set_property", "loop-file", false},
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("cmd[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// PlayPaused 加载指定 URL 但保持暂停（续播恢复用，不发声）。
// 命令序列：set pause=true → loadfile（mpv 的 pause 属性不随文件重置）。
// start>0 时用 4 参 loadfile（mpv ≥0.38）随加载原子定位，避免加载窗口内
// 单独 seek 被 mpv 拒绝（error running command）的竞态。
func TestMpvPlayerPlayPaused(t *testing.T) {
	fake := newFakeMpvServer(t)
	p := connectTestPlayer(t, fake)

	// start>0：4 参 loadfile（replace -1 + start= 选项），定位随加载原子完成
	if err := p.PlayPaused("https://www.youtube.com/watch?v=abc", 66.6); err != nil {
		t.Fatal(err)
	}
	cmds := fake.recordedCommands()
	got := cmds[3:] // 前 3 条是 observe_property
	want := [][]interface{}{
		{"set_property", "pause", true},
		{"loadfile", "https://www.youtube.com/watch?v=abc", "replace", float64(-1), "start=66.6"},
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("cmd[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	}

	// start=0：2 参 loadfile（兼容 mpv <0.38，从头加载）
	if err := p.PlayPaused("https://www.youtube.com/watch?v=def", 0); err != nil {
		t.Fatal(err)
	}
	cmds = fake.recordedCommands()
	got = cmds[5:] // 前 5 条是 observe_property + 上一条 PlayPaused 的 2 条命令
	want = [][]interface{}{
		{"set_property", "pause", true},
		{"loadfile", "https://www.youtube.com/watch?v=def"},
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
		{"PlayPaused", func() error { return p.PlayPaused("https://example.com/a.mp3", 30) }},
		{"Pause", func() error { return p.Pause() }},
		{"Resume", func() error { return p.Resume() }},
		{"Seek", func() error { return p.Seek(30) }},
		{"SetLoop", func() error { return p.SetLoop(true) }},
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
		{"PlayPaused", func() error { return p.PlayPaused("https://example.com/a.mp3", 30) }},
		{"Pause", func() error { return p.Pause() }},
		{"Resume", func() error { return p.Resume() }},
		{"Seek", func() error { return p.Seek(10) }},
		{"SetLoop", func() error { return p.SetLoop(true) }},
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

// mpv 启动参数必须含 --ytdl-format=bestaudio：纯音频播放器只需音频流，
// 避免同时开视频+音频两个流（YouTube 403 风控暴露面翻倍，见设计文档）。
// 且必须含 --ytdl-raw-options=socket-timeout=15,retries=2：yt-dlp 取流收紧——
// 403/网络黑洞快速失败（默认 retries=10 指数退避可拖 1-2 分钟，取流悬挂
// 卡住的来源之一；回归：连播未缓存下一首卡住）。
// 包装脚本把收到的全部参数记入 MUSIC_TUI_FAKE_MPV_LOG，Start 返回时
// socket 已就绪（args 行先于 socket 出现），直接读日志断言。
func TestMpvStartProcessYtdlFormatArg(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	logPath := filepath.Join(dir, "starts.log")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", logPath)
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取日志: %v", err)
	}
	if !strings.Contains(string(data), "--ytdl-format=bestaudio") {
		t.Errorf("mpv 启动参数应含 --ytdl-format=bestaudio:\n%s", data)
	}
	if !strings.Contains(string(data), "--ytdl-raw-options=socket-timeout=15,retries=2") {
		t.Errorf("mpv 启动参数应含 --ytdl-raw-options=socket-timeout=15,retries=2:\n%s", data)
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
	// binPath 用不存在的路径：ensureConnected 会触发重连但 exec 必然失败，
	// 命令应快速返回错误（不能用 "mpv"——本机真实 mpv 会被启动并连接成功）。
	p := NewMpvPlayer(filepath.Join(t.TempDir(), "no-such-mpv"), filepath.Join(t.TempDir(), "x.sock"))
	if _, err := p.Volume(); err == nil {
		t.Fatal("未连接时 Volume 应报错")
	}
	if err := p.SetVolume(50); err == nil {
		t.Fatal("未连接时 SetVolume 应报错")
	}
}

// ---------------------------------------------------------------------------
// 断线自动重连测试（helper 进程模式模拟真实 mpv 进程死亡 + 重启）
// ---------------------------------------------------------------------------

// TestFakeMpvProcess 是 fake mpv 进程的测试入口（helper 进程模式）：
// 普通测试运行时不做事；设置 MUSIC_TUI_FAKE_MPV=1 时以"mpv 可执行文件"
// 身份运行——监听 MUSIC_TUI_FAKE_MPV_SOCKET 指定的 IPC socket 并应答
// 命令，直到进程被杀。设置 MUSIC_TUI_FAKE_MPV_LOG 时，启动时向该文件
// 追加一行 "started"（用于断言进程启动次数），收到命令时追加命令行。
// 注意：helper 场景不能使用 t.Fatalf，错误用 log.Fatalf 或忽略。
func TestFakeMpvProcess(t *testing.T) {
	if os.Getenv("MUSIC_TUI_FAKE_MPV") != "1" {
		return
	}
	socket := os.Getenv("MUSIC_TUI_FAKE_MPV_SOCKET")
	if logPath := os.Getenv("MUSIC_TUI_FAKE_MPV_LOG"); logPath != "" {
		appendFakeLog(logPath, "started")
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("fake mpv: listen %s: %v", socket, err)
	}
	// 接受所有连接：waitForSocket 的探测连接会立即关闭，随后才是
	// mpvipc 的真实连接。进程被杀时 Accept 报错即退出进程。
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveFakeMpvConn(conn)
	}
}

// serveFakeMpvConn 应答 fake mpv 进程的一条客户端连接：逐行读取
// mpv JSON IPC 命令并回复 success（协议与 fakeMpvServer 一致）。
func serveFakeMpvConn(conn net.Conn) {
	defer conn.Close()
	logPath := os.Getenv("MUSIC_TUI_FAKE_MPV_LOG")
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var req struct {
			Command []interface{} `json:"command"`
			ID      int64         `json:"request_id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if logPath != "" {
			if line, err := json.Marshal(req.Command); err == nil {
				appendFakeLog(logPath, "cmd "+string(line))
			}
		}
		status := "success"
		var data interface{}
		if len(req.Command) > 0 && req.Command[0] == "get_property" {
			if len(req.Command) > 1 {
				if prop, ok := req.Command[1].(string); ok && prop == "duration" {
					data = 217.0
				} else {
					status = "property unavailable"
				}
			}
		}
		if err := writeFakeMpvResponse(conn, req.ID, status, data); err != nil {
			return
		}
	}
}

// appendFakeLog 向日志文件追加一行（跨进程断言用；失败静默忽略）。
func appendFakeLog(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s\n", line)
}

// writeFakeMpvWrapperScript 生成 mpv 包装脚本：Start() 以 mpv 参数启动它，
// 脚本解析出 --input-ipc-server 的 socket 路径后 exec 测试二进制重入
// TestFakeMpvProcess 作为 fake mpv 进程（环境变量 MUSIC_TUI_FAKE_MPV_LOG
// 由 t.Setenv 设置，随 exec 链透传）。设置 MUSIC_TUI_FAKE_MPV_LOG 时
// 在 exec 前把收到的全部参数记为一行 "args: $*"（启动参数断言用）。
func writeFakeMpvWrapperScript(t *testing.T, dir string) string {
	t.Helper()
	script := filepath.Join(dir, "fake-mpv.sh")
	content := fmt.Sprintf(`#!/bin/sh
# 测试用 mpv 包装脚本：解析 --input-ipc-server 参数，转交 fake mpv helper 进程
for arg in "$@"; do
    case "$arg" in
        --input-ipc-server=*) export MUSIC_TUI_FAKE_MPV_SOCKET="${arg#--input-ipc-server=}" ;;
    esac
done
if [ -n "$MUSIC_TUI_FAKE_MPV_LOG" ]; then
    echo "args: $*" >> "$MUSIC_TUI_FAKE_MPV_LOG"
fi
export MUSIC_TUI_FAKE_MPV=1
exec "%s" -test.run=TestFakeMpvProcess
`, os.Args[0])
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// killMpvProcess 杀掉当前 mpv 进程，模拟 mpv 崩溃/被外部杀死。
func killMpvProcess(t *testing.T, p *MpvPlayer) {
	t.Helper()
	p.stateMu.Lock()
	cmd := p.cmd
	p.stateMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("mpv 进程不存在")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
}

// waitConnected 等待播放器连接可用（自动重连完成），超时则失败。
// 只轮询 stateMu 保护的 p.conn（不调 mpvipc.IsClosed，见 ensureConnected 注释）。
func waitConnected(t *testing.T, p *MpvPlayer, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p.stateMu.Lock()
		conn := p.conn
		p.stateMu.Unlock()
		if conn != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待连接恢复超时（%s）", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitConnClosed 等待当前连接被清理（断开已被 pump/onDisconnect 处理），
// 之后命令路径必然走 ensureConnected 的重连逻辑。
func waitConnClosed(t *testing.T, p *MpvPlayer, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p.stateMu.Lock()
		closed := p.conn == nil
		p.stateMu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待连接断开超时（%s）", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitErrorEvent 等待下一个 ErrorEvent（丢弃其他类型事件）。
func waitErrorEvent(t *testing.T, p *MpvPlayer, timeout time.Duration) ErrorEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-p.Events():
			if ee, ok := ev.(ErrorEvent); ok {
				return ee
			}
		case <-deadline:
			t.Fatalf("等待 ErrorEvent 超时（%s）", timeout)
			return ErrorEvent{}
		}
	}
}

// waitErrorEventContaining 等待文案包含 substr 的 ErrorEvent。
func waitErrorEventContaining(t *testing.T, p *MpvPlayer, substr string, timeout time.Duration) ErrorEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-p.Events():
			if ee, ok := ev.(ErrorEvent); ok && strings.Contains(ee.Err.Error(), substr) {
				return ee
			}
		case <-deadline:
			t.Fatalf("等待含 %q 的 ErrorEvent 超时（%s）", substr, timeout)
			return ErrorEvent{}
		}
	}
}

// countLogLines 统计日志文件中以 prefix 开头的行数（文件不存在 = 0）。
func countLogLines(t *testing.T, path, prefix string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// waitForLogLine 轮询日志文件直到出现包含 substr 的行（超时则失败）。
func waitForLogLine(t *testing.T, path, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), substr) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("日志中未出现 %q（%s）", substr, path)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// 核心场景：mpv 进程死亡后自动重连。用真实 Start() 启动 fake mpv 进程
// （helper 进程），杀掉进程后应自动重启进程并恢复连接，无需任何命令触发；
// 恢复后 Play 直接可用且新进程确实收到 loadfile 命令。
func TestMpvPlayerAutoReconnectAfterProcessDeath(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	logPath := filepath.Join(dir, "starts.log")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", logPath)
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// 杀掉 mpv 进程：应触发断开事件 + 自动重连
	killMpvProcess(t, p)
	ev := waitErrorEvent(t, p, 5*time.Second)
	if !strings.Contains(ev.Err.Error(), "自动重连") {
		t.Errorf("断开事件文案 = %v, want 含自动重连", ev.Err)
	}

	// 自动重连应在 5s 内恢复连接（不依赖任何命令触发）
	waitConnected(t, p, 5*time.Second)

	// 重连后的新进程可正常使用
	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatalf("重连后 Play 应成功: %v", err)
	}
	// 新进程确实收到了 loadfile 命令（helper 记录在日志）
	waitForLogLine(t, logPath, `"loadfile"`, 3*time.Second)

	// 进程启动次数 = 初始 1 次 + 自动重连 1 次
	if got := countLogLines(t, logPath, "started"); got != 2 {
		t.Errorf("mpv 启动次数 = %d, want 2（初始 + 自动重连）", got)
	}
}

// 惰性重连：断开后直接 Play（不等后台自动重连）也应成功——命令路径
// 触发或等待单飞重连，而不是报"mpv 未连接"。
func TestMpvPlayerLazyReconnectOnPlay(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", filepath.Join(dir, "starts.log"))
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	killMpvProcess(t, p)
	// 等断开被检测到（连接进入未连接状态），再立即 Play
	waitConnClosed(t, p, 5*time.Second)

	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatalf("断开后 Play 应自动触发重连并成功: %v", err)
	}
	waitConnected(t, p, 5*time.Second)
}

// 重连失败：自动重连 3 次全部失败后发出明确 ErrorEvent（不静默）；
// 冷却期内 Play 快速失败（含"重连失败"文案）且不再发起新进程（防风暴）。
func TestMpvPlayerReconnectFailureGivesClearError(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", filepath.Join(dir, "starts.log"))
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// 换成"启动即失败"的坏脚本（exit 1），每次启动追加一行计数
	badLog := filepath.Join(dir, "bad-starts.log")
	badScript := filepath.Join(dir, "bad-mpv.sh")
	if err := os.WriteFile(badScript, []byte(fmt.Sprintf("#!/bin/sh\necho started >> %s\nexit 1\n", badLog)), 0o755); err != nil {
		t.Fatal(err)
	}
	p.stateMu.Lock()
	p.binPath = badScript
	p.stateMu.Unlock()

	killMpvProcess(t, p)

	// 自动重连 3 次（间隔 1s）全部失败后应发出明确错误事件
	ev := waitErrorEventContaining(t, p, "重连失败", 10*time.Second)
	if !strings.Contains(ev.Err.Error(), "自动重连失败") {
		t.Errorf("自动重连失败事件文案 = %v, want 含自动重连失败", ev.Err)
	}

	// 冷却期内连续命令：全部快速失败、文案含"重连失败"，且不发起新进程
	start := time.Now()
	calls := []struct {
		name string
		call func() error
	}{
		{"Play", func() error { return p.Play("https://example.com/a.mp3") }},
		{"Pause", func() error { return p.Pause() }},
		{"Resume", func() error { return p.Resume() }},
		{"Seek", func() error { return p.Seek(30) }},
	}
	for _, tc := range calls {
		err := tc.call()
		if err == nil {
			t.Errorf("%s: 重连失败后应报错", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "重连失败") {
			t.Errorf("%s: err = %v, want 含重连失败", tc.name, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("冷却期内命令应快速失败（不等待新重连）: %v", elapsed)
	}

	// 坏脚本启动次数 = 自动重连 3 次尝试，冷却期内命令不应增加
	if got := countLogLines(t, badLog, "started"); got > 3 {
		t.Errorf("坏脚本启动次数 = %d, want ≤ 3（自动重连 3 次尝试）", got)
	}
}

// 单飞：断开后并发多个 ensureConnected 应合并为一次重连——全部成功，
// 且进程启动次数不随并发数增长。正常时序 = 初始 1 次 + 单飞重连 1 次；
// 若后台 autoReconnect 与命令路径恰好分头触发，单飞也会合并为同一次，
// 故上限为 2。并发用 ensureConnected 而非 Play：mpvipc 的 sendCommand
// 写 socket 无锁，并发 Call 会交错写坏 IPC 流（收到无效 JSON 行被跳过，
// 请求超时）——生产 UI 为单线程顺序调用，不受影响。
func TestMpvPlayerReconnectSingleFlight(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	logPath := filepath.Join(dir, "starts.log")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", logPath)
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	killMpvProcess(t, p)
	waitConnClosed(t, p, 5*time.Second)

	const n = 5
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.ensureConnected()
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("ensureConnected[%d] = %v, want nil（单飞重连应合并并成功）", i, errs[i])
		}
	}

	// 重连后的连接可正常使用
	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatalf("重连后 Play 应成功: %v", err)
	}
	waitForLogLine(t, logPath, `"loadfile"`, 3*time.Second)

	if got := countLogLines(t, logPath, "started"); got > 2 {
		t.Errorf("mpv 启动次数 = %d, want ≤ 2（初始 + 单飞合并的重连）", got)
	}
}

// 崩溃循环（端到端）：断开 → 自动重连成功 → 立即再断开，循环多轮后播放器
// 必须仍可用（Play 成功），不得出现"conn 非 nil 但无 pump"的卡死终态。
func TestMpvPlayerReconnectCrashLoop(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	logPath := filepath.Join(dir, "starts.log")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", logPath)
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	const rounds = 3
	for i := 0; i < rounds; i++ {
		killMpvProcess(t, p)
		// 等断开被处理（conn 清理）并自动重连成功，再进入下一轮
		waitConnClosed(t, p, 5*time.Second)
		waitConnected(t, p, 5*time.Second)
	}

	// 多轮崩溃循环后播放器仍可用：Play 成功且新进程收到 loadfile
	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatalf("崩溃循环后 Play 应成功: %v", err)
	}
	waitForLogLine(t, logPath, `"loadfile"`, 3*time.Second)

	// 每轮恰好一次重连：初始 1 次 + 每轮 1 次
	if got := countLogLines(t, logPath, "started"); got != rounds+1 {
		t.Errorf("mpv 启动次数 = %d, want %d（初始 + 每轮自动重连）", got, rounds+1)
	}
}

// 回归（确定性复现审查发现的交错）：重连 leader 完成前，其刚建立的连接
// 又立即死亡。交错时序：mpv 死亡 → onDisconnect → autoReconnect →
// reconnect 成为单飞 leader（reconnecting=true）→ startProcess/connect
// 成功（p.conn=新连接、新 pump 启动）→ 新连接立即死亡 → 新 pump 退出 →
// onDisconnect 先于 leader 尾巴（reconnecting=false）拿到 stateMu。
// 若 onDisconnect 因"重连进行中"而跳过清理（旧实现把"连接已被替换"与
// "重连进行中"合并进 stillCurrent 守卫），终态是 p.conn 挂着死连接且无
// pump：ensureConnected 永远返回 nil、命令全部失败、无 ErrorEvent——
// 永久卡死。真实时序窗口在微秒级（外部杀进程无法稳定命中），故本测试
// 用同包字段按 reconnect() 的写入顺序模拟该交错，断言：清理必须发生
// （conn 置 nil），且后续命令经 ensureConnected 惰性重连恢复。
func TestMpvPlayerDisconnectDuringReconnectRecovers(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "mpv.sock")
	t.Setenv("MUSIC_TUI_FAKE_MPV_LOG", filepath.Join(dir, "starts.log"))
	script := writeFakeMpvWrapperScript(t, dir)

	p := NewMpvPlayer(script, socketPath)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// 模拟：重连 leader 已在进行中（与 reconnect() 内的写入一致）
	p.stateMu.Lock()
	p.reconnecting = true
	done := make(chan struct{})
	p.reconnectDone = done
	p.stateMu.Unlock()

	// 新连接（当前 pump 的连接）死亡：pump 退出触发 onDisconnect。
	// 修复后此处必须清理死连接（conn 置 nil），而不是因 reconnecting
	// 跳过清理留下卡死终态。
	killMpvProcess(t, p)
	waitConnClosed(t, p, 5*time.Second)

	// 模拟 leader 尾巴：写回结果并通知等待者（与 reconnect() 尾部一致）
	p.stateMu.Lock()
	p.reconnecting = false
	p.reconnectErr = nil
	close(done)
	p.stateMu.Unlock()

	// 终态必须可恢复：conn 已置 nil，命令经惰性重连成功
	// （旧实现此处 conn 挂着死连接：Play 报库错误且永不重连）
	if err := p.Play("https://example.com/a.mp3"); err != nil {
		t.Fatalf("重连进行中断开后，Play 应经惰性重连恢复: %v", err)
	}
	waitConnected(t, p, 5*time.Second)
}

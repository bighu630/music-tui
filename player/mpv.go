package player

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dexterlb/mpvipc"
)

// 观察属性 ID：mpv observe_property 的第一个参数。
const (
	obsTimePos    int64 = 1
	obsDuration   int64 = 2
	obsPause      int64 = 3
	obsEOFReached int64 = 4
)

// MpvPlayer 通过 JSON IPC 控制 mpv 进程，并把 mpv 事件转换为
// player.Event 推送到 Events() 通道。进程启动与 socket 连接解耦：
// Start() = 启动进程 + connect()；测试直接调用 connect()。
type MpvPlayer struct {
	binPath    string
	socketPath string

	cmd    *exec.Cmd
	waitCh chan error
	conn   *mpvipc.Connection

	events   chan Event
	mu       sync.Mutex
	duration float64
	closed   atomic.Bool
}

// NewMpvPlayer 创建播放器实例。binPath 为 mpv 可执行文件路径。
func NewMpvPlayer(binPath, socketPath string) *MpvPlayer {
	return &MpvPlayer{
		binPath:    binPath,
		socketPath: socketPath,
		events:     make(chan Event, 256),
	}
}

// Start 启动 mpv 进程（--idle=yes 常驻），等待 IPC socket 就绪后
// 连接、注册属性观察并启动事件泵。
func (p *MpvPlayer) Start() error {
	if p.cmd != nil {
		return errors.New("播放器已在运行")
	}
	_ = os.Remove(p.socketPath) // 清理上次残留的 socket 文件
	args := []string{
		"--idle=yes",
		"--no-video",           // 纯音频，避免 mpv 抢占终端
		"--no-terminal",        // 禁用 mpv 的终端控制，避免与 TUI 抢键盘
		"--keep-open=no",       // 播完即结束，保证 eof-reached 事件
		"--no-resume-playback", // 不写恢复播放状态文件
		"--input-ipc-server=" + p.socketPath,
	}
	cmd := exec.Command(p.binPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 mpv 进程: %w", err)
	}
	p.cmd = cmd
	p.waitCh = make(chan error, 1)
	go func() { p.waitCh <- cmd.Wait() }()

	if err := p.waitForSocket(5 * time.Second); err != nil {
		_ = p.killProcess()
		return err
	}
	if err := p.connect(); err != nil {
		_ = p.killProcess()
		return err
	}
	return nil
}

// waitForSocket 轮询等待 mpv 的 IPC socket 出现；进程提前退出则报错。
func (p *MpvPlayer) waitForSocket(timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		conn, err := net.Dial("unix", p.socketPath)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case werr := <-p.waitCh:
			return fmt.Errorf("mpv 进程提前退出: %w", werr)
		case <-deadline:
			return fmt.Errorf("mpv IPC socket 超时未就绪: %s", p.socketPath)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// connect 连接 socket、注册属性观察并启动事件泵（测试可直接调用）。
func (p *MpvPlayer) connect() error {
	conn := mpvipc.NewConnection(p.socketPath)
	if err := conn.Open(); err != nil {
		return fmt.Errorf("连接 mpv IPC: %w", err)
	}
	p.conn = conn
	if err := p.observeProperties(); err != nil {
		_ = conn.Close()
		return err
	}
	go p.pump()
	return nil
}

// observeProperties 注册进度/时长/暂停/结束四个属性的观察。
func (p *MpvPlayer) observeProperties() error {
	props := []struct {
		id   int64
		name string
	}{
		{obsTimePos, "time-pos"},
		{obsDuration, "duration"},
		{obsPause, "pause"},
		{obsEOFReached, "eof-reached"},
	}
	for _, prop := range props {
		if _, err := p.conn.Call("observe_property", prop.id, prop.name); err != nil {
			return fmt.Errorf("observe_property %s: %w", prop.name, err)
		}
	}
	return nil
}

// pump 从 mpv 事件通道读取事件并转换为 player.Event 分发。
// mpvipc 在连接断开时不会自动关闭事件通道，必须借助
// WaitUntilClosed + stop 信号解除 range 阻塞（详见调研结论 0.2）。
func (p *MpvPlayer) pump() {
	events, stop := p.conn.NewEventListener()
	defer close(stop)

	go func() {
		p.conn.WaitUntilClosed()
		stop <- struct{}{}
	}()

	for ev := range events {
		switch ev.Name {
		case "property-change":
			p.handlePropertyChange(ev)
		case "file-loaded":
			p.emit(TrackStartedEvent{Duration: p.getDuration()})
		case "end-file":
			if ev.Reason == "error" {
				p.emit(ErrorEvent{Err: fmt.Errorf("mpv 播放出错（end-file reason=error）")})
			}
		}
	}
	// events 通道关闭 = mpv 断开（崩溃或被退出）
	if !p.closed.Load() {
		p.emit(ErrorEvent{Err: errors.New("mpv 连接断开")})
	}
}

// handlePropertyChange 把 mpv property-change 事件映射为业务事件。
func (p *MpvPlayer) handlePropertyChange(ev *mpvipc.Event) {
	switch ev.ID {
	case obsTimePos:
		pos, ok := toFloat64(ev.Data)
		if !ok {
			return
		}
		if d := p.getDuration(); d > 0 && pos > d {
			return // 异常进度值（Position > Duration）过滤丢弃
		}
		p.emit(ProgressEvent{Position: pos, Duration: p.getDuration()})
	case obsDuration:
		if d, ok := toFloat64(ev.Data); ok {
			p.setDuration(d)
		}
	case obsPause:
		if paused, ok := toBool(ev.Data); ok {
			p.emit(StateEvent{Playing: !paused})
		}
	case obsEOFReached:
		if eof, ok := toBool(ev.Data); ok && eof {
			p.emit(TrackEndedEvent{})
		}
	}
}

// Play 开始播放指定 URL（loadfile 替换当前歌曲）。
func (p *MpvPlayer) Play(url string) error {
	if p.conn == nil || p.conn.IsClosed() {
		return errors.New("mpv 未连接")
	}
	p.setDuration(0)
	if _, err := p.conn.Call("loadfile", url); err != nil {
		return fmt.Errorf("loadfile: %w", err)
	}
	return nil
}

// Pause 暂停播放。
func (p *MpvPlayer) Pause() error {
	return p.conn.Set("pause", true)
}

// Resume 继续播放。
func (p *MpvPlayer) Resume() error {
	return p.conn.Set("pause", false)
}

// Seek 跳转到指定秒数（绝对位置）。
func (p *MpvPlayer) Seek(seconds float64) error {
	if _, err := p.conn.Call("seek", seconds, "absolute"); err != nil {
		return fmt.Errorf("seek: %w", err)
	}
	return nil
}

// Events 返回播放器事件流。
func (p *MpvPlayer) Events() <-chan Event {
	return p.events
}

// emit 非阻塞推送事件；缓冲满时丢弃（避免阻塞 mpv 事件读取）。
func (p *MpvPlayer) emit(ev Event) {
	select {
	case p.events <- ev:
	default:
	}
}

// Close 退出 mpv 并清理 socket 文件；可重复调用。
// 注意：mpv 对 quit 命令可能不回复就退出，Call 可能阻塞，
// 因此放到 goroutine 里加超时，超时后直接杀掉进程。
func (p *MpvPlayer) Close() error {
	if p.closed.Load() {
		return nil
	}
	p.closed.Store(true)
	if p.conn != nil && !p.conn.IsClosed() {
		done := make(chan struct{})
		go func() {
			_, _ = p.conn.Call("quit")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
		_ = p.conn.Close()
	}
	_ = p.killProcess()
	_ = os.Remove(p.socketPath)
	return nil
}

// killProcess 杀掉 mpv 进程并回收 Wait goroutine（非阻塞）。
func (p *MpvPlayer) killProcess() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.waitCh:
	default:
	}
	return nil
}

func (p *MpvPlayer) getDuration() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.duration
}

func (p *MpvPlayer) setDuration(d float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.duration = d
}

func toFloat64(v interface{}) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func toBool(v interface{}) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

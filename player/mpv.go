package player

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dexterlb/mpvipc"
)

// 观察属性 ID：mpv observe_property 的第一个参数。
const (
	obsTimePos  int64 = 1
	obsDuration int64 = 2
	obsPause    int64 = 3
)

// 断线自动重连参数。
const (
	maxReconnectAttempts = 3               // 后台自动重连最大尝试次数
	reconnectInterval    = 1 * time.Second // 每次自动重连尝试的间隔
	reconnectCooldown    = 2 * time.Second // 重连失败后的冷却期：期间命令不再发起新重连（防风暴）
)

// loadWatchdogTimeout 加载看门狗时限：loadfile 后限时未收到 file-loaded/end-file
// 即判定加载悬挂（yt-dlp 取流卡死/403 重试退避/网络黑洞），emit LoadTimeoutError。
// 包级变量：测试可调小以缩短等待。
var loadWatchdogTimeout = 30 * time.Second

// MpvPlayer 通过 JSON IPC 控制 mpv 进程，并把 mpv 事件转换为
// player.Event 推送到 Events() 通道。进程启动与 socket 连接解耦：
// Start() = 启动进程 + connect()；测试直接调用 connect()。
type MpvPlayer struct {
	binPath    string
	socketPath string

	// 取流附加配置（可选，均空时行为与旧版完全一致）：cookieFile 经
	// --ytdl-raw-options=cookiefile= 传给 yt-dlp；headers 生成临时配置文件
	// 经 config-locations= 指向（见 buildYtdlpConf）。ytdlpConfPath 由
	// startProcess 写入/Close 删除（stateMu 保护）。
	cookieFile    string
	headers       map[string]string
	ytdlpConfPath string

	cmd    *exec.Cmd
	waitCh chan error
	conn   *mpvipc.Connection

	// stateMu 保护进程/连接字段与重连单飞状态：pump 断开路径、命令路径
	// 与 Close 退出路径都会修改这些字段，必须互斥访问。
	stateMu sync.Mutex

	// 重连单飞状态（stateMu 保护）
	reconnecting  bool          // 是否有重连进行中
	reconnectDone chan struct{} // 进行中重连的完成通知（reconnecting=true 时非 nil）
	reconnectErr  error         // 最近一次重连结果（等待者共享）
	lastFail      time.Time     // 最近一次重连失败时刻（冷却期判断）

	// lastLoadID 最近一次成功 loadfile 的 playlist_entry_id（stateMu 保护）。
	// end-file error 事件按此归属过滤：事件 id 与它不一致 = 陈旧事件（旧曲取流
	// 失败晚到），丢弃防 UI 误删健康缓存（回归：恢复播放误报"YouTube 未返回
	// 可播放音轨"）。0 = 未知（旧版 mpv 无此字段/断开后），过滤禁用保守放行。
	lastLoadID int64

	// 加载看门狗状态（stateMu 保护）。loadDeadline 是看门狗截止时刻，零值 =
	// 无活跃加载（新 loadfile 即新一轮：deadline 整体替换，旧 deadline 作废，
	// 不会误报——回归：TestLoadWatchdogResetOnNewLoad）；loadResolved 是当前
	// 加载是否已有结论（file-loaded/end-file eof|error 到达；armWatchdog 置
	// false，disarmWatchdog 置 true）——超时边界竞态防护：加载恰在 deadline 后
	// 成功时，file-loaded 先于 watchdog 判定到达（disarm 已执行）则不报超时
	//（审查 P2-3）；watchdogSeq 是看门狗循环代际（connect 自增，旧循环发现
	// 代际不符即退出，重连后不残留 goroutine；superseded 检查必须先于 expired
	// 认领，旧循环不得清新代际的 deadline——回归：TestWatchdogSupersededBeforeClaim）。
	loadDeadline time.Time // 看门狗截止时刻；零值 = 无活跃加载
	loadResolved bool      // 当前加载是否已有结论（file-loaded/end-file 到达）
	watchdogSeq  int       // 看门狗循环代际：每次 connect 自增；旧循环发现代际不符即退出

	events   chan Event
	mu       sync.Mutex
	duration float64
	closed   atomic.Bool

	subsMu sync.Mutex
	subs   map[chan Event]struct{} // Subscribe() 广播订阅者
}

// NewMpvPlayer 创建播放器实例。binPath 为 mpv 可执行文件路径。
// cookieFile/headers 可选：附加到 mpv 的 --ytdl-raw-options（cookiefile=/
// config-locations=），均空时不改变既有行为。
func NewMpvPlayer(binPath, socketPath string, cookieFile string, headers map[string]string) *MpvPlayer {
	return &MpvPlayer{
		binPath:    binPath,
		socketPath: socketPath,
		cookieFile: cookieFile,
		headers:    headers,
		events:     make(chan Event, 256),
	}
}

// Start 启动 mpv 进程（--idle=yes 常驻），等待 IPC socket 就绪后
// 连接、注册属性观察并启动事件泵。
func (p *MpvPlayer) Start() error {
	p.stateMu.Lock()
	running := p.cmd != nil
	p.stateMu.Unlock()
	if running {
		return errors.New("播放器已在运行")
	}
	return p.startProcess()
}

// startProcess 启动 mpv 进程并建立 IPC 连接：清理残留 socket → exec 启动
// → 等待 socket 就绪 → connect()。任一步失败都会杀掉进程并清空
// cmd/waitCh/conn，不残留脏状态（Start 与自动重连共用）。
func (p *MpvPlayer) startProcess() error {
	_ = os.Remove(p.socketPath) // 清理上次残留的 socket 文件
	// --ytdl-raw-options 动态拼接：打底 socket-timeout=15,retries=2（yt-dlp 取流
	// 收紧：403/网络黑洞快速失败），cookieFile/headers 非空时追加 cookiefile= 与
	// config-locations=（mpv 列表值语法：值含逗号时双引号包裹，内部 `"` 无法
	// 转义表示）。headers 生成临时配置失败仅 log 警告并跳过 config-locations
	// 段——绝不因 header 配置问题导致 mpv 启动失败（取流功能降级不崩溃）。
	ytdlRawOpts := "socket-timeout=15,retries=2"
	if p.cookieFile != "" {
		ytdlRawOpts += ",cookiefile=" + quoteMpvOptionValue(p.cookieFile)
	}
	if len(p.headers) > 0 {
		confPath, err := buildYtdlpConf(p.headers)
		if err != nil {
			log.Printf("生成 yt-dlp 临时配置失败，跳过 config-locations（取流降级继续）: %v", err)
		} else {
			p.stateMu.Lock()
			p.ytdlpConfPath = confPath // 幂等：每次重新生成（覆盖写同一路径，重连场景）
			p.stateMu.Unlock()
			ytdlRawOpts += ",config-locations=" + quoteMpvOptionValue(confPath)
		}
	}
	args := []string{
		"--idle=yes",
		"--no-video",                        // 纯音频，避免 mpv 抢占终端
		"--no-terminal",                     // 禁用 mpv 的终端控制，避免与 TUI 抢键盘
		"--keep-open=no",                    // 播完即退出，可靠触发 end-file reason=eof
		"--ytdl-format=bestaudio",           // 纯音频播放器只需音频流：避免同时开视频+音频两个流（403 风控暴露面减半）
		"--ytdl-raw-options=" + ytdlRawOpts, // yt-dlp 取流收紧：403/网络黑洞快速失败（默认 retries=10 可拖 1-2 分钟）
		"--no-resume-playback",              // 不写恢复播放状态文件
		"--input-ipc-server=" + p.socketPath,
	}
	p.stateMu.Lock()
	binPath := p.binPath
	p.stateMu.Unlock()
	cmd := exec.Command(binPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 mpv 进程: %w", err)
	}
	// waitCh 局部捕获：清理并发置 nil 后，Wait goroutine 仍写旧通道不阻塞
	waitCh := make(chan error, 1)
	p.stateMu.Lock()
	p.cmd = cmd
	p.waitCh = waitCh
	p.stateMu.Unlock()
	go func() { waitCh <- cmd.Wait() }()

	if err := p.waitForSocket(waitCh, 5*time.Second); err != nil {
		_ = p.killProcess()
		p.clearProcess()
		return err
	}
	if err := p.connect(); err != nil {
		_ = p.killProcess()
		p.clearProcess()
		return err
	}
	return nil
}

// waitForSocket 轮询等待 mpv 的 IPC socket 出现；进程提前退出则报错。
// waitCh 由调用方传入（startProcess 的局部通道），避免与清理并发竞态。
func (p *MpvPlayer) waitForSocket(waitCh chan error, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		conn, err := net.Dial("unix", p.socketPath)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case werr := <-waitCh:
			if werr == nil {
				return errors.New("mpv 进程提前退出（退出码 0）")
			}
			return fmt.Errorf("mpv 进程提前退出: %w", werr)
		case <-deadline:
			return fmt.Errorf("mpv IPC socket 超时未就绪: %s", p.socketPath)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// connect 连接 socket、注册属性观察并启动事件泵（测试可直接调用）。
// 注意：p.conn 在属性观察成功后才发布——观察期间若 mpv 中途死亡，
// 观察会快速失败并清理，不会留下"p.conn 已设置但 pump 未启动"的
// 半连接卡死态（否则无人检测断开，命令永久失败）。
func (p *MpvPlayer) connect() error {
	conn := mpvipc.NewConnection(p.socketPath)
	if err := conn.Open(); err != nil {
		return fmt.Errorf("连接 mpv IPC: %w", err)
	}
	// 持锁执行属性观察：与 Close 的并发清理互斥（mpvipc 的 Call/Close
	// 无锁访问内部字段，并发即数据竞争）；观察最坏 1.5s（每条 500ms
	// 超时），持锁有界，不构成死锁（无人持锁等待观察的结果）。
	p.stateMu.Lock()
	if p.closed.Load() {
		p.stateMu.Unlock()
		_ = conn.Close()
		return errors.New("播放器已关闭")
	}
	if err := p.observeProperties(conn); err != nil {
		p.stateMu.Unlock()
		_ = conn.Close()
		return err
	}
	p.conn = conn
	p.stateMu.Unlock()
	go p.pump(conn)
	// 启动加载看门狗：loadDeadline 零值时空转，无副作用。
	// 代际快照：重连后新 connect 自增 seq，旧循环检测到代际不符即退出
	//（防多次重连后旧看门狗 goroutine 残留堆积）。
	p.stateMu.Lock()
	p.watchdogSeq++
	seq := p.watchdogSeq
	p.stateMu.Unlock()
	go p.watchdogLoop(seq)
	return nil
}

// observeProperties 注册进度/时长/暂停三个属性的观察。
// 不观察 eof-reached：keep-open=no 下 EOF 瞬间属性即变 unavailable
// （data=null），结束信号统一用 end-file reason=eof。
// 每条命令带 500ms 超时：mpv 中途死亡/不响应时 mpvipc 的 Call 会永久
// 阻塞（关闭连接也不通知 waitingRequests），重连路径必须快速失败以便
// 重试，否则 connect() 永久挂起、重连 leader 与所有等待者都被拖死。
func (p *MpvPlayer) observeProperties(conn *mpvipc.Connection) error {
	props := []struct {
		id   int64
		name string
	}{
		{obsTimePos, "time-pos"},
		{obsDuration, "duration"},
		{obsPause, "pause"},
	}
	for _, prop := range props {
		if err := callWithTimeout(func() error {
			_, err := conn.Call("observe_property", prop.id, prop.name)
			return err
		}); err != nil {
			return fmt.Errorf("observe_property %s: %w", prop.name, err)
		}
	}
	return nil
}

// pump 从 mpv 事件通道读取事件并转换为 player.Event 分发。
// mpvipc 在连接断开时不会自动关闭事件通道，必须借助
// WaitUntilClosed + stop 信号解除 range 阻塞（详见调研结论 0.2）。
// conn 为创建本泵的连接（局部捕获）：重连后新连接有新 pump，
// 旧 pump 只操作旧连接，不与清理并发竞态。
func (p *MpvPlayer) pump(conn *mpvipc.Connection) {
	events, stop := conn.NewEventListener()
	defer close(stop)

	go func() {
		conn.WaitUntilClosed()
		stop <- struct{}{}
	}()

	for ev := range events {
		switch ev.Name {
		case "property-change":
			p.handlePropertyChange(ev)
		case "file-loaded":
			p.disarmWatchdog() // 加载成功：看门狗解除（限时等待结束）
			// 真实 mpv 中 duration property-change 晚于 file-loaded 到达，
			// 此时同步 Get 兜底。Get 响应走 mpvipc checkResult 路由（不经过
			// 事件通道），pump 内调用不会死锁；但若 mpv 恰在此刻崩溃/卡死，
			// Get 会永久阻塞在 mpvipc 的结果通道上（关闭连接也不通知 waiting
			// requests），pump 回不到 range 循环 → 断线 ErrorEvent 不再触发。
			// 故加超时：最坏退化为泄漏一个 500ms 内自灭的 goroutine（mpv 永不
			// 响应时永久挂起，与 Close 的 quit goroutine 同款权衡）。
			d := p.getDuration()
			if d <= 0 {
				got := make(chan float64, 1)
				go func() {
					if v, err := conn.Get("duration"); err == nil {
						if f, ok := toFloat64(v); ok {
							got <- f
						}
					}
				}()
				select {
				case f := <-got:
					d = f
				case <-time.After(500 * time.Millisecond):
				}
			}
			p.emit(TrackStartedEvent{Duration: d})
		case "end-file":
			// 结束信号来源：keep-open=no 下 EOF 时 eof-reached 属性立即变
			// unavailable（data=null，toBool 失败），可靠信号是 end-file。
			switch ev.Reason {
			case "eof":
				p.disarmWatchdog() // 播放结束：加载已有结论，看门狗解除
				p.emit(TrackEndedEvent{})
			case "error":
				// 陈旧事件过滤：end-file error 不携带曲目身份，mpv ≥0.33 的
				// playlist_entry_id 字段补上身份——事件 id 与最近一次成功
				// loadfile 的 id 不一致 = 旧曲取流失败晚到（用户已切到缓存曲目），
				// UI 按"当前曲目"归属会误删健康缓存，故丢弃；旧版 mpv 无该字段
				// 或 id 未知（lastLoadID=0）时过滤禁用，保守放行。
				if p.isStaleEndFileError(ev) {
					continue
				}
				p.disarmWatchdog() // 当前曲加载失败：加载已有结论，看门狗解除（陈旧错误不 disarm——见过滤）
				// 提取 mpv IPC file_error 字段的诊断文本（如 "no audio or video data played"）；
				// mpvipc 的 Event.ExtraData 已保留该字段，旧版 mpv 可能缺失（空串兜底）。
				fileErr := ""
				if ev.ExtraData != nil {
					if s, ok := ev.ExtraData["file_error"].(string); ok {
						fileErr = s
					}
				}
				p.emit(ErrorEvent{Err: &LoadFailedError{FileError: fileErr}})
			}
		}
	}
	// events 通道关闭 = mpv 断开（崩溃或被退出）
	if !p.closed.Load() {
		p.onDisconnect(conn)
	}
}

// onDisconnect 处理 mpv 断开：诊断进程退出原因、清理死状态、
// 通知 UI 并启动后台自动重连。
func (p *MpvPlayer) onDisconnect(conn *mpvipc.Connection) {
	// 诊断：非阻塞读取 mpv 退出码，区分"进程已退出"与"连接断开"
	p.stateMu.Lock()
	p.lastLoadID = 0 // 新连接无身份信息：过滤禁用（保守放行），首个 loadfile 后重新启用
	// 断线时刻在途加载必然已死：看门狗解除，重连后新连接无活跃加载。若不清，
	// 重连后新 watchdogLoop 继承旧 deadline（最多还有 30s）到期误报
	// LoadTimeoutError → UI 无用户操作自动重播断线前曲目（审查 P1）。
	p.loadDeadline = time.Time{}
	p.loadResolved = true // 无活跃加载：不存在"加载结论未定"状态
	waitCh := p.waitCh
	current := p.conn
	p.stateMu.Unlock()
	select {
	case werr := <-waitCh:
		log.Printf("mpv 进程已退出: %v（触发自动重连）", werr)
	default:
		log.Printf("mpv IPC 连接断开（进程可能仍存活，触发自动重连）")
	}
	if current != conn {
		// 连接已被更新的重连替换（命令路径已接管恢复）：本泵不清理，
		// 避免误杀新进程。
		return
	}
	_ = p.killProcess()
	p.clearProcess()
	p.stateMu.Lock()
	reconnecting := p.reconnecting
	p.stateMu.Unlock()
	if reconnecting {
		// 进行中重连（leader）刚建立的连接又已死：只清理死状态，不启动
		// autoReconnect（避免与进行中重连并发拉新进程）。leader 尾巴
		// （reconnectErr/close(done)）不碰 cmd/conn，完成后 conn==nil，
		// 后续命令经 ensureConnected 惰性重连恢复。此路径不发 ErrorEvent
		// （重连进行中连接又死），UI 保持原状即可。
		return
	}
	p.emit(ErrorEvent{Err: errors.New("mpv 连接断开，正在自动重连…")})
	go p.autoReconnect()
}

// autoReconnect 后台自动重连：最多尝试 maxReconnectAttempts 次，每次间隔
// reconnectInterval；某次成功即返回，全部失败则发出明确错误事件（不静默）。
func (p *MpvPlayer) autoReconnect() {
	for i := 0; i < maxReconnectAttempts; i++ {
		if p.closed.Load() {
			return
		}
		err := p.reconnect()
		if err == nil {
			return
		}
		if i == maxReconnectAttempts-1 {
			p.emit(ErrorEvent{Err: fmt.Errorf("mpv 自动重连失败：%w", err)})
			return
		}
		time.Sleep(reconnectInterval)
	}
}

// reconnect 重新启动 mpv 进程并建立连接（单飞：并发调用合并为一次，
// 等待者共享同一结果）。
func (p *MpvPlayer) reconnect() error {
	p.stateMu.Lock()
	if p.reconnecting {
		done := p.reconnectDone
		p.stateMu.Unlock()
		<-done // 等待进行中的重连完成，读取其共享结果
		p.stateMu.Lock()
		err := p.reconnectErr
		p.stateMu.Unlock()
		return err
	}
	p.reconnecting = true
	done := make(chan struct{})
	p.reconnectDone = done
	p.stateMu.Unlock()

	err := p.doReconnect()

	p.stateMu.Lock()
	p.reconnecting = false
	p.reconnectErr = err
	close(done)
	p.stateMu.Unlock()
	return err
}

// doReconnect 实际执行一次重连（单飞发起者调用，不持锁）：
// 检查关闭 → 清理死进程状态 → 重启 mpv 进程并连接。
func (p *MpvPlayer) doReconnect() error {
	if p.closed.Load() {
		return errors.New("播放器已关闭")
	}
	_ = p.killProcess()
	p.clearProcess()
	if err := p.startProcess(); err != nil {
		p.stateMu.Lock()
		p.lastFail = time.Now() // 记录失败时刻，供冷却期防风暴
		p.stateMu.Unlock()
		return err
	}
	if p.closed.Load() {
		// Close 与重连并发：Close 可能恰好没杀掉本进程（字段尚未赋值），
		// 主动清理避免残留；确保 Close 不会被重连拖住。
		_ = p.killProcess()
		p.clearProcess()
		return errors.New("播放器已关闭")
	}
	return nil
}

// ensureConnected 确保 IPC 连接可用；不可用时触发/等待一次重连（单飞）。
// 重连失败后有冷却期（reconnectCooldown）：期间不发起新尝试，
// 快速返回错误（防风暴、防 UI 冻结）。
//
// 注意：命令触发的重连最坏同步阻塞调用方约 5 秒（waitForSocket 上限，
// 与 Start 同款）；失败进入冷却期后则快速返回，不再等待。
//
// 注意：不调用 mpvipc 的 IsClosed()——该库的 IsClosed 无锁读取内部
// client 字段，与断线时库内部 Close 的写入并发即触发数据竞争（pre-existing，
// 本实现只是不再触发它）。连接的"已断开"状态由 pump 退出后的 onDisconnect
// 清理（置 nil）传递，本方法只读 stateMu 保护的 p.conn。
func (p *MpvPlayer) ensureConnected() error {
	p.stateMu.Lock()
	if p.conn != nil {
		p.stateMu.Unlock()
		return nil
	}
	reconnecting := p.reconnecting
	done := p.reconnectDone
	cooldown := !p.lastFail.IsZero() && time.Since(p.lastFail) < reconnectCooldown
	lastErr := p.reconnectErr
	p.stateMu.Unlock()

	if reconnecting {
		<-done
		p.stateMu.Lock()
		err := p.reconnectErr
		p.stateMu.Unlock()
		return err
	}
	if cooldown {
		return fmt.Errorf("mpv 重连失败（%v），正在自动重试", lastErr)
	}
	return p.reconnect()
}

// currentConn 返回当前连接（可能为 nil）；命令在 ensureConnected
// 之后用它执行 IPC，避免直接并发读写字段。
func (p *MpvPlayer) currentConn() *mpvipc.Connection {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.conn
}

// armWatchdog 武装加载看门狗：loadfile 成功后限时等待 file-loaded/end-file。
// 新 loadfile 即新一轮：deadline 整体替换（旧 deadline 作废，不会误报——
// 回归：TestLoadWatchdogResetOnNewLoad）。
func (p *MpvPlayer) armWatchdog() {
	p.stateMu.Lock()
	p.loadDeadline = time.Now().Add(loadWatchdogTimeout)
	p.loadResolved = false // 新一轮加载：结论未定
	p.stateMu.Unlock()
}

// disarmWatchdog 解除加载看门狗（收到 file-loaded/end-file = 加载有结论）。
// loadResolved 置 true：看门狗据此不再误报——加载恰在 deadline 后成功时，
// file-loaded 先于 watchdog tick 到达则超时错误被抑制（审查 P2-3）。
func (p *MpvPlayer) disarmWatchdog() {
	p.stateMu.Lock()
	p.loadDeadline = time.Time{}
	p.loadResolved = true // 加载已有结论
	p.stateMu.Unlock()
}

// watchdogLoop 加载看门狗：周期检查 loadDeadline，到期且仍无 file-loaded/
// end-file 结论认领 → emit LoadTimeoutError（UI 走重试/跳过链路，不再无限期
// 静默卡住）。到期清 deadline 认领本次超时防重复触发。锁内先查代际：seq 与
// 当前 connect 代际不符（已被新连接取代）立即退出、不碰任何状态——若先
// claim 后查 superseded，旧代际 loop 最后一片 tick 会吞掉新代际的 deadline
// （重连后误报超时，审查 P1）。p.closed（Close）时退出，不泄漏 goroutine。
func (p *MpvPlayer) watchdogLoop(seq int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.stateMu.Lock()
			superseded := p.watchdogSeq != seq
			if superseded {
				p.stateMu.Unlock()
				return // 旧代际：不碰 deadline，新代际接管
			}
			// !loadResolved：file-loaded/end-file 已到达（disarm 先执行，加载
			// 已有结论）则不报——加载恰在 deadline 后成功时避免误报。残余窗口
			//（锁内判定后、unlock 前结论到达）为微秒级，可接受（审查 P2-3）。
			expired := !p.loadDeadline.IsZero() && !p.loadResolved && time.Now().After(p.loadDeadline)
			if expired {
				p.loadDeadline = time.Time{} // 认领本次超时，防重复触发
			}
			p.stateMu.Unlock()
			if expired {
				p.emit(ErrorEvent{Err: &LoadTimeoutError{Timeout: loadWatchdogTimeout}})
			}
		case <-time.After(100 * time.Millisecond):
			if p.closed.Load() {
				return
			}
		}
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
		d := p.getDuration() // 取一次局部变量：过滤与 emit 用同一值
		if d > 0 && pos > d {
			return // 异常进度值（Position > Duration）过滤丢弃
		}
		p.emit(ProgressEvent{Position: pos, Duration: d})
	case obsDuration:
		if d, ok := toFloat64(ev.Data); ok {
			p.setDuration(d)
		}
	case obsPause:
		if paused, ok := toBool(ev.Data); ok {
			p.emit(StateEvent{Playing: !paused})
		}
	}
}

// callWithTimeout 带超时执行一条 mpv IPC 命令：mpv 挂死时 mpvipc 的
// Call 会永久阻塞（关闭连接也不通知 waitingRequests），必须加超时兜底。
// 超时返回错误；内部 goroutine 可能永久挂起（每次超时泄漏一个，与 Close
// 的 quit goroutine 同款权衡）——mpv 挂死属罕见故障，可接受。
func callWithTimeout(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(500 * time.Millisecond):
		return errors.New("mpv 命令超时（500ms 未响应）")
	}
}

// Play 开始播放指定 URL（loadfile 替换当前歌曲）。
func (p *MpvPlayer) Play(url string) error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	p.setDuration(0)
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接") // 断开清理恰好在检查后发生：极窄窗口，下次命令会重连
	}
	var data interface{}
	if err := callWithTimeout(func() error {
		var err error
		data, err = conn.Call("loadfile", url)
		return err
	}); err != nil {
		return fmt.Errorf("loadfile: %w", err)
	}
	p.recordLoadID(data)
	p.armWatchdog() // 加载看门狗：限时等待 file-loaded/end-file（取流悬挂时主动报错）
	return nil
}

// PlayPaused 加载指定 URL 但保持暂停（续播恢复用）：先置 pause=yes
// 再 loadfile——mpv 的 pause 属性不随文件重置，新文件加载后保持暂停
// 不发声。start>0 时随 loadfile 的 start= 选项原子定位（mpv ≥0.38 的
// 4 参语法 loadfile <url> replace <index> <options>）：loadfile 响应是
// 即时的（文件异步加载，网络曲目加载窗口达数秒），加载完成前单独下发
// seek 会被 mpv 拒绝（error running command），故定位必须由 loadfile
// 一并完成，消除"加载后立即 seek"的竞态。
func (p *MpvPlayer) PlayPaused(url string, start float64) error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	p.setDuration(0)
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接") // 断开清理恰好在检查后发生：极窄窗口，下次命令会重连
	}
	if err := callWithTimeout(func() error {
		return conn.Set("pause", true)
	}); err != nil {
		return fmt.Errorf("set pause: %w", err)
	}
	var data interface{}
	if err := callWithTimeout(func() error {
		// start 极端值兜底：NaN/负值落入 start<=0 走 2 参从头加载（安全）；
		// +Inf 落入 4 参分支会生成 "start=+Inf"，mpv 接受并钳制到 EOF（无害）；实际来自 time-pos 不会出现。
		var loadErr error
		if start > 0 {
			// 4 参语法（mpv ≥0.38）：loadfile <url> replace <index> <options>，
			// start= 选项随加载原子定位；旧 3 参 options 语法在 0.38+ 已改为
			// playlist index（传 options 会 invalid parameter），本环境 mpv 0.41.0。
			posStr := strconv.FormatFloat(start, 'f', -1, 64) // 避免 %g 科学计数法（1e+09 不被 mpv 选项解析器接受）
			data, loadErr = conn.Call("loadfile", url, "replace", -1, "start="+posStr)
			return loadErr
		}
		data, loadErr = conn.Call("loadfile", url) // 2 参：兼容 mpv <0.38，从头加载
		return loadErr
	}); err != nil {
		return fmt.Errorf("loadfile: %w", err)
	}
	p.recordLoadID(data)
	p.armWatchdog() // 恢复加载同样受看门狗保护：取流悬挂时主动报错（恢复路径已能感知）
	return nil
}

// recordLoadID 记录最近一次成功 loadfile 的 playlist_entry_id（mpv ≥0.33 的
// loadfile 命令响应携带该字段，供 end-file error 归属过滤）。data 非
// map[string]interface{} 或缺少该字段（旧版 mpv）→ 记录 0：过滤禁用，保守放行。
func (p *MpvPlayer) recordLoadID(data interface{}) {
	id := int64(0)
	if m, ok := data.(map[string]interface{}); ok {
		if f, ok := m["playlist_entry_id"].(float64); ok {
			id = int64(f)
		}
	}
	p.stateMu.Lock()
	p.lastLoadID = id
	p.stateMu.Unlock()
}

// isStaleEndFileError 判断 end-file error 事件是否属于更早的 loadfile（陈旧
// 事件）：事件携带 playlist_entry_id 且 lastLoadID 已知且两者不一致时返回 true。
// 事件无该字段（旧版 mpv）或 id 未知时返回 false（保守放行）。读取 lastLoadID
// 须持 stateMu（与 recordLoadID/onDisconnect 写入互斥）。
func (p *MpvPlayer) isStaleEndFileError(ev *mpvipc.Event) bool {
	if ev.ExtraData == nil {
		return false
	}
	evID, ok := ev.ExtraData["playlist_entry_id"].(float64)
	if !ok {
		return false
	}
	p.stateMu.Lock()
	last := p.lastLoadID
	p.stateMu.Unlock()
	if last == 0 {
		return false
	}
	return int64(evID) != last
}

// Pause 暂停播放。
func (p *MpvPlayer) Pause() error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接")
	}
	if err := callWithTimeout(func() error {
		return conn.Set("pause", true)
	}); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	return nil
}

// Resume 继续播放。
func (p *MpvPlayer) Resume() error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接")
	}
	if err := callWithTimeout(func() error {
		return conn.Set("pause", false)
	}); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	return nil
}

// Seek 跳转到指定秒数（绝对位置）。
func (p *MpvPlayer) Seek(seconds float64) error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接")
	}
	if err := callWithTimeout(func() error {
		_, err := conn.Call("seek", seconds, "absolute")
		return err
	}); err != nil {
		return fmt.Errorf("seek: %w", err)
	}
	return nil
}

// SetLoop 设置单曲循环（mpv loop-file 无缝循环）。loop-file 是 per-file
// 属性：新 loadfile 自动重置为不循环，无需显式关闭；UI 层切歌/切模式时
// 仍显式 SetLoop(false) 保证一致性。循环播放不产生 end-file 事件，
// TrackEnded 自然不触发，进度回绕由 time-pos 属性推送驱动。
func (p *MpvPlayer) SetLoop(loop bool) error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接")
	}
	if err := callWithTimeout(func() error {
		return conn.Set("loop-file", loop)
	}); err != nil {
		return fmt.Errorf("set loop-file: %w", err)
	}
	return nil
}

// SetVolume 设置 mpv 音量（0-100，越界钳制）。
// 与 Play/Pause/Resume/Seek 一致：断开时自动重连（见 ensureConnected）。
func (p *MpvPlayer) SetVolume(percent float64) error {
	if err := p.ensureConnected(); err != nil {
		return err
	}
	conn := p.currentConn()
	if conn == nil {
		return errors.New("mpv 未连接")
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	if err := callWithTimeout(func() error {
		return conn.Set("volume", percent)
	}); err != nil {
		return fmt.Errorf("set volume: %w", err)
	}
	return nil
}

// Volume 读取 mpv 音量（0-100）。
// 与 Play/Pause/Resume/Seek 一致：断开时自动重连（见 ensureConnected）。
func (p *MpvPlayer) Volume() (float64, error) {
	if err := p.ensureConnected(); err != nil {
		return 0, err
	}
	conn := p.currentConn()
	if conn == nil {
		return 0, errors.New("mpv 未连接")
	}
	var v interface{}
	var err error
	if err = callWithTimeout(func() error {
		v, err = conn.Get("volume")
		return err
	}); err != nil {
		return 0, fmt.Errorf("get volume: %w", err)
	}
	f, ok := toFloat64(v)
	if !ok {
		return 0, errors.New("mpv volume 属性返回非数值")
	}
	return f, nil
}

// Events 返回播放器事件流。
func (p *MpvPlayer) Events() <-chan Event {
	return p.events
}

// Subscribe 返回一个广播事件通道与退订函数。与 Events() 不同，Subscribe
// 可被多个消费者同时使用（如 MPRIS 服务）；每个订阅者独立接收全部事件，
// 缓冲满时丢弃（与 emit 语义一致）。退订后不再收到事件。
func (p *MpvPlayer) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	if p.subs == nil {
		p.subs = make(map[chan Event]struct{})
	}
	p.subs[ch] = struct{}{}
	return ch, func() {
		p.subsMu.Lock()
		defer p.subsMu.Unlock()
		delete(p.subs, ch)
	}
}

// emit 非阻塞广播事件：主通道（Events()）+ 所有订阅者；缓冲满时丢弃
// （避免阻塞 mpv 事件读取）。
func (p *MpvPlayer) emit(ev Event) {
	select {
	case p.events <- ev:
	default:
	}
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for ch := range p.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Close 退出 mpv 并清理 socket 文件；可重复调用。
// 注意：mpv 对 quit 命令可能不回复就退出，Call 可能阻塞，
// 因此放到 goroutine 里加超时，超时后直接杀掉进程。
// mpvipc 关闭连接时不会通知阻塞中的请求（waitingRequests），超时后该
// goroutine 可能永久阻塞 —— 每次 Close 泄漏一个 goroutine；v1 中 Close
// 仅在退出时调用一次，可接受。
func (p *MpvPlayer) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	p.stateMu.Lock()
	conn := p.conn
	p.stateMu.Unlock()
	if conn != nil {
		done := make(chan struct{})
		go func() {
			_, _ = conn.Call("quit")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
		_ = conn.Close()
	}
	_ = p.killProcess()
	p.clearProcess()
	_ = os.Remove(p.socketPath)
	p.stateMu.Lock()
	confPath := p.ytdlpConfPath
	p.ytdlpConfPath = ""
	p.stateMu.Unlock()
	if confPath != "" {
		os.Remove(confPath) // 删除临时配置文件，失败忽略（TempDir 残留无害）
	}
	return nil
}

// killProcess 杀掉 mpv 进程并回收 Wait goroutine（非阻塞）。
func (p *MpvPlayer) killProcess() error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.killProcessLocked()
}

// killProcessLocked 调用方须持有 stateMu。
func (p *MpvPlayer) killProcessLocked() error {
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

// clearProcess 清空进程与连接字段（stateMu 内）。
// 前置：已调用 killProcess 回收进程；本函数只置 nil 字段。
func (p *MpvPlayer) clearProcess() {
	p.stateMu.Lock()
	p.cmd = nil
	p.waitCh = nil
	p.conn = nil
	p.stateMu.Unlock()
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

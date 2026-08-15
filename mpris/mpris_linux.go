//go:build linux

// Package mpris 实现 MPRIS 2.2 D-Bus 媒体控制协议（仅 Linux 编译）。
//
// 数据流双向：播放器事件（player 广播订阅）→ MPRIS 属性与 Seeked 信号；
// D-Bus 方法调用 → 转调 Player 接口。连接/注册失败仅告警，绝不影响主功能。
package mpris

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"music-tui/model"
	"music-tui/player"
	"music-tui/queue"
)

// 服务名与对象路径（MPRIS 规范固定值）。
const (
	serviceName = "org.mpris.MediaPlayer2.music-tui"
	objectPath  = "/org/mpris/MediaPlayer2"
)

// 两个 MPRIS 接口名。
const (
	ifaceRoot   = "org.mpris.MediaPlayer2"
	ifacePlayer = "org.mpris.MediaPlayer2.Player"
)

// seekedThreshold 进度跳变超过该阈值（秒）才发 Seeked 信号。
const seekedThreshold = 2.0

// trackIDPrefix 是 mpris:trackid 对象路径前缀。
const trackIDPrefix = "/org/mpris/MediaPlayer2/TrackList/"

// trackIDPath 把曲目 ID 编码为合法 D-Bus 对象路径段。YouTube ID 可含 '-'，
// 而对象路径元素只允许 [A-Za-z0-9_]：godbus 封送非法路径直接 panic，会把
// 整个应用带崩（真实播放中实测复现）。hex 编码保证唯一、可逆、全合法字符。
// 前置条件：id 非空（UI 侧曲目 ID 恒非空；空 ID 会生成带尾部斜杠的非法路径）。
func trackIDPath(id string) dbus.ObjectPath {
	return dbus.ObjectPath(trackIDPrefix + hex.EncodeToString([]byte(id)))
}

// offsetUs 是 Seek 的相对偏移量（微秒）。用命名类型而非裸 int64，避免
// go vet stdmethods 将 Seek 误判为 io.Seeker 实现（D-Bus 线上签名仍为 x）。
type offsetUs int64

// playerLike 是 mpris 服务依赖的播放器能力子集：事件订阅 + 控制 + 音量。
// 与 mpris_unsupported.go 中定义保持一致（两文件互斥编译，接口仅用于
// 让 NewServer 在两平台保持同一签名）。
type playerLike interface {
	Subscribe() (<-chan player.Event, func())
	Play(url string) error
	Pause() error
	Resume() error
	Seek(seconds float64) error
	SetVolume(percent float64) error
	Volume() (float64, error)
}

// controller 是 mpris 服务依赖的队列控制能力：播放编排（Next/Previous）与
// 播放模式读写。由 ui 层实现（与首页 ,/. 键、s 键同一编排路径），
// main 组装时经 SetController 注入。与 mpris_unsupported.go 中定义保持一致
// （两文件互斥编译，接口仅用于 SetController 签名匹配）。
type controller interface {
	PlayNext() error     // 播放下一首（与 , 键同一编排）；queue.ErrEmpty = 无曲可播
	PlayPrevious() error // 播放上一首（与 . 键同一编排）；queue.ErrEmpty = 无曲可播
	SetMode(queue.Mode)  // 绝对模式切换（与 s 键同一路径），恒成功（SetLoop 失败仅 toast）
	Mode() queue.Mode    // 当前播放模式（并发安全）
	Len() int            // 队列长度（并发安全）
}

// bus 是 Server 依赖的最小总线能力（生产为 *dbus.Conn，测试注入 fake）。
type bus interface {
	Emit(path dbus.ObjectPath, name string, values ...any) error
	Close() error
}

// propsStore 是 Server 依赖的属性存储最小接口（生产为 *prop.Properties）。
type propsStore interface {
	SetMust(iface, name string, v any)
	GetMust(iface, name string) any
}

// Server 是 MPRIS D-Bus 服务端：播放器事件推送属性，D-Bus 方法转调播放器。
type Server struct {
	p    playerLike
	ctrl controller // 队列控制注入（main 经 SetController 注入；nil = 未注入）

	conn     bus
	props    propsStore
	closeCh  chan struct{}
	pumpDone chan struct{} // pump 退出信号：Close 等它关闭后才断开连接

	closed atomic.Bool // Close 幂等保护；Close 后 pump 可能短暂存活，字段不再置 nil

	mu    sync.Mutex
	track *model.Track // 当前/最后曲目（ui 通过 SetTrack 回调写入）

	lastPos float64 // 上次 ProgressEvent 位置（秒），仅 pump goroutine 访问
}

// NewServer 创建 MPRIS 服务端（未连接总线，需调用 Start）。
func NewServer(p playerLike) *Server {
	return &Server{p: p}
}

// Start 连接 session bus、注册服务名并导出对象。失败返回错误，
// 调用方应仅记录警告并继续（MPRIS 不影响播放器主功能）。
func (s *Server) Start() error {
	if s.closed.Load() {
		return errors.New("MPRIS 服务已关闭，不能重启")
	}
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return fmt.Errorf("连接 session bus: %w", err)
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return fmt.Errorf("session bus 认证失败: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return fmt.Errorf("session bus Hello 失败: %w", err)
	}
	reply, err := conn.RequestName(serviceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("请求服务名 %s: %w", serviceName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		_ = conn.Close()
		return fmt.Errorf("服务名 %s 已被其他进程占用", serviceName)
	}
	s.conn = conn

	props, err := prop.Export(conn, objectPath, s.propertyMap())
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("导出 MPRIS 属性: %w", err)
	}
	s.props = props
	if err := conn.Export(&rootHandler{s}, objectPath, ifaceRoot); err != nil {
		_ = conn.Close()
		return fmt.Errorf("导出根接口: %w", err)
	}
	if err := conn.Export(&playerHandler{s}, objectPath, ifacePlayer); err != nil {
		_ = conn.Close()
		return fmt.Errorf("导出 Player 接口: %w", err)
	}

	// 初始音量对齐 mpv 当前值（读取失败静默保持默认 1.0）。
	if v, err := s.p.Volume(); err == nil {
		s.props.SetMust(ifacePlayer, "Volume", clamp01(v/100))
	}

	s.closeCh = make(chan struct{})
	s.pumpDone = make(chan struct{})
	go s.pump()
	return nil
}

// Close 停止事件泵并断开总线；幂等，可重复调用。
// 顺序很关键：先关 closeCh 并等 pump 完全退出（<-pumpDone），之后才关连接。
// 原因：PlaybackStatus/Metadata/Volume 是 EmitTrue 属性，SetMust 会经
// emitChange 调 conn.Emit；若先关连接，pump 存活期内处理残留事件时 Emit
// 返回 ErrClosed（dbus: connection closed by user），SetMust 直接 panic，
// 还会跳过 main.go 的 defer 清理（mpv 残留）。等 pump 退出后泵内不会再碰
// conn，此时关连接才是安全的（真实总线复现过该 panic）。pump 因事件通道
// 提前关闭而退出时 pumpDone 已关，等待立即返回；Start 未完成（closeCh 为
// nil，pump 未启动）时跳过等待。字段保持不置 nil。
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.closeCh != nil {
		close(s.closeCh)
		<-s.pumpDone // 等 pump 完全退出，杜绝存活期处理事件
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return nil
}

// SetTrack 由 ui 在开始播放新曲目时回调（nil 表示当前无曲目，用于播放
// 失败清理缓存）。MPRIS Metadata 在曲目结束后保留最后曲目（见 pump）。
func (s *Server) SetTrack(t *model.Track) {
	s.mu.Lock()
	s.track = t
	s.mu.Unlock()
}

// SetController 注入队列控制器并初始化队列相关属性（LoopStatus/Shuffle/
// CanGoNext/CanGoPrevious 按当前模式与队列长度投影）。幂等，可重复调用；
// Start 前后均可调用（Start 前调用时属性存储未建，同步延后到 Start 后首次
// 事件/操作）。未注入（nil）时 Next/Previous 返回 NotSupported、写回调 Failed。
func (s *Server) SetController(ctrl controller) {
	s.ctrl = ctrl
	if ctrl == nil || s.props == nil {
		return
	}
	s.syncMode(ctrl.Mode())
	s.refreshNav()
}

// SyncMode 由 ui 在播放模式变更后调用：同步 LoopStatus/Shuffle 属性
// （EmitTrue → PropertiesChanged 广播）。与 controller.SetMode 配合完成
// 双向同步：D-Bus 写 → SetMode → ui 切换 → SyncMode 回写投影。
func (s *Server) SyncMode(m queue.Mode) { s.syncMode(m) }

func (s *Server) syncMode(m queue.Mode) {
	if s.props == nil {
		return
	}
	s.props.SetMust(ifacePlayer, "LoopStatus", loopStatusFor(m))
	s.props.SetMust(ifacePlayer, "Shuffle", shuffleFor(m))
}

// refreshNav 按队列长度刷新 CanGoNext/CanGoPrevious（Len>1 才可跳转；
// 单曲/空队列均不可）。调用时机：SetController、每次播放事件后、
// 每次 Next/Previous 转调后。
func (s *Server) refreshNav() {
	if s.props == nil || s.ctrl == nil {
		return
	}
	can := s.ctrl.Len() > 1
	s.props.SetMust(ifacePlayer, "CanGoNext", can)
	s.props.SetMust(ifacePlayer, "CanGoPrevious", can)
}

// pump 订阅播放器事件并同步到 MPRIS 属性；Close 或事件通道关闭时退出。
// defer 顺序：先注册 unsub、后注册 close(pumpDone)，退出时后进先出——
// 先关 pumpDone 信号（Close 据此得知 pump 已不再触碰 conn），再执行 unsub。
func (s *Server) pump() {
	events, unsub := s.p.Subscribe()
	defer unsub()
	defer close(s.pumpDone)
	for {
		select {
		case <-s.closeCh:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.handleEvent(ev)
		}
	}
}

// handleEvent 把播放器事件映射为 MPRIS 属性更新与 Seeked 信号。
func (s *Server) handleEvent(ev player.Event) {
	switch e := ev.(type) {
	case player.ProgressEvent:
		posUs := int64(e.Position * 1e6)
		s.props.SetMust(ifacePlayer, "Position", posUs)
		if shouldEmitSeeked(s.lastPos, e.Position) {
			_ = s.conn.Emit(objectPath, ifacePlayer+".Seeked", posUs)
		}
		s.lastPos = e.Position
	case player.StateEvent:
		s.props.SetMust(ifacePlayer, "PlaybackStatus", playbackStatus(e.Playing))
	case player.TrackStartedEvent:
		// 新曲目：位置归零并重置跳变基准，避免首帧误报 Seeked。
		s.lastPos = 0
		s.props.SetMust(ifacePlayer, "Position", int64(0))
		s.props.SetMust(ifacePlayer, "PlaybackStatus", "Playing")
		s.mu.Lock()
		t := s.track
		s.mu.Unlock()
		s.props.SetMust(ifacePlayer, "Metadata", metadataFor(t))
	case player.TrackEndedEvent:
		// 按 MPRIS 惯例保留最后曲目的 Metadata，仅状态置 Stopped。
		s.props.SetMust(ifacePlayer, "PlaybackStatus", "Stopped")
	case player.ErrorEvent:
		s.props.SetMust(ifacePlayer, "PlaybackStatus", "Stopped")
	}
	// 队列长度可能随播放推进变化：每次事件后刷新可跳转状态
	s.refreshNav()
}

// propertyMap 定义两个 MPRIS 接口的全部属性（godbus/prop 新 API）。
func (s *Server) propertyMap() prop.Map {
	return prop.Map{
		ifaceRoot: {
			"CanQuit":             {Value: false, Emit: prop.EmitConst},
			"CanRaise":            {Value: false, Emit: prop.EmitConst},
			"HasTrackList":        {Value: false, Emit: prop.EmitConst},
			"Identity":            {Value: "music-tui", Emit: prop.EmitConst},
			"DesktopEntry":        {Value: "", Emit: prop.EmitConst},
			"SupportedUriSchemes": {Value: []string{"http", "https"}, Emit: prop.EmitConst},
			"SupportedMimeTypes":  {Value: []string{}, Emit: prop.EmitConst},
		},
		ifacePlayer: {
			"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
			"LoopStatus": {Value: "Playlist", Writable: true, Emit: prop.EmitTrue,
				Callback: s.loopStatusCallback},
			"Rate":     {Value: 1.0, Emit: prop.EmitConst},
			"Shuffle":  {Value: false, Writable: true, Emit: prop.EmitTrue, Callback: s.shuffleCallback},
			"Metadata": {Value: map[string]dbus.Variant{}, Emit: prop.EmitTrue},
			"Volume": {Value: 1.0, Writable: true, Emit: prop.EmitTrue,
				Callback: s.volumeCallback},
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"MinimumRate":   {Value: 1.0, Emit: prop.EmitConst},
			"MaximumRate":   {Value: 1.0, Emit: prop.EmitConst},
			"CanGoNext":     {Value: false, Emit: prop.EmitTrue},
			"CanGoPrevious": {Value: false, Emit: prop.EmitTrue},
			"CanPlay":       {Value: true, Emit: prop.EmitConst},
			"CanPause":      {Value: true, Emit: prop.EmitConst},
			"CanSeek":       {Value: true, Emit: prop.EmitConst},
			"CanControl":    {Value: true, Emit: prop.EmitConst},
		},
	}
}

// volumeCallback 处理客户端对 Volume 的写入：校验 0-1 并转调 mpv（0-100）。
// 注意：回调在 Properties.Set 持锁期间执行，不得再读取本服务的 props。
func (s *Server) volumeCallback(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(float64)
	if !ok || v < 0 || v > 1 {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if err := s.p.SetVolume(v * 100); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// loopStatusCallback 处理客户端对 LoopStatus 的写入：校验枚举值并转调
// 控制器切换播放模式。注意：回调在 Properties.Set 持锁期间执行，不得
// 再读取本服务的 props（投影回写由 ui 经 SyncMode 完成）。
func (s *Server) loopStatusCallback(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(string)
	if !ok || (v != "None" && v != "Track" && v != "Playlist") {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if s.ctrl == nil {
		return dbus.MakeFailedError(errors.New("MPRIS 控制器未注入"))
	}
	s.ctrl.SetMode(modeForLoopStatus(v, s.ctrl.Mode()))
	return nil
}

// shuffleCallback 处理客户端对 Shuffle 的写入：校验布尔并转调控制器。
// 锁内约束同 loopStatusCallback。
func (s *Server) shuffleCallback(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(bool)
	if !ok {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if s.ctrl == nil {
		return dbus.MakeFailedError(errors.New("MPRIS 控制器未注入"))
	}
	s.ctrl.SetMode(modeForShuffle(v, s.ctrl.Mode()))
	return nil
}

// rootHandler 实现 org.mpris.MediaPlayer2 根接口方法。
type rootHandler struct{ s *Server }

// Raise 无窗口可提升（CanRaise=false），no-op。
func (h *rootHandler) Raise() *dbus.Error { return nil }

// Quit 退出由应用自身管理（CanQuit=false），no-op。
func (h *rootHandler) Quit() *dbus.Error { return nil }

// playerHandler 实现 org.mpris.MediaPlayer2.Player 接口方法。
type playerHandler struct{ s *Server }

// Play 开始/继续播放。
func (h *playerHandler) Play() *dbus.Error {
	if err := h.s.p.Resume(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// Pause 暂停播放。
func (h *playerHandler) Pause() *dbus.Error {
	if err := h.s.p.Pause(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// PlayPause 按当前状态切换：播放中→暂停，否则→继续。
func (h *playerHandler) PlayPause() *dbus.Error {
	if h.s.props.GetMust(ifacePlayer, "PlaybackStatus") == "Playing" {
		return h.Pause()
	}
	return h.Play()
}

// Stop 暂停并回到开头（第一版无队列，最接近停止语义）。
func (h *playerHandler) Stop() *dbus.Error {
	if err := h.s.p.Pause(); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := h.s.p.Seek(0); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// Seek 相对跳转；offset 单位微秒（可为负），目标位置截断到 >=0。
func (h *playerHandler) Seek(offset offsetUs) *dbus.Error {
	posUs, ok := h.s.props.GetMust(ifacePlayer, "Position").(int64)
	if !ok {
		posUs = 0
	}
	target := float64(posUs+int64(offset)) / 1e6
	if target < 0 {
		target = 0
	}
	if err := h.s.p.Seek(target); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// SetPosition 绝对跳转；无曲目或 trackId 与当前曲目不匹配时返回 InvalidArgs。
// position 单位微秒。注意：无曲目时必须显式拦截（currentTrackID 返回空路径，
// 客户端传空 ObjectPath 会绕过 trackId 校验，对未加载文件的 mpv 发起 seek，
// 返回 Failed 而非 InvalidArgs）。
func (h *playerHandler) SetPosition(trackId dbus.ObjectPath, position int64) *dbus.Error {
	if !h.s.hasTrack() || trackId != h.s.currentTrackID() {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	if err := h.s.p.Seek(float64(position) / 1e6); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

// Next 转调队列控制器播放下一首（与首页 , 键同一编排路径）；
// 空队列返回 NotSupported。
func (h *playerHandler) Next() *dbus.Error {
	if h.s.ctrl == nil {
		return notSupported()
	}
	if err := h.s.ctrl.PlayNext(); err != nil {
		if errors.Is(err, queue.ErrEmpty) {
			return notSupported()
		}
		return dbus.MakeFailedError(err)
	}
	h.s.refreshNav()
	return nil
}

// Previous 转调队列控制器播放上一首（与首页 . 键同一编排路径）；
// 空队列返回 NotSupported。
func (h *playerHandler) Previous() *dbus.Error {
	if h.s.ctrl == nil {
		return notSupported()
	}
	if err := h.s.ctrl.PlayPrevious(); err != nil {
		if errors.Is(err, queue.ErrEmpty) {
			return notSupported()
		}
		return dbus.MakeFailedError(err)
	}
	h.s.refreshNav()
	return nil
}

// OpenUri 不支持外部 URI 播放，返回 NotSupported。
func (h *playerHandler) OpenUri(uri string) *dbus.Error { return notSupported() }

// notSupported 构造 NotSupported 错误。
func notSupported() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.NotSupported", nil)
}

// hasTrack 返回当前是否有已加载曲目（mutex 保护读）。
func (s *Server) hasTrack() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.track != nil
}

// currentTrackID 返回当前曲目的 mpris trackid；无曲目时返回空对象路径。
func (s *Server) currentTrackID() dbus.ObjectPath {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.track == nil {
		return ""
	}
	return trackIDPath(s.track.ID)
}

// ---- Server 便捷转发（供单测与调用方直接调用；D-Bus 导出仍走 handler 包装） ----

// Play 转发到 playerHandler.Play。
func (s *Server) Play() *dbus.Error { return (&playerHandler{s}).Play() }

// Pause 转发到 playerHandler.Pause。
func (s *Server) Pause() *dbus.Error { return (&playerHandler{s}).Pause() }

// PlayPause 转发到 playerHandler.PlayPause。
func (s *Server) PlayPause() *dbus.Error { return (&playerHandler{s}).PlayPause() }

// Stop 转发到 playerHandler.Stop。
func (s *Server) Stop() *dbus.Error { return (&playerHandler{s}).Stop() }

// Seek 转发到 playerHandler.Seek（offset 单位微秒）。
func (s *Server) Seek(offset offsetUs) *dbus.Error { return (&playerHandler{s}).Seek(offset) }

// SetPosition 转发到 playerHandler.SetPosition。
func (s *Server) SetPosition(trackId dbus.ObjectPath, position int64) *dbus.Error {
	return (&playerHandler{s}).SetPosition(trackId, position)
}

// Next 转发到 playerHandler.Next。
func (s *Server) Next() *dbus.Error { return (&playerHandler{s}).Next() }

// Previous 转发到 playerHandler.Previous。
func (s *Server) Previous() *dbus.Error { return (&playerHandler{s}).Previous() }

// OpenUri 转发到 playerHandler.OpenUri。
func (s *Server) OpenUri(uri string) *dbus.Error { return (&playerHandler{s}).OpenUri(uri) }

// Raise 转发到 rootHandler.Raise。
func (s *Server) Raise() *dbus.Error { return (&rootHandler{s}).Raise() }

// Quit 转发到 rootHandler.Quit。
func (s *Server) Quit() *dbus.Error { return (&rootHandler{s}).Quit() }

// ---- 纯函数（单测覆盖） ----

// playbackStatus 映射播放中状态到 MPRIS PlaybackStatus。
func playbackStatus(playing bool) string {
	if playing {
		return "Playing"
	}
	return "Paused"
}

// shouldEmitSeeked 判定进度跳变（正/负）是否超过阈值（秒），决定是否发 Seeked。
func shouldEmitSeeked(last, current float64) bool {
	d := current - last
	return d > seekedThreshold || d < -seekedThreshold
}

// metadataFor 构建 MPRIS Metadata 字典；nil 曲目返回空字典。
func metadataFor(t *model.Track) map[string]dbus.Variant {
	if t == nil {
		return map[string]dbus.Variant{}
	}
	m := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(trackIDPath(t.ID)),
		"mpris:length":  dbus.MakeVariant(int64(t.Duration * 1e6)),
		"xesam:title":   dbus.MakeVariant(t.Title),
		"xesam:artist":  dbus.MakeVariant(artistList(t.Artist)),
	}
	if t.CoverURL != "" {
		m["mpris:artUrl"] = dbus.MakeVariant(t.CoverURL)
	}
	return m
}

// artistList 把单歌手字段转为 MPRIS 艺术家数组（空值给空数组）。
func artistList(artist string) []string {
	if artist == "" {
		return []string{}
	}
	return []string{artist}
}

// clamp01 把音量限制在 [0,1]。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// loopStatusFor 映射播放模式到 MPRIS LoopStatus：单曲循环=Track；
// 列表循环与随机（播完均回绕到队首，语义即列表循环）=Playlist。
func loopStatusFor(m queue.Mode) string {
	if m == queue.RepeatOne {
		return "Track"
	}
	return "Playlist"
}

// shuffleFor 映射播放模式到 MPRIS Shuffle：仅随机模式为 true。
func shuffleFor(m queue.Mode) bool { return m == queue.Shuffle }

// modeForLoopStatus 映射 MPRIS LoopStatus 写入到播放模式。
// Playlist 对 Shuffle 模式保持（随机模式下播完回绕，投影已是 Playlist，
// 写 Playlist 不应关闭随机）；None 归入 Sequential（设计决策：无第四态）。
func modeForLoopStatus(s string, cur queue.Mode) queue.Mode {
	switch s {
	case "Track":
		return queue.RepeatOne
	case "None":
		return queue.Sequential
	default: // "Playlist"
		if cur == queue.Shuffle {
			return cur
		}
		return queue.Sequential
	}
}

// modeForShuffle 映射 MPRIS Shuffle 写入到播放模式：true → 随机模式；
// false 仅当当前是随机模式时切回列表循环（关闭随机不动其他循环设置）。
func modeForShuffle(b bool, cur queue.Mode) queue.Mode {
	if b {
		return queue.Shuffle
	}
	if cur == queue.Shuffle {
		return queue.Sequential
	}
	return cur
}

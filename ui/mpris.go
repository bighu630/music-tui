package ui

import (
	tea "charm.land/bubbletea/v2"

	"music-tui/queue"
)

// mprisReqKind 区分 MPRIS 控制器请求类型。
type mprisReqKind int

const (
	reqNext mprisReqKind = iota
	reqPrev
	reqSetMode
)

// mprisReq 一条 MPRIS 控制器请求：由 MPRIS D-Bus goroutine 经 dispatch
// 投递，bubbletea 循环消费执行后回写 reply（缓冲 1，D-Bus 侧同步等待）。
type mprisReq struct {
	kind  mprisReqKind
	mode  queue.Mode
	reply chan error
}

// mprisReqMsg 把 MPRIS 控制器请求包装为 bubbletea 消息。
type mprisReqMsg struct{ req mprisReq }

// MprisController 实现 mpris 包的 controller 接口（方法签名一致即隐式
// 满足，ui 不 import mpris；接口匹配由 main 组装处编译期保证）。
// PlayNext/PlayPrevious/SetMode 经 channel 投递到 bubbletea 消息循环，
// 与首页 ,/. 键、s 键走完全相同的编排路径（线程安全 + 行为一致）。
type MprisController struct {
	reqs chan mprisReq
	q    *queue.Queue

	// onModeChanged 模式变更通知（main 注入 mpris.Server.SyncMode）；
	// 启动期单次写入、之后仅 bubbletea goroutine 读，无需加锁。
	onModeChanged func(queue.Mode)
}

// SetModeSink 注册模式变更通知回调（main 注入 mprisSrv.SyncMode）。
func (c *MprisController) SetModeSink(fn func(queue.Mode)) { c.onModeChanged = fn }

// MprisController 返回 MPRIS 控制器桥指针（main 组装注入用：SetController +
// SetModeSink 修改指针目标，Model 值拷贝共享同一桥）。
func (m Model) MprisController() *MprisController { return m.mprisCtrl }

// PlayNext 请求播放下一首；队列为空返回 queue.ErrEmpty（MPRIS 映射 NotSupported）。
func (c *MprisController) PlayNext() error {
	if c.q.Len() == 0 {
		return queue.ErrEmpty
	}
	return c.dispatch(mprisReq{kind: reqNext})
}

// PlayPrevious 请求播放上一首；队列为空返回 queue.ErrEmpty。
func (c *MprisController) PlayPrevious() error {
	if c.q.Len() == 0 {
		return queue.ErrEmpty
	}
	return c.dispatch(mprisReq{kind: reqPrev})
}

// SetMode 请求切换播放模式（恒成功：SetLoop 失败与 s 键一致仅 toast）。
func (c *MprisController) SetMode(m queue.Mode) { _ = c.dispatch(mprisReq{kind: reqSetMode, mode: m}) }

// Mode 返回当前播放模式（queue 并发安全，D-Bus goroutine 直接读）。
func (c *MprisController) Mode() queue.Mode { return c.q.Mode() }

// Len 返回队列长度（queue 并发安全）。
func (c *MprisController) Len() int { return c.q.Len() }

// dispatch 投递请求并同步等待执行结果（bubbletea 消费后回包）。
func (c *MprisController) dispatch(req mprisReq) error {
	reply := make(chan error, 1)
	req.reply = reply
	c.reqs <- req
	return <-reply
}

// subscribeMprisReqs 阻塞监听 MPRIS 控制器请求并注入 bubbletea。
// 与 waitForPlayerEvents 同模式：每条请求处理后需重新订阅（cmd 链不丢）。
func subscribeMprisReqs(ch chan mprisReq) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return mprisReqMsg{req: req}
	}
}

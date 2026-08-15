// Package preload 实现“预加载”调度：队列播放时自动预下载“即将播放的下一首”
// 到缓存，切歌时直接命中本地缓存秒切（YouTube 403 风控下连播不卡顿）。
//
// 职责与边界：
//   - 只负责“何时预下载哪首”的调度决策，不感知下载结果——失败静默
//     （cache 层已有日志策略）；下载产物最终归属 cache 的 LRU 管理（超限淘汰）。
//   - 串行单在途：同一时刻至多一个预下载在途；目标随时可被 UI 协程更新，
//     在途下载不取消（yt-dlp 子进程无干净取消链路，让其完成——产物仍是
//     有效缓存），完成后自动处理最新目标。
//   - 依赖注入：CacheClient 抽象缓存层（cache.Manager 满足）；cache 为 nil
//     时所有方法安全 no-op（未配置缓存场景）。
//
// 并发模型：目标槽位（target）由互斥锁保护；单 worker goroutine 串行消费；
// wake 通道（缓冲 1）唤醒空闲 worker，stop 通道终止 worker。
// UI 协程可随时调用 SetTarget/Target/Stop（全部线程安全）。
package preload

import (
	"sync"

	"music-tui/model"
)

// CacheClient 是调度器对缓存层的依赖抽象：CacheAsync 启动后台预下载并返回
// 完成信号（nil = no-op：已缓存/禁用/同 ID 在途，没有下载发生）。
// cache.Manager 满足此接口。
type CacheClient interface {
	CacheAsync(track model.Track) <-chan struct{}
}

// Scheduler 预加载调度器：持有“最新目标”槽位，后台单 worker 串行消费。
//
// 内部结构：
//   - target：最新目标（mutex 保护）；nil = 无目标，worker 阻塞等 wake/stop。
//   - lastProcessed：去重状态——已启动下载的目标（按 ID 判同，非指针比较：
//     UI 层每次传新指针，指针比较会让去重形同虚设）。同 ID 目标不重复调用
//     CacheAsync（见 run 内注释）；SetTarget(nil) 清空目标的同时重置该状态
//     （之后重设同 ID 曲目会重新触发下载，失败后可重试）。
//   - worker goroutine：循环 取最新目标 → CacheAsync → 串行等完成。
//   - wake：缓冲 1 的唤醒信号（见 run 内注释：为何缓冲 1 防丢失）。
//   - stop/done：Stop 终止 worker 并等待其退出；stopOnce 保证 stop 只 close
//     一次（Stop 幂等）。
//
// 线程安全：SetTarget/Target/Stop 均可随时从 UI 协程调用；worker 只在
// 互斥锁保护下读写目标槽位。注意：SetTarget 传入的 *model.Track 由调用方
// 持有，调度器按指针消费（CacheAsync 时解引用拷贝）——调用方须保证
// 目标曲目内容不被并发修改（替换指针而非改内容，UI 层即此语义）。
type Scheduler struct {
	mu     sync.Mutex
	cache  CacheClient
	target *model.Track // 最新目标指针；nil = 无目标（worker 阻塞等 wake）
	last   *model.Track // 去重状态：已启动下载的目标（按 ID 判同；SetTarget(nil) 重置）

	wake chan struct{} // worker 唤醒信号（缓冲 1：见 run 注释）
	stop chan struct{} // 停止信号（只 close 一次）
	done chan struct{} // worker 退出信号（Stop 等待用）

	stopOnce sync.Once // 保证 stop 只 close 一次（Stop 幂等）
}

// New 创建并启动调度器（worker goroutine 立即开始循环）。
// cache 可为 nil（缓存未配置）：SetTarget 变 no-op，调度器整体闲置。
func New(c CacheClient) *Scheduler {
	s := &Scheduler{
		cache: c,
		wake:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

// SetTarget 更新预加载目标（最新目标胜出：worker 每次只取当前槽位）。
// nil = 停止/清空目标，同时重置去重状态——之后重设同一曲目（同 ID）会
// 重新触发下载（失败后可重试：先清空再重设 = 显式重试）。
// 无 nil 间隔的同 ID 重设（含下载失败后重设）不重新触发——与"失败静默"
// 一致，不自动重试。
// 在途下载不取消（让其完成，产物仍是有效缓存）。
// 线程安全：UI 协程随时可调；cache 为 nil 时 no-op（不 panic）。
func (s *Scheduler) SetTarget(t *model.Track) {
	if s.cache == nil {
		return // 缓存未配置：预加载整体 no-op（New(nil) 安全）
	}
	s.mu.Lock()
	if t == nil {
		// 清空目标 = 显式重置去重状态：worker 侧对同 ID 的"已处理"记忆失效，
		// 之后重设同一曲目会重新走 CacheAsync（失败后可重试的语义）。
		s.last = nil
	}
	s.target = t
	s.mu.Unlock()
	// 非阻塞唤醒：worker 空闲时被叫醒取最新目标；忙（在途等待/处理中）时
	// 信号丢弃也没关系——worker 每个循环都会重新读目标槽位，最终目标不会丢。
	// 缓冲 1：防止“唤醒恰发生在 worker 即将阻塞之前”的竞态丢信号（见 run）。
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Target 返回当前最新目标指针（nil = 无目标）。测试断言用。
func (s *Scheduler) Target() *model.Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

// Stop 停止 worker 并等待其退出；幂等（可多次调用）。
// 在途下载不等待：worker 直接退出（cache 层下载 goroutine 自行结束并
// 关闭完成信号，无人等待也无副作用）。测试/程序退出用。
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done // 等 worker 退出（stop 关闭后 run 必在下一个 select 点退出）
	})
}

// run 是后台 worker 主循环：串行消费目标槽位。
//
// 循环语义：
//  1. 取最新目标（互斥锁内），按 ID 与 last（去重状态）判同：同 ID 视为
//     已处理（在途或已完成），不再调用 CacheAsync——完成后回环会拿到同一
//     目标，重复调用对已缓存条目是纯浪费（真实 cache 返回 nil 后还得等
//     wake，等价于直接等）；也符合"失败静默"（下载失败后同 ID 重设不自动
//     重试，需 SetTarget(nil) 显式重试）。判同通过则锁内原子"读取 + 认领"
//     （登记 last）：SetTarget(nil) 恰在窗口期到达时，worker 不会认领一个
//     已被清空的目标。nil → 阻塞等 wake/stop。
//  2. 非 nil → cache.CacheAsync(*t)：
//     - done == nil（已缓存/禁用/同 ID 在途）→ 缓存层无事可做：等 wake/stop。
//       不能回环重试——目标未变时 CacheAsync 会一直返回 nil，空转烧 CPU；
//       等 wake 即“不死等”：新目标一到立即处理。
//     - done != nil → 阻塞等 done 关闭（串行：同一时刻至多一个预下载在途）。
//  3. 在途期间 SetTarget 只更新槽位，不打断等待；done 关闭后回环自动处理
//     最新目标（旧下载不取消——产物仍是有效缓存）。
//  4. stop 关闭：任一等待点立即退出（Stop 期间在途 → 直接退出不等待）。
//
// wake 缓冲 1 的必要性：SetTarget 可能恰在 worker“读完 nil 目标、尚未进入
// select 阻塞”的窗口期发送唤醒——无缓冲通道此时无人接收，信号丢失，
// worker 永久睡眠。缓冲 1 保证该唤醒必然暂存；若缓冲已满（存在更早的
// 待处理唤醒），丢弃也无妨——worker 消费任一唤醒后都会重读目标槽位。
func (s *Scheduler) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		t := s.target
		if t != nil && s.last != nil && t.ID == s.last.ID {
			t = nil // 同 ID 已处理（在途或已完成）：与“无目标”同路径，等 wake/stop
		} else if t != nil {
			s.last = t // 认领并登记：本目标视为已启动下载（此后同 ID 去重）
		}
		s.mu.Unlock()
		if t == nil {
			select {
			case <-s.stop:
				return
			case <-s.wake:
			}
			continue
		}
		// 防御：cache 为 nil 时目标槽位不应被置位（SetTarget 已拦截），
		// 双保险避免 nil 接口解引用 panic。
		if s.cache == nil {
			return
		}
		// 尽力而为的停止检查（非阻塞）：此处与 Stop() 的 close(stop) 存在竞态
		// 窗口——检查通过后 Stop 才关闭 stop 的话，本次仍会启动一个下载。
		// 后果无害（至多多一个后台下载，产物是有效缓存，Stop 也不等它），
		// 故措辞为 best-effort：不严格保证"停止后不再有任何下载开始"。
		select {
		case <-s.stop:
			return
		default:
		}
		done := s.cache.CacheAsync(*t)
		if done == nil {
			// no-op（已缓存/禁用/同 ID 在途）：不会再有完成信号，
			// 等 wake/stop——新目标一到立即处理（“不死等”）。
			select {
			case <-s.stop:
				return
			case <-s.wake:
			}
			continue
		}
		select {
		case <-s.stop:
			return // Stop 期间在途：直接退出，不等待下载（cache 层自行结束）
		case <-done: // 串行等当前下载彻底结束（成功注册或失败耗尽）
		}
	}
}

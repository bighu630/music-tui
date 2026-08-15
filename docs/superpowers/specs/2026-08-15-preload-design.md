# 预加载（Preload）设计：自动预下载即将播放的下一首

日期：2026-08-15
状态：已确认（用户逐点确认 4 个决策点）

## 背景与目标

连播时切歌存在等待/卡顿/取流失败问题（YouTube 403 风控等）。目标：队列播放时自动预下载"即将播放的下一首"到缓存，切歌时直接命中本地缓存秒切。

与现有"缓存预热"（TrackStarted 时 CacheAsync 当前曲）互补：预热解决"当前曲已缓存"，预加载解决"下一首已缓存"。

## 用户确认的决策点

1. **触发时机**：当前曲开始播放（TrackStartedEvent）即预下载下一首。备选"接近结尾触发"弃用（省流量但复杂易错；LRU 上限 100 兜底浪费有限）。
2. **配置开关**：不加 `preload.enabled`（YAGNI）——`cache.enabled` 已能全局关下载，预加载自然变 no-op。
3. **目标变更时旧下载**：不取消、让其完成（yt-dlp 子进程无干净取消链路；产物仍是有效缓存，LRU 兜底）；新目标串行排队——**同时至多一个预下载在途**。
4. **失败静默**：复用 CacheAsync 的静默失败策略（仅日志、不影响播放）。

## 架构与组件

### 1. `queue` 包：`PeekNext()`（不推进状态）

```go
// PeekNext 返回 Next 将推进到的下一首但不改变状态；空队列返回 false。
// 与 Next 同语义：无当前曲目时返回队首；末尾回绕到队首。
func (q *Queue) PeekNext() (model.Track, bool)
```

单曲队列回绕返回自身——此时目标已缓存（TrackStarted 预热过），CacheAsync no-op，天然安全。

### 2. `cache` 包：`CacheAsync` 返回完成信号

```go
// 签名变更（现有调用方忽略返回值，零改动）：
func (m *Manager) CacheAsync(track model.Track) <-chan struct{}
```

- no-op（开关关/同 ID 在途/条目已存在）→ 返回 nil。
- 否则返回 channel：下载彻底结束（成功注册或预算耗尽失败）时关闭。
- `download()` 内部通过 defer close 完成信号传递。

### 3. 新 `preload` 包：调度器（核心）

```go
type CacheClient interface {
    CacheAsync(track model.Track) <-chan struct{}
}

type Scheduler struct { ... } // 互斥锁保护目标槽位 + 后台 worker 循环 + wake/stop channel

func New(c CacheClient) *Scheduler
func (s *Scheduler) SetTarget(t *model.Track) // nil=停止；与在途同 ID 由 CacheAsync no-op 去重；新 ID 记为目标
func (s *Scheduler) Stop()                    // 停止 worker（测试/退出用）
func (s *Scheduler) Target() *model.Track     // 当前最新目标（测试断言用）
```

worker 循环语义：
- 取最新目标；nil → 阻塞等 wake/stop。
- 非 nil → `done := cache.CacheAsync(*t)`；`done == nil`（已缓存/禁用/在途）→ 立即处理新目标，不死等；否则 `<-done` 串行等完成。
- 同一时刻至多一个下载在途（串行天然保证）。
- 失败静默：调度器不感知下载结果。

### 4. `ui/root.go` 集成：`refreshPreload()` 辅助

```go
// 门控：!ended && state.Track != nil && mode != RepeatOne 时预载 PeekNext()，否则 SetTarget(nil)
func (m Model) refreshPreload()
```

调用点（所有队列形态/播放状态变更处）：
- `TrackStartedEvent`（现有预热当前曲之后）
- `trackAppendMsg` / `queuePlayMsg` / `prevTrackMsg` / `nextTrackMsg` / `queueDeleteMsg` / `queueClearMsg`
- `cycleMode`（toggleModeMsg 与 queueModeMsg 共用）
- `plLoadMsg` / `startPlay`
- `stopAfterEnd`、`ErrorEvent` 收尾（ended=true → 清空目标）

RepeatOne 跳过：mpv 层无缝循环同曲，queue 指针照常推进——预载"下一首"是浪费（当前曲 TrackStarted 时已缓存）。

## 错误处理

- 下载失败：cache 层现有日志策略，调度器无感知，播放不受影响。
- 目标变更/清空：不取消在途下载，仅更新目标槽位。
- 并发：Scheduler 内部互斥锁 + 单 worker 串行；SetTarget 可随时从 UI 协程调用。

## 测试

- `queue`：PeekNext 空队列/无当前/中间/回绕/单曲。
- `cache`：done channel 语义（禁用/已缓存/在途 → nil；成功/失败耗尽 → 关闭）。
- `preload`：
  - fake client 单测：nil 目标不下载、触发下载、在途同 ID 去重、在途换目标串行、在途清空后不再下载、CacheAsync 返回 nil 不死等、Stop 退出。
  - 集成测试：真实 cache.Manager + 假 yt-dlp 脚本（复用 cache 包 fakeYtDlpBody 模式）→ SetTarget → 轮询 Lookup 命中 → 文件落盘 + 索引注册。
- `ui/root`：TrackStarted 后 `preloader.Target()` 断言为下一首；RepeatOne/清空/删除/ended 门控断言；现有测试不回归。

## Git 纪律

- 在 `.worktrees/preload`（分支 `feat/preload`）开发。
- 只 git add 自己负责的文件，不用 `git add -A`，commit 前 git status 检查。

## 验收标准

- 播放中自动预下载下一首；模式感知（RepeatOne 跳过）；失败静默不阻塞；队列变更联动（增删清/切模式/跳转）；去重防重复下载；`go build ./...`、`go vet ./...`、`go test ./... -race` 全绿；用户实测连播秒切、缓存目录出现预下载文件。

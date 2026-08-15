# MPRIS 队列控制设计（Next/Previous + LoopStatus/Shuffle）

日期：2026-08-15
状态：已确认（用户逐项确认：None 映射方案 A、属性映射表、边界行为）

## 1. 背景与目标

MPRIS 当前实现（设计文档第 13 章）：Next/Previous 返回 NotSupported、LoopStatus 固定 None（EmitConst）、Shuffle 固定 false（EmitConst）、CanGoNext/CanGoPrevious 固定 false——当时无播放队列。

现在已有 queue 包（三态 Mode：Sequential 列表循环 / Shuffle 随机 / RepeatOne 单曲循环，Next/Prev 均回绕）与 ui 层连播编排（TrackEnded→queue.Next→beginPlay）。

目标：MPRIS 支持 Next/Previous 操作 + LoopStatus/Shuffle 属性读写，与 UI 模式显示双向同步，测试全绿，playerctl 实测通过。

## 2. 已确认的设计决策

### 2.1 LoopStatus=None 映射（方案 A）
None → Sequential（列表循环）。不新增"顺序不循环"第四态——TUI 无该模式的 UI 入口需求，改动最小、风险低。语义差异（设 None 后播完仍回绕）在文档中注明。

### 2.2 属性映射表

读（模式 → MPRIS 投影）：

| 模式 | LoopStatus | Shuffle |
|---|---|---|
| Sequential | Playlist | false |
| RepeatOne | Track | false |
| Shuffle | Playlist | true |

写（MPRIS → 模式，单属性写均有确定结果；多属性同时写后写者生效）：

| 写入 | 行为 |
|---|---|
| Shuffle=true | 无条件切 Shuffle 模式 |
| Shuffle=false | 仅当当前是 Shuffle 模式时切回 Sequential；其余模式不变 |
| LoopStatus=Track | 无条件切 RepeatOne |
| LoopStatus=Playlist | 仅当当前是 RepeatOne 时切 Sequential；Shuffle/Sequential 保持不变 |
| LoopStatus=None | 切 Sequential |

### 2.3 边界行为
- 非空队列：MPRIS Next/Previous 转调与首页 `,`/`.` 键完全相同的编排路径（queue.Next/Prev + beginPlay），回绕行为一致
- 空队列：CanGoNext/CanGoPrevious=false；Next/Previous 返回 NotSupported
- 单曲队列：CanGoNext/CanGoPrevious=false（Len>1 才 true）；方法被调时仍回绕重播同曲（与 UI 键一致）
- CanGoNext/CanGoPrevious 是动态属性（EmitTrue），刷新时机：SetController 时、每次播放事件（TrackStarted/TrackEnded/Error）后、每次 Next/Previous 转调后。UI 侧纯队列变更（如搜索页 a 追加）不实时刷新，属性可能短暂过期，下次播放事件即修正（用户已接受）

## 3. 架构

### 3.1 分层与数据流

```
player 事件 ──► mpris.Server ──► D-Bus 属性/信号（PropertiesChanged/Seeked）
D-Bus 方法 ──► mpris.Server ──► controller 接口 ──► ui.Model（bubbletea 消息循环）
                                     ▲
ui 模式切换（cycleMode）──────────────┘（onModeChanged 回调 → SyncMode 投影同步）
```

- mpris 包依赖 queue 包（Mode 枚举、ErrEmpty），不依赖 ui 包
- ui 包实现 controller，**不 import mpris**（Go 结构类型隐式满足接口，main.go 组装处编译期检查）
- 播放编排（queue.Next + beginPlay）只能在 bubbletea 循环内执行（Model 状态单 goroutine 假设）；controller 的 PlayNext/PlayPrevious/SetMode 通过 channel 投递请求 + 同步等待 reply，保证线程安全且与 UI 键位行为完全一致

### 3.2 mpris 包新增（mpris/mpris_linux.go）

```go
// controller 是 mpris 服务依赖的队列控制能力；由 ui 实现，main 组装注入。
type controller interface {
    PlayNext() error      // 播放下一首编排（与 , 键同路径）；queue.ErrEmpty = 无曲可播
    PlayPrevious() error  // 播放上一首编排（与 . 键同路径）；queue.ErrEmpty = 无曲可播
    SetMode(queue.Mode)   // 绝对模式切换（与 s 键同路径），恒成功（SetLoop 失败仅 toast）
    Mode() queue.Mode     // 当前模式（同步读，queue 加锁后并发安全）
    Len() int             // 队列长度（同步读）
}
```

- `SetController(ctrl controller)`：注入后立即初始化 LoopStatus/Shuffle/CanGoNext/CanGoPrevious（读 ctrl.Mode()/ctrl.Len()）；幂等可重复调用；未注入（nil）时 Next/Previous 返回 NotSupported、LoopStatus/Shuffle 写回调返回 Failed
- `SyncMode(m queue.Mode)`：ui 模式变更后调用，投影更新 LoopStatus/Shuffle（SetMust + EmitTrue 广播 PropertiesChanged）
- `refreshNav()`：按 ctrl.Len()>1 更新 CanGoNext/CanGoPrevious；调用时机 = SetController + 每次 handleEvent 后 + 每次 Next/Previous 转调后
- 纯函数（单测覆盖）：`loopStatusFor(mode)`、`shuffleFor(mode)`、`modeForLoopStatus(s string, cur queue.Mode) queue.Mode`（含保持逻辑）、`modeForShuffle(b bool, cur queue.Mode) queue.Mode`（含保持逻辑）
- Next/Previous handler：转调 controller，`errors.Is(err, queue.ErrEmpty)` → NotSupported，其他错误 → Failed
- propertyMap 变更：LoopStatus 改 `Writable: true, Emit: EmitTrue` + Callback（校验值 ∈ {None, Track, Playlist} → ctrl.SetMode；回调在 prop 锁内执行，**不得碰本服务 props**，投影回写由 SyncMode 完成）；Shuffle 同（校验 bool）；CanGoNext/CanGoPrevious 改 `Emit: EmitTrue`
- 写回调流程：客户端 Set → Callback 校验+转调 ctrl.SetMode → ui 切换模式 → onModeChanged → SyncMode 投影覆盖（可能回写相同值，幂等广播，属正常回显）

### 3.3 ui 包新增（ui/mpris.go）

```go
type mprisReqKind int
const (
    reqNext mprisReqKind = iota
    reqPrev
    reqSetMode
)
type mprisReq struct {
    kind  mprisReqKind
    mode  queue.Mode
    reply chan error // 缓冲 1；PlayNext/PlayPrevious 回 ErrEmpty 或 nil；SetMode 恒 nil
}
type mprisReqMsg struct{ req mprisReq }

// MprisController 实现 mpris 包的 controller 接口（方法签名一致，隐式满足）。
// 不 import mpris：错误哨兵用 queue.ErrEmpty，接口匹配由 main 组装处编译期保证。
type MprisController struct {
    reqs chan mprisReq
    q    *queue.Queue
}
```

- Model 持有 `mpris *MprisController`（NewModel 内创建，chan 缓冲 16）
- Model.Init 返回订阅 cmd（仿 waitForPlayerEvents）：循环 `m.reqs <- req` → 发 `mprisReqMsg` 给自身
- Update 处理 `mprisReqMsg`：
  - reqNext → 同 nextTrackMsg 路径（queue.Next + retryCount 重置 + queueSkip 解除 + beginPlay + refreshPreload）；`queue.Next()` false → reply `queue.ErrEmpty`
  - reqPrev → 同 prevTrackMsg 路径；false → `queue.ErrEmpty`
  - reqSetMode → `applyMode(req.mode)`；reply nil
  - 注意：所有分支 reply 必须回写（阻塞在 D-Bus goroutine 等待）；处理完成后继续监听（cmd 链不丢）
- `applyMode(mode)`：从 cycleMode 重构出的绝对模式切换（SetMode + SetLoop(next==RepeatOne) + refreshPreload + syncQueueViews + 末尾 `notifyModeChanged()`）；cycleMode 改为算 next 后调 applyMode——UI 三态循环行为不变
- `notifyModeChanged()`：`if m.onModeChanged != nil { m.onModeChanged(m.queue.Mode()) }`
- Model 新增字段 `onModeChanged func(queue.Mode)` + setter `SetModeSink(fn)`（仿 onTrack 注入模式；不动 NewModel 签名，现有测试零破坏）
- `MprisController` 方法：
  - `PlayNext() error`：构造 req+reply，`m.reqs <- req`（阻塞 send），`<-reply`
  - `PlayPrevious() error`：同
  - `SetMode(m queue.Mode)`：`<-reply` 恒 nil
  - `Mode() queue.Mode`：`q.Mode()` 直接读（queue 加锁后并发安全）
  - `Len() int`：`q.Len()` 直接读

### 3.4 queue 包改造（queue/queue.go）

- 新增 `mu sync.RWMutex`；全部公开方法加锁（Add/InsertNext/Replace/ReplaceAll/Remove/Move/Clear/Next/PeekNext/Prev/SetMode/JumpTo/Snapshot/Restore/Current/Tracks/CurrentIndex/Mode/Len）——机械改动，行为不变；Tracks()/Snapshot() 的拷贝在锁内完成
- 新增哨兵 `var ErrEmpty = errors.New("queue: empty")`（controller 空队列判定，mpris 用 errors.Is 映射 NotSupported）
- queue_test 现有测试保持全绿（行为不变）；补并发读-写 -race 测试

### 3.5 main.go 组装

```go
mprisSrv := mpris.NewServer(mpv)
if err := mprisSrv.Start(); err != nil { ... } else { defer mprisSrv.Close() }
model := ui.NewModel(..., mprisSrv.SetTrack, ...)   // 签名不变
mprisSrv.SetController(model.MprisController())     // 编译期检查接口满足
```

### 3.6 mpris_unsupported.go 桩

- 同步定义 controller 接口（与 linux 版一致，互斥编译）；桩 NewServer 签名不变；新增 `SetController(ctrl controller)` no-op、`SyncMode(m queue.Mode)` no-op（保持 main.go 无平台分支）

## 4. 错误处理

| 场景 | 行为 |
|---|---|
| 空队列 Next/Previous | NotSupported（queue.ErrEmpty 映射） |
| controller 未注入时 Next/Previous | NotSupported |
| controller 未注入时写 LoopStatus/Shuffle | Failed |
| 非法 LoopStatus 值 / 非 bool Shuffle | InvalidArgs |
| 播放编排实际失败（取流等） | 走既有 beginPlay 失败路径（toast/重试），不阻塞 D-Bus 返回（编排已接受） |
| SetMode 中 mpv SetLoop 失败 | 模式仍切换，仅 toast（与 s 键行为一致），D-Bus 返回成功 |
| UI 退出后 D-Bus 调用 | main defer 顺序保证 mpris.Close 先于 model 销毁；reqs chan 阻塞 send 至多到 UI 循环停止，无悬挂泄漏 |

## 5. 测试策略（TDD）

- mpris 包（linux 构建）：
  - 映射纯函数单测：四模式投影（含 Shuffle 模式的 LoopStatus=Playlist）、四种写入映射（含保持逻辑：Playlist 对 Shuffle 保持、Shuffle=false 对 RepeatOne 保持等）
  - fakeController（记录调用 + 可控 Len/Mode/错误）→ SetController 初始化属性断言、SyncMode 投影断言、Next/Previous 转调断言、ErrEmpty → NotSupported、refreshNav 触发时机
  - 写回调单测（直接调 callback 函数，仿 volumeCallback 现有测试模式）：非法值 InvalidArgs、合法值转调 SetMode
  - 现有 fakePlayer/fakeProps/fakeBus 复用
- queue 包：并发读写 -race（新）、现有测试全绿回归
- ui 包：MprisController 路由测试（reqs channel 注入 fake 消息 → PlayNext/PlayPrevious 触发 beginPlay、SetMode 切换模式并触发 onModeChanged）、cycleMode 重构回归（现有 toggleModeMsg/queueModeMsg 测试保持绿）
- 全量验证：`go build ./... && go vet ./... && go test ./... -race`

## 6. 文档修订

- 新写本 spec；修订 `docs/superpowers/specs/2026-08-13-music-tui-design.md` 13.3 节：
  - Next/Previous 描述改为"转调队列 controller，空队列 NotSupported"
  - LoopStatus 改为"Playlist/Track/None 读写，映射播放模式"；Shuffle 改为"读写，映射随机模式"
  - CanGoNext/CanGoPrevious 改为"动态属性（Len>1），EmitTrue"
  - 属性清单与数据流同步更新

## 7. 验收标准

1. `playerctl next` / `playerctl previous` 切歌生效（含回绕）
2. `playerctl loop track/playlist/none` 切换模式生效且 UI 底部/队列页模式显示同步
3. `playerctl shuffle on/off` 切换生效且 UI 同步
4. UI 内 s 键/模式按钮切换后 `playerctl loop-status` / `playerctl shuffle` 读到同步值
5. 空队列时 `playerctl next` 返回错误、CanGoNext=false
6. 全量构建/测试/vet/-race 通过

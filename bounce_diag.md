# 歌词回跳诊断报告（systematic-debugging Phase 1-2）

- **Worktree**: `/data/code/music-tui/.worktrees/fix-lyrics-ms`
- **分支**: `fix/lyrics-ms-precision`（基于 `dev`，含 `lyrics/lrc.go` 毫秒化 + `ui/home.go` hysteresis）
- **现象**: 拖动进度条同一句有时候跳有时候不跳；表现为上一句结束跳到下一句 → 莫名跳回上一句 → 再回到正确句。边界附近状态抖动，非固定几句卡。
- **复现脚本**: `/tmp/repro_bounce.go`（在 worktree 内 `go run` 验证）
- **检验文件**: `lyrics/lrc.go:LineAt`、`ui/home.go:syncState/rebuildLyrics/middleCache`、`ui/root.go:waitForPlayerEvents/progressThrottle`、`player/mpv.go:handlePropertyChange`

---

## 1. 执行的诊断步骤

1. **阅读代码**：确认 `lyrics/lrc.go` 已改为 `timeToMs(Round)` 比较；`ui/home.go:syncState` 仅当 `idx != currentLine` 重建；`ui/progress_throttle.go` 200ms 窗口取最后；`ui/root.go:waitForPlayerEvents` 的 `select` 随机性；`player/mpv.go` 的 `time-pos` 直接 emit 无单调守卫。
2. **构造复现**：用 5 行歌词 `[0,10,20,30,40]` 模拟 mpv 位置抖动 `19.99,20.00,19.995,20.01` 调用 `LineAt`；模拟 `progressThrottle` 窗口内乱序 `10.0(N+1)→9.99(N)` 取最后；检查 `middleCache` 是否复用旧帧。
3. **证据采集**：在 worktree 内运行 `go run /tmp/repro_bounce.go`（确保命中毫秒化后的 `lyrics`），观察 N→N+1→N→N+1 可稳定复现；检查 `player/mpv.go` 无 `pos < lastPos` 过滤；检查 `ui/home.go` 缓存键逻辑。

---

## 2. 根因排序（按证据强度）

### 根因 A：`player/mpv.go` 的 `time-pos` 可回退 + `lyrics.LineAt` 边界锐利（最高置信）

**机理**  
- `player/mpv.go:handlePropertyChange`（`obsTimePos`）对 `pos>0` 仅标记 `stallSawProgress`，其余直接 `emit(ProgressEvent{Position:pos})`，无单调性守卫，`pos > duration` 仅丢弃，其他回退透传。
- 回退来源：① 拖动后 `mpv Seek absolute` 异步，`time-pos` 在 demuxer 定位完成前仍报旧值；② `ui/home.go:Update` 对 `left/right` 与进度条点击做了**乐观更新** `m.state.Position = target`，但下一帧 `ProgressEvent` 若仍是旧 `time-pos` 会覆盖乐观值；③ mpv 音频时钟微校正（网络抖动、seek 后校正常见 5-15ms）。
- `lyrics.LineAt` 虽已改为毫秒 `Round`，但仅消除 **<0.5ms** 亚毫秒抖动；`19.995s`（`ms=19995`）距 `20.000s` 边界仍有 5ms，仍映射到上一行。复现脚本在 worktree 内证实：
  ```
  19.99(19990)→idx1, 20.00(20000)→idx2, 19.995(19995)→idx1 ***BOUNCE***, 20.01→idx2
  ```
  粗粒度 LRC（整秒如 `10,20,30`）最易触发——毫秒级回退即跨句。

**证据**  
- 代码审查：`player/mpv.go` `handlePropertyChange` 无回退过滤；`ui/home.go` 乐观更新与服务器值 race 显式存在。
- 复现强度：worktree 内 5-10ms 回退必现 N→N+1→N；用户描述“拖动同一句有时候跳”与 seek 后旧值竞态一致；该路径与 `progressThrottle` 无关也能复现（单帧即跳）。

---

### 根因 B：`ui/root.go:progressThrottle` 200ms 窗口“取最后” + `waitForPlayerEvents` 的 `select` 随机（高置信，解释间歇性）

**机理**  
- `ui/progress_throttle.go`: `Push(ProgressEvent)` 进入 200ms 合并窗口，窗口内只保留**最后一个** `pending`，`Fired` 到点 `Take` 才放行。注释明确“不重置计时——重置会造成永不触发”。
- 若抖动序列 `[19.99(N),20.00(N+1),19.995(N)]` 落在同一窗口内，`pending` 最终为 `19.995(N)`，`Fired` 交付的正是回退值，UI 先显示 N+1（上一窗口）再显示 N（本窗口），形成 200ms 间隔的回跳。
- `ui/root.go:waitForPlayerEvents`：
  ```go
  select {
   case ev, ok := <-p.Events(): if th.Push(ev) { return ... }
   case <-th.Fired(): pe,_ := th.Take(); return ...
  }
  ```
  当 `Events` 与 `Fired` 同时就绪时，Go `select` **均匀随机**选分支。后果：
  - `Fired` 先赢：先交付回退 N，下一窗口再交付 N+1 → 多一帧回跳。
  - `Events` 先赢：新值先替换 `pending`，再 `Take` 得到的是 N+1 → 无回跳。
- 该随机性精确解释“同一句有时候跳有时候不跳”的非确定性。

**证据**  
- 复现：`throttle.Push(19.99)→Push(20.00)→Push(19.995) → Fired` 结果 `idx1`（期望 `idx2`），回归 `*** REGRESSION: window ends on stale N ***`。
- 代码审查：`th.Push` 对 `ProgressEvent` 返回 `false`（不立即放行），`Fired` 通道由 `time.AfterFunc(window, close)` 驱动；`select` 随机性是语言规范，非实现缺陷。
- 时序影响：窗口 200ms 意味着回跳总是以 200ms 粒度出现，符合用户“马上回到正确句”（下一个窗口校正）。

---

### 根因 C：`lyrics.LineAt` 毫秒化是必要但不充分，需滞回（中等置信，作为防御缺口）

**机理**  
- 当前 `lyrics` 修复：`timeToMs = Round(t*1000)`，`LineAt` 按 `posMs` 与 `TimeMs` 比较，`Shift` 按毫秒整数累加，`parseTimeTag` 按整数毫秒计算。测试 `TestLineAt_SubMillisecondStability` 覆盖 10.0575-10.0584 稳定命中。
- 该修复仅保证 **0.5ms 内浮点误差**不抖动；对于 5-20ms 的真实回退（时钟校正、seek 旧值）仍锐利切换。粗粒度 LRC（`[00:10.00]` 整秒或 10ms 步进）上，任意 5ms 回退即跨句，远大于 0.5ms 容差。
- `ui/home.go:syncState` 的 `idx != currentLine` hysteresis 仅抑制**同行内**微抖重建，跨行切换必重建 `rebuildLyrics/scrollLyricsTo/rebuildMiddleCache`，无时间维度滞回，跨行抖动被忠实重放。

**证据**  
- 边界实验：`19.9995`（0.5ms 距离）经 Round 后与 `20.0` 同 `ms=20000`，`idx2` 稳定；但 `19.995`（5ms 距离）`ms=19995` 仍 `idx1`，回跳依旧。
- `home.go` 注释自身承认“hysteresis 防抖：仅当 idx != currentLine 时触发重建，避免同行内 position 微抖… lyrics 层已按毫秒整数比较消除亚毫秒抖动，此处为双保险”——双保险仅覆盖亚毫秒。

---

### 已排除：`ui/home.go:middleCache` 复用旧缓存

- `middleView` 命中条件：`middleCache != "" && W/H/hide 匹配 && lyricsState != loading`；否则 `renderMiddleView()`。
- `syncState` 仅在 `idx != currentLine` 时 `rebuildLyrics→scrollLyricsTo→rebuildMiddleCache()`，跨行必重建，无 stale 复用；同行内保留缓存是**预期**稳定行为。
- 验证：窗口 200ms 内缓存显示上一窗口的 N+1 约 200ms 属 throttle 滞后，非缓存 bug；`lyricsLoading` 时显式跳过缓存，避免 spinner 动画被缓存。

---

## 3. 最小验证方案（不改业务代码）

### 方案 1：加日志定位回退源（推荐，零风险）

在 `player/mpv.go:handlePropertyChange`（`obsTimePos` 分支）与 `ui/root.go:waitForPlayerEvents` 的 `Push/Fired/Take` 处打带 `ms` 精度日志：

```go
// mpv.go handlePropertyChange
log.Debug("time-pos raw=%.6f ms=%d last=%.6f delta=%.3fms", pos, timeToMs(pos), lastPos, (pos-lastPos)*1000)
// root.go waitForPlayerEvents
log.Debug("throttle Push pos=%.3f idx=%d pending=%.3f", ev.Position, idx, pending)
log.Debug("throttle Fired deliver pos=%.3f idx=%d", pe.Position, idx)
// home.go syncState
log.Debug("syncState pos=%.3f idx %d->%d", state.Position, oldIdx, newIdx)
```

拖动进度条复现一次，观察：是否出现 `delta<0` 的 `time-pos` 回退；`Push` 的 `pending` 是否以回退值收尾；`syncState` 的 `idx` 是否出现 N→N+1→N。

### 方案 2：加滞回（hysteresis）验证（最小改动，仅验证）

在 `lyrics.LineAt` 或 `home.syncState` 引入时间滞回，例如：

- 选项 A（歌词层）：记录 `lastIdx` 与 `lastSwitchMs`，当 `pos` 回退跨边界且距上次切换 <500ms 时抑制回跳（或要求 `pos > Lines[idx+1].Time + 0.3s` 才前进，`pos < Lines[idx].Time - 0.1s` 才后退）。
- 选项 B（UI 层）：在 `progressThrottle` 或 `syncState` 增加单调守卫 `if pos+0.02 < lastDeliveredPos { drop }`，或 `if idx == lastIdx-1 && timeSinceLastSwitch < 400ms { keep }`。

先以**日志+计数**验证：统计 500ms 窗口内跨边界回跳次数，确认滞回能消除 90%以上回跳且不误伤正常切句。

### 方案 3：单调守卫对比实验

在 `player/mpv.go` 临时加入 `lastPos` 守卫：

```go
if pos < lastPos - 0.005 { log.Warn("time-pos backward %.3f -> %.3f", lastPos, pos); return /*drop*/ }
```

对比丢弃回退 vs 不丢弃的回跳率；若丢弃后回跳消失，则根因 A 得确证。注意拖动 seek 后首帧可能合法回退，需排除“seek 发生后 300ms 内”的合法回退。

---

## 4. 复现脚本使用

```bash
cd /data/code/music-tui/.worktrees/fix-lyrics-ms
~/go-sdk/go/bin/go run /tmp/repro_bounce.go
# 关键输出：4 次 BOUNCE，Window1 Fired regression，select 随机解释
```

脚本已覆盖：① 边界抖动 N→N+1→N→N+1；② 200ms 窗口取最后导致回退值被交付；③ select 随机导致非确定性。

---

## 5. 结论

最可能的链路是 **A→B 叠加**：`mpv time-pos` 的 5-20ms 回退（seek 旧值/时钟校正）经 `LineAt` 锐利边界产生跨句，`progressThrottle` 的窗口取最后将回退值固化为 200ms 粒度的可见帧，`select` 随机使同一操作间歇性复现。`LineAt` 的毫秒化已解决亚毫秒问题，但对真实回退需时间维度滞回才能根治；`middleCache` 非根因。


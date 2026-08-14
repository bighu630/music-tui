# 首页排版调整 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 首页撑满全屏；中间左封面右歌词居中；底部进度条行（上）+ 控制按钮行（下）；播放模式三态（列表循环/随机/单曲循环）；进度条点击 seek。

**Architecture:** 纯逻辑层（queue 三态模式 + Prev、线条渐变进度条纯函数）先行，player 接口扩展 SetLoop（mpv loop-file 无缝单曲循环），最后 UI 层（home.go 布局改版 + root.go 消息接线）。全部 TDD，回归测试同步更新。

**Tech Stack:** Go / bubbletea / lipgloss / bubbles（viewport、spinner）/ mpv IPC。Spec: `docs/superpowers/specs/2026-08-14-home-layout-redesign.md`

---

### Task 1: queue 三态模式 + Prev

**Files:**
- Modify: `queue/queue.go`（Mode 枚举、Next 回绕、新增 Prev）
- Test: `queue/queue_test.go`

- [ ] **Step 1: 写失败测试**——更新 `TestNextSequentialOrderAndStopAtEnd` 为回绕语义；新增：Prev 回绕、Prev 空队列、Prev 单曲队列、RepeatOne 模式 Next 正常推进、Shuffle 末尾回绕、Next 空队列不变。
- [ ] **Step 2: 跑测试确认失败**：`go test ./queue/ -run 'TestNext|TestPrev'`
- [ ] **Step 3: 实现**
  - `Mode` 增加 `RepeatOne`（iota 第三位）；注释更新：Sequential = 列表循环（Next 末尾回绕到 0），Shuffle = 随机循环（末尾回绕，不重洗），RepeatOne = 单曲循环（队列推进语义同 Sequential，无缝循环在 player 层）。
  - `Next()` 末尾回绕：`currentIdx+1 >= len(tracks)` 时 `currentIdx = 0` 并返回 `tracks[0]`（Sequential/Shuffle/RepeatOne 一致）。空队列返回 false 不变。currentIdx==-1 从头开始不变。
  - 新增 `Prev() (model.Track, bool)`：空队列 false；currentIdx==-1 → 指向末尾；currentIdx==0 → 回绕到末尾；否则 currentIdx--。
- [ ] **Step 4: 跑测试通过**：`go test ./queue/`
- [ ] **Step 5: Commit**：`feat(queue): 三态播放模式与回绕 Next/Prev`

### Task 2: player 接口 SetLoop + mpv 实现 + fake

**Files:**
- Modify: `player/player.go`（接口加方法）、`player/mpv.go`（实现）、`ui/root_test.go`（fakePlayer）
- Test: `player/mpv_test.go`（如有，检查现有测试文件）

- [ ] **Step 1: 写失败测试**：mpv 的 SetLoop 测试（在现有 mpv 测试基建上；若 mpv 测试用 fake conn，断言 `set_property loop-file` 调用）；fakePlayer 加 `SetLoop` 记录字段 `loops []bool`。
- [ ] **Step 2: 确认失败**：`go build ./...`（接口未实现必然编译失败）
- [ ] **Step 3: 实现**
  - `Player` 接口新增 `SetLoop(loop bool) error`（注释：单曲循环用 mpv loop-file 无缝循环；per-file 属性，换文件自动重置）。
  - mpv：`conn.Call("set_property", "loop-file", loop)`。
  - fakePlayer 实现 + 断言 helper。
- [ ] **Step 4: 测试通过**：`go test ./player/ ./ui/`
- [ ] **Step 5: Commit**：`feat(player): SetLoop 单曲无缝循环`

### Task 3: 线条渐变进度条纯函数 + 点击换算

**Files:**
- Create: `ui/progressbar.go`、`ui/progressbar_test.go`
- Modify: 无（home.go 接线在 Task 4）

- [ ] **Step 1: 写失败测试**
  - `lineProgressBar(width, percent)`：
    - width=20, percent=0 → 0 个已播字符，滑块在首位，其余 `━` 灰。
    - width=20, percent=0.5 → 滑块在第 10 字符位，前 10 个 `━` 渐变色，后 9 个灰。
    - width=20, percent=1 → 全部已播，滑块在末位。
    - width<3 退化：width=1 → `●`；width=2 → `━●`（无 panic）。
    - 渐变：已播段含 ANSI 色码（断言字符串含 `\x1b[` 且渐变色阶数 > 1；同一百分比下位置不同颜色不同——取渲染串剥离 ANSI 后可见宽 = width）。
    - 滑块字符 `●` 恰好出现一次。
  - `progressClickPercent(x, barWidth int) float64`：x=0 → 0；x=barWidth-1 → 接近 1；x=barWidth/2 → 0.5；x 越界 clamp。
- [ ] **Step 2: 确认失败**：`go test ./ui/ -run TestLineProgress -v`
- [ ] **Step 3: 实现**（新文件 `ui/progressbar.go`）
  - 色阶常量：`[]lipgloss.Color{"63","99","129","177","212"}`（紫→粉）。
  - 已播字符按 `i/(filled)` 比例取色阶；未播 `━` Faint 灰。
  - 注意：宽字符/ANSI 宽度处理——`━` `●` 均为单宽字符；最终可见宽必须 == width（测试断言）。
  - 点击换算：`percent = float64(x) / float64(barWidth)` clamp [0,1]。
- [ ] **Step 4: 测试通过**：`go test ./ui/ -run 'TestLineProgress|TestProgressClick' -v`
- [ ] **Step 5: Commit**：`feat(ui): 线条渐变进度条与点击换算`

### Task 4: home.go 布局改版 + 按钮/键位/鼠标

**Files:**
- Modify: `ui/home.go`（view 全屏布局、底部两行、MouseMsg、`,` `.` 键、按钮消息）、`ui/home_test.go`
- 依赖：Task 1（queueMode）、Task 3（lineProgressBar/progressClickPercent）

- [ ] **Step 1: 写失败测试**
  - view 撑满：`setSize(120, 40)` 后 `view()` 行数 == 40（含空行）。
  - 中间区：有曲目时 view 含封面（或占位框）与歌词区，两者水平并排（断言 `No Cover` 与 `暂无歌词`/歌词内容行号关系——具体断言以"同一行同时含两个区块内容"或按行解析）。
  - 无歌词居中：lyricsNone 态渲染出的 `暂无歌词` 在歌词列的垂直中部（按 view 行号断言）。
  - 底部行1（进度条行）：view 倒数第 2 行含 `●` 与时间串。
  - 底部行2（按钮行）：view 最后一行含 `⏮` `⏯` `⏭` 与模式图标。
  - `,` 键 → fakePlayer 播放前一曲（plays 断言）；`.` 键 → 下一曲。
  - MouseMsg 点击进度条行（构造 `tea.MouseMsg{Y: height-1+1, X: barWidth/2}`，注意 Tab 栏偏移）→ fakePlayer.seeks 有值且 ≈ Duration/2。
  - 无曲目时 `,`/`.`/点击均无命令。
- [ ] **Step 2: 确认失败**
- [ ] **Step 3: 实现**
  - 布局重构 `view()`：
    - 无曲目：`lipgloss.Place(width, height, Center, Center, "🎵 未在播放...")` 撑满。
    - 有曲目：中间区 = `PlaceVertical(center)` 包 `JoinHorizontal(Top, coverView, "  ", lyricsColumn)`；歌词列高度 = 中间区高，宽度 = width-coverW-gap-边距。
    - 底部两行：行1 = `lineProgressBar + 时间`；行2 = 按钮栏（`⏮  ⏯  ⏭  模式按钮  |  标题-歌手  |  3/12·模式`，超宽截断）。
    - 歌词居中：歌词内容行数 < 歌词列高时用 padding/Align 垂直居中；溢出走现有 viewport 逻辑。`暂无歌词`/加载中用居中渲染。
  - `Update` 增加 `tea.MouseMsg`：换算页面内坐标（屏幕 Y-1）；落在进度条行（页面内 Y==height-2）→ seek；落在按钮行（页面内 Y==height-1）→ 按 X 区间触发上一首/播放暂停/下一首/模式切换。按钮 X 区间常量（如 0-3 ⏮、4-7 ⏯、8-11 ⏭、13-16 模式）。
  - 新增消息生产 cmd：`prevTrackCmd`/`nextTrackCmd`/`toggleModeCmd`（类型定义放 root.go 或 home.go 由 Task 5 消费）。
  - `,`/`.` 键 → 对应 cmd（无曲目忽略）。
- [ ] **Step 4: 测试通过**：`go test ./ui/ -run 'TestHome' -v`
- [ ] **Step 5: Commit**：`feat(ui): 首页全屏布局与底部控制区`

### Task 5: root.go 接线（模式循环、prev/next、SetLoop、TrackEnded 回绕）

**Files:**
- Modify: `ui/root.go`、`ui/queue.go`（modeLabel 三态文案、s 键三态循环）、`ui/queue_test.go`、`ui/root_test.go`
- 依赖：Task 1/2/4

- [ ] **Step 1: 写失败测试**
  - 模式切换：`m` 键/首页模式按钮 → Sequential→Shuffle→RepeatOne→Sequential 循环；RepeatOne 时 fakePlayer.loops 最近一次为 true，其他模式为 false。
  - 切歌（beginPlay）时 SetLoop 随模式设置。
  - `.` 下一首：队列 [t1,t2] 当前 t1 → 播放 t2；`:` 无、`,` 上一首：当前 t2 → 重播 t1。
  - TrackEnded 回绕：队列 [t1,t2] 播完 t2 → 回绕播 t1（原"停止"断言改为回绕）。
  - 队列空 TrackEnded → 仍停止（ended=true，不 panic）。
  - 单曲循环 + 手动下一首：RepeatOne 下 `.` → 正常切下一首且 SetLoop(false)（新文件 loadfile 重置，但 UI 显式关闭）。
- [ ] **Step 2: 确认失败**
- [ ] **Step 3: 实现**
  - 消息类型：`prevTrackMsg`/`nextTrackMsg`/`toggleModeMsg`（root.Update 处理：prev → queue.Prev() → beginPlay；next → queue.Next() → beginPlay；toggleMode → SetMode 三态循环 → syncQueueViews）。
  - `beginPlay` 末尾：模式 == RepeatOne → `SetLoop(true)`，否则 `SetLoop(false)`（失败仅记 lastError 不阻断播放）。
  - `queueModeMsg`（队列页 s 键）复用三态循环逻辑。
  - `queue.go` `modeLabel`/`modeName`：三态文案（列表循环/随机/单曲循环）。
  - TrackEnded 分支：Next 末尾回绕后 `stopAfterEnd` 仅队列为空时触发（现状已如此，验证回绕后行为即可）。
- [ ] **Step 4: 测试通过**：`go test ./ui/`
- [ ] **Step 5: Commit**：`feat(ui): 模式三态循环与上一首/下一首接线`

### Task 6: 回归更新 + 全量验证

**Files:**
- Modify: `ui/queue_test.go`（播完停止 → 回绕）、`queue/queue_test.go`（StopAtEnd → 回绕）、其他受影响的测试

- [ ] **Step 1: 定位受影响断言**：`grep -rn "播完\|StopAtEnd\|无下一首" queue/ ui/*_test.go`，逐一定位"播完停止"断言并更新为回绕语义（或按 spec 调整用例预期）。
- [ ] **Step 2: 全量验证**
  - `go build ./...`
  - `go vet ./...`
  - `go test ./... -race`
- [ ] **Step 3: 自查**：功能不回归——seek ←/→、空格暂停/重播、歌词高亮滚动、封面缓存渲染（TestHomeCover* 系列）、队列页 s 键。
- [ ] **Step 4: Commit**：`test: 更新播完循环语义回归用例`

---

## 交付物

- 全屏首页布局（中间封面+歌词居中、底部进度条行+按钮行、无歌词居中提示）
- 线条渐变进度条（━ + ● 滑块，点击 seek）
- 播放模式三态（🔁 列表循环 / 🔀 随机 / 🔂 单曲循环），mpv 无缝单曲循环
- `,`/`.` 上一首/下一首、`m` 模式切换、按钮鼠标点击
- 全量测试通过（build/vet/test -race），无功能回归

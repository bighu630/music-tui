# 首页排版调整设计（home-layout-redesign）

日期：2026-08-14 · 状态：已获用户确认

## 目标

调整 music-tui 首页排版：撑满全屏；底部两行控制区（进度条行 + 按钮行）；中间左封面右歌词（均居中，无歌词时提示也居中）。不回归现有功能（seek、空格暂停、歌词高亮滚动、封面渲染）。

## 布局（仅首页；Tab 栏 1 行不变）

窗口高度 H（setSize 收到的是 Height-1，即 Tab 栏以下页面高度）。新布局：

```
┌─ 中间区（高度 H-2，内容垂直居中）──────────────────┐
│      [封面 30×17 居中]    [歌词区 垂直居中]          │
│    （占位框/加载中/无封面）  （"暂无歌词"也居中）     │
├─ 底部行1 进度条 ─────────────────────────────────┤
│  ━━━━━━━●━━━━━  01:23/03:45                    │
├─ 底部行2 控制栏 ─────────────────────────────────┤
│  ⏮  ⏯  ⏭  🔁  |  标题 - 歌手   |  3/12·模式     │
└──────────────────────────────────────────────────┘
```

- 进度条行（页面内 Y = height-2）在**上**，按钮行（页面内 Y = height-1）在**下**（用户确认）。
- 中间区：`JoinHorizontal(Top, 封面列, gap, 歌词列)` 后用 `lipgloss.Place` 垂直居中（或等价的 Padding 计算）。
- "未在播放"空态：全屏居中占满（不再左上角两行）。

## 底部行1：线条渐变进度条（自绘，弃用 bubbles/progress）

- 纯函数 `lineProgressBar(width int, percent float64) string`（home.go 内或新文件 ui/progressbar.go）：
  - 渲染 `━` × 已播字符数 + `●` 滑块 + `━` × 剩余字符数；宽度不足 3 时退化（滑块在 0 端，纯 `━` 或 `●`）。
  - 滑块位置：`filled = round(percent * width)`，滑块放在第 `filled` 个字符位置（0-based；percent=1 时滑块在末尾）。
  - **渐变**：已播段每个 `━` 按位置在色阶中取色（紫→粉，与现有 `progress.WithDefaultGradient` 观感一致：`#5A56E0` → `#EE6FF8` 或等价 lipgloss 色号阶梯，如 63→212 的 5-8 段插值）。未播段 `━` 灰色（Faint 或 240）。
  - 渐变实现：预定义色阶切片（如 `[]lipgloss.Color{"63","99","129","177","212"}` 5 段），按 `i/filled` 比例取色；纯函数、无状态，可单测。
- 进度条宽度 = 页面宽度 - 时间串宽度 - 间距（`mm:ss/mm:ss`，formatDuration 已有）。
- **鼠标点击 seek**：点击落在进度条行（屏幕 Y == height，因 Tab 栏占 1 行；页面内 Y = height-2 → 屏幕 Y = height-1+1？——统一约定：页面内坐标 = 屏幕坐标 - 1，由 home.Update 处理 MouseMsg 时换算）且列 X ∈ [0, barWidth) → `seekCmd(player, percent*Duration)`。home.Update 增加 `tea.MouseMsg` 分支（root.onMouse 在 Y!=0 时已 delegate 到页面，无需改 root）。
- 渲染期记录进度条行号：固定为页面内倒数第 2 行，无需动态测量。

## 底部行2：控制按钮栏

- 按钮：`⏮ 上一首` / `⏯ 播放/暂停` / `⏭ 下一首` / 模式按钮（三态图标：🔁 列表循环 / 🔀 随机 / 🔂 单曲循环），中间显示 `标题 - 歌手`（截断），右侧 `队列位置 · 模式名`。
- **鼠标点击**：按钮行（页面内 Y = height-1）的 X 落在按钮区间 → 触发对应动作（上一首/播放暂停/下一首/模式切换）。按钮宽度常量（如每个按钮含图标+间距固定宽，点击用 X 区间判断）。
- **键位**（root.go 全局或 home.go 局部处理，不冲突现有键）：
  - `,` 上一首、`.` 下一首（home.Update）
  - `m` 模式切换（home.Update 或 root 全局；队列页 `s` 键保持切换，两处共用同一消息/逻辑）
  - 空格=播放暂停（已有）、←/→=seek（已有）
- 无队列/无当前曲时按钮与 `,`/`.` 忽略（不 panic、无命令）。

## 播放模式三态（queue 包扩展）

`queue.Mode` 增加 `RepeatOne`；语义：

- `Sequential`（🔁 列表循环）：`Next()` 在末尾回绕到 0（**行为变化：原"播完停止"取消**）。
- `Shuffle`（🔀 随机）：现有洗牌逻辑不变；`Next()` 末尾回绕到 0（不重洗，保持简单）。
- `RepeatOne`（🔂 单曲循环）：队列逻辑同 Sequential（手动下一首正常推进）；**无缝循环由 player 层实现**。
- 新增 `Prev()`：currentIdx-1，回绕到末尾（currentIdx==-1 时从末尾开始）；空队列返回 false。
- `SetMode` 三态循环由 UI 层做（Sequential→Shuffle→RepeatOne→Sequential）。

### player 层：SetLoop（单曲循环无缝）

- `player.Player` 接口新增 `SetLoop(loop bool) error`；mpv 实现：`conn.Call("set_property", "loop-file", loop)`（loop-file 是 per-file 属性，新 loadfile 自动重置为 no，无需显式关闭；但切歌/切模式/退出时 UI 层仍显式 SetLoop(false) 保证一致性）。
- mpv 循环时不产生 end-file/TrackEnded 事件（mpv 标准行为），root 的 TrackEnded 分支在单曲循环下自然不触发；进度回绕由 time-pos property-change 自然驱动。
- fakePlayer 记录 SetLoop 调用（供测试断言）。
- 单曲循环期间用户按 `.`/上一首或模式切走 → UI 层 SetLoop(false) + beginPlay 新曲。

### root.go 接线

- `beginPlay`/`startPlay` 等入口：若当前模式为 RepeatOne → `SetLoop(true)`，否则 `SetLoop(false)`（播放开始时设置）。
- 模式切换消息（现有 queueModeMsg 语义扩展为三态循环）由首页按钮/`m` 键/队列页 `s` 键共用。
- 首页按钮消息：新增 `prevTrackMsg`/`nextTrackMsg`（或复用现有 queue 消息模式）→ 调用 queue.Prev/Next → beginPlay。
- TrackEnded 分支：Next 末尾回绕后不再触发 stopAfterEnd（除非队列为空）。保留 queueSkip/ended 逻辑。

## 歌词区居中

- 歌词行数 < 区域高度：内容垂直居中（渲染时在内容前后补空行或 Align）；行数 ≥ 区域高度：现有行为不变（viewport 滚动 + 当前行居中 scrollLyricsTo）。
- "暂无歌词" / "歌词加载中…"：在歌词列内垂直居中。
- 歌词区宽度 = 页面宽度 - coverW - gap - 边距；高度 = 中间区高度。

## 测试策略

- 纯函数单测（新文件 home 相关或 ui/progressbar_test.go）：
  - `lineProgressBar`：宽度/百分比/滑块位置/渐变字符着色/退化宽度。
  - 点击列 → 百分比换算。
  - queue：Prev/Next 三态循环语义（末尾回绕、RepeatOne、空队列、单曲队列）。
- 集成测试（root_test/home_test 风格，fakePlayer 注入）：
  - 底部按钮渲染存在（view 含 ⏮/⏯/⏭/模式图标）。
  - `,`/`.` 触发 prev/next（fakePlayer.plays 断言 URL 序列）。
  - `m`/模式按钮 → 三态循环切换 + RepeatOne 时 SetLoop(true)。
  - 点击进度条 MouseMsg → fakePlayer.seeks 断言。
  - 无歌词时 view 中 "暂无歌词" 垂直居中（断言渲染行位置或包含关系）。
  - 全屏：view 行数 == height（撑满断言）。
- **回归更新**（行为变化导致的既有测试改语义）：
  - `queue/queue_test.go` `TestNextSequentialOrderAndStopAtEnd` → 改为回绕断言。
  - `ui/queue_test.go` `TestTrackEndedAutoAdvances`（播完停止）→ 改为播完回绕第一首。
  - `TestDeleteLastCurrentThenTrackEndedPlaysFromHead` 等依赖"停止"语义的用例逐一定位更新。
- 全量验证：`go build ./...`、`go vet ./...`、`go test ./... -race`。

## 不做的事（YAGNI）

- 不改搜索/历史/队列页布局（仅队列页模式名展示同步三态文案）。
- 不重洗随机循环序列。
- 不做进度条拖拽/滑块交互（仅点击）。
- 不加 MPRIS 相关改动。

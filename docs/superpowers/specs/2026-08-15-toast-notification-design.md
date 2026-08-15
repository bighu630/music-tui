# Toast 通知 + 底部常驻状态栏设计（toast-notification）

日期：2026-08-15 · 状态：已获用户确认

## 目标

把 music-tui 的错误/成功提示从"替换页面中间区末行的横幅"改为 toast 通知式：
临时浮现、数秒后自动消失、不参与布局计算（排版零跳动）；同时新增底部常驻
状态栏作为布局稳定的报错/状态"家"。所有提示统一走 toast 通道。

## 现状问题（改动动机）

- root.View 中 `lastError`（红 ⚠）/ `notice`（绿 ✔）横幅**替换** body 中间区
  末行（倒数第 3/4 行）显示——报错时该行内容被顶掉、消失后恢复，画面跳动
  （用户原话："报错进程破坏排版"）。
- 横幅不会自动消失，一直挂到下次操作才清除。
- 报错产生点共 14 处（播放失败、恢复失败、重试中/重试耗尽、跳过、播放列表
  CRUD 失败、p 键无选中、seek/循环失败等），全部写 `m.lastError`；
  成功提示（"已添加到…"）写 `m.notice`。全仓仅 root.go/root_test.go 引用这两个字段。

## 设计决策（用户已确认）

| 决策点 | 结论 |
|---|---|
| 固定区域形式 | A：底部常驻状态栏（恒 1 行，页面高度同步减 1，一次性调整后永不变） |
| toast 位置 | 最后一行（状态栏行）左对齐：报错期间该行显示报错消息（状态栏内容临时被覆盖），消失后恢复，不参与布局 |
| 显示时长 | 错误/警告 5s，成功/信息 3s |
| 多条通知 | 新 toast 覆盖旧 toast（单条显示，重置定时器） |
| 通道统一 | 错误/警告/成功/信息全部走 toast 通道 |
| 过程性提示 | "正在自动重试"等也走 toast（warning，5s） |

## 布局（改动后）

窗口高度 H（root 收到 WindowSizeMsg）。页面 setSize 高度从 `Height-2` 改为
`Height-3`：

```
┌─ Tab 栏 2 行（标签行 + 分隔线，不变）─────────────────┐
├─ 页面 body（高度 H-3，内容不变）──────────────────────┤
│  ...                                                │
│  （toast 只动最后一行，body 逐字不变）                 │
├─ 底部状态栏 1 行（恒存在）────────────────────────────┤
│  ⏵ 顺序播放 · 3/10           [当前曲目标题截断]       │
└─────────────────────────────────────────────────────┘
```

- 状态栏内容：左 = `⏵/⏸/⏹ 模式 · 队列位置 x/N`（无播放时 `⏹ 未在播放`；
  播放结束/出错停止显示 `⏹`，暂停显示 `⏸`），
  右 = 当前曲目标题（先渲染左段，再按剩余宽度 `width - leftW - 1` 动态
  `ansi.Truncate` 截断，窄窗口不折行）。所有页面（含选择器打开时）共用。
- toast 渲染到 `lines[len-1]`（最后一行 = 状态栏行）左对齐：报错期间该行
  整行替换为 toast 文本（状态栏内容临时被覆盖），自动消失后状态栏内容
  恢复；行数恒不变、其余行逐字不变 → 排版零跳动（toast 不参与布局）。
  超宽 toast 按 `ansi.Truncate(text, m.width, "…")` 截断（tail 宽度计入
  长度，结果恒 ≤ m.width 不折行）：**保头部**（错误类型/消息开头），尾部
  省略号截断——截掉句尾（如“跳过继续播放”）是用户要求的变更，与旧
  TruncateLeft 保句尾不同。整行替换无样式渗透风险（不截断原行内容）；
  ansi.Truncate 对 lipgloss 样式安全（截断点后的转义含尾部 reset 原样保留）。

## 架构

### 新文件 `ui/toast.go`：toast 状态机

```go
type toastKind int
const (
    toastError   toastKind = iota // 红 ⚠，5s
    toastWarning                  // 黄 ⚠，5s
    toastSuccess                  // 绿 ✔，3s
    toastInfo                     // 默认色 ℹ，3s
)
type toast struct {
    text string
    kind toastKind
    id   uint64
}
// 包级时长变量（测试可调小快进，同 retryBackoff 模式）：
// var toastErrorDuration = 5 * time.Second
// var toastSuccessDuration = 3 * time.Second
```

- Model 新增 `toast *toast` + `toastID uint64`；**删除 `lastError`/`notice` 字段**。
- `showToast(text string, kind toastKind) (Model, tea.Cmd)`：覆盖语义
  （替换旧 toast + 重置定时器），返回 `tea.Tick(时长, …)` 产生
  `toastExpireMsg{id}`；id 与当前 toast 不匹配（已被覆盖）则丢弃。
- 新按键/鼠标操作不主动清除 toast（生命周期只由定时器管理）；
  仅 `beginPlay` 成功时清除（新曲目 = 新状态）。

### 消息路由（root.go）

- 14 处 `m.lastError = "…"` → `m.showToast("…", toastError)`；
  "正在自动重试"/"跳过继续播放"/"缓存损坏" → `toastWarning`；
  "已添加到「xxx」" → `toastSuccess`。
- 删除"新按键分发前清除 notice"逻辑（toast 由定时器管理）。

### View 渲染（root.go）

- `tabBar() + "\n" + body + "\n" + statusBar`；statusBar 为纯函数（无状态）。
- toast 活跃时对最后一行（状态栏行）做左对齐整行覆盖（见布局节），
  不改变行数。

## 测试（TDD）

- `ui/toast_test.go` 单测：kind→时长映射、覆盖语义（新 toast 替换旧、
  旧过期消息 id 不匹配被丢弃）、过期清除（`toastExpireMsg` 命中/不命中）。
- `ui/root_test.go` 更新：
  - `TestRootViewBannerStaysWithinHeight` 重写：含 toast 与不含 toast 时
    View 行数相同且除覆盖区外逐行相同；状态栏恒在末行。
  - 现有 `m.lastError` 断言（约 15 处）改为读 `m.toast`（helper `toastText()`）。
  - `WindowSizeMsg` 相关布局断言适配 Height-3（页面高度减 1）。
- 全量：`go build ./... && go vet ./... && go test ./... -race` 全绿。

## 不做的事（YAGNI）

- 不做 toast 队列/堆叠（多条覆盖即可）。
- 不改动各页面内部布局与交互。
- 不引入第三方 toast 库（手工覆盖渲染，依赖已有 ansi.Truncate 模式）。

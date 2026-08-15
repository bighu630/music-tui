# 文件日志系统设计（file-logger）

日期：2026-08-15 · 状态：已获用户确认

## 目标

为 music-tui 增加可排查的文件日志：写入系统临时目录（os.TempDir()），带级别
与大小轮转；现有零散输出点（log.Printf / stderr 警告）统一接入；播放、缓存、
取流、歌词关键链路补齐日志，解决"近期 mpv/缓存/连播问题难以定位"。

用户原话："把日志保存到 tmp 目录吧，不然后面都没法排查了。"

## 现状问题（改动动机）

- 现有 `log.Printf` 约 10 处（cache 下载失败、lyrics 源失败、player 重连、
  ui 历史/会话写入失败），走标准库 stderr——TUI 界面下不可见、无处可查。
- main.go 6 处 `fmt.Fprintf(os.Stderr, "music-tui: 警告：…")` 损坏降级警告，
  只在启动终端可见。
- 关键链路无日志：播放（Play/Pause/Seek/连播切换）、缓存（下载开始/完成/命中/
  淘汰/校验失败）、取流（yt-dlp 命令与失败原因）、歌词（AI 调用、匹配结果）
  均无记录，问题发生时只能靠复现。

## 用户确认的决策

1. **路径**：`/tmp/music-tui.log` 单文件（A 方案）——`tail -f /tmp/music-tui.log`
   即可排查；多实例同时运行会互相追加（已知接受）。
2. **轮转**：单文件上限 **5MB**（A 方案），超限 `music-tui.log` → `music-tui.log.1`
   （旧 .1 删除），保留最近 2 个文件共最多 10MB。
3. **级别配置**：config.json 加可选 `log.level`（debug/info/warn/error，
   缺失或非法回落 `info`）——平时 info，排查时临时改 debug。

## 设计

### 1. 新包 `logger/`（叶子包，只依赖 stdlib）

全局 API（无实例句柄，进程内单例）：

```go
type Level int
const (LevelDebug Level = iota; LevelInfo; LevelWarn; LevelError)
func ParseLevel(s string) Level      // 非法/空 → LevelInfo
func Init(level Level)               // 打开/创建 /tmp/music-tui.log（追加，0600）
func SetLevel(l Level)               // 运行中调整级别（config 加载后调用）
func Debug/Info/Warn/Error(format string, args ...any)
```

- 行格式：`2026-08-16 10:23:45.123 [INFO] 消息`
- 级别过滤：低于当前级别不写。
- 并发安全：内部 mutex 串行化写文件与轮转（mpv 泵 goroutine、缓存下载
  goroutine、UI 线程同时写）。
- 轮转：累计写入大小 + 新行长度 > 5MB → 删旧 `.1` → rename 当前为 `.1` →
  重新创建继续写，计数清零。累计大小由 writer 自维护（不自 stat）。
- Init 失败（tmp 不可写等）→ 静默降级为 no-op：程序正常运行，日志是辅助
  设施绝不阻断启动。
- 文件权限 0600（与 config 写盘一致）：日志含曲目 URL 等，防其他本地用户读取。

### 2. config 扩展

- `Log struct { Level string }`，json tag `"log": {"level": "…"}`。
- Load 规范化：缺失/空/非法 → `"info"`（沿用现有"逐项回落默认"模式，
  返回的 Config 始终是规范化后的完整值）；合法值原样保留。
- 校验用 `logger.ParseLevel`（config import logger，无循环依赖：
  logger 只依赖 stdlib）。
- Default() 默认写 `"log": {"level": "info"}`。

### 3. main.go 初始化与现有输出点迁移

初始化顺序（保证 config 损坏时也有日志）：

```
run() 开头 → logger.Init(LevelInfo) → 加载 config → logger.SetLevel(ParseLevel(cfg.Log.Level))
```

- 6 处 `fmt.Fprintf(os.Stderr, "music-tui: 警告：…")`（损坏降级）→ 保留 stderr
  输出（启动期用户可见）+ 追加 `logger.Warn` 同文案。
- 4 处 `log.Printf`（MPRIS 不可用、缓存降级、AI 缓存降级）→ `logger.Warn`，
  删除 stdlib log import。
- fatal 错误（run() 返回 err）保持 stderr 退出不变。

### 4. 关键链路日志点

| 链路 | 位置 | 级别 | 内容 |
|---|---|---|---|
| 播放 | ui/root.go beginPlay | Info | 曲目（ID/标题/艺术家）+ 是否缓存命中（fromCache） |
| 播放 | ui/root.go resumeCmd | Info | 续播恢复曲目 + 位置 + fromCache |
| 播放 | ui/root.go TrackEnded | Info | 曲目结束 → 连播下一首（或停止，含 queueSkip 分支） |
| 播放 | ui/root.go ErrorEvent | Warn/Error | 播放失败原因、重试（第 N/max）、跳过、重试耗尽停止 |
| 播放 | ui/root.go togglePlay/seek | Debug | 暂停/恢复/seek |
| 播放 | player/mpv.go Play/PlayPaused | Info | loadfile URL；失败 Error |
| 播放 | player/mpv.go 进程退出/断连/重连 | Warn | 现有 log.Printf 升级；重连成功 Info |
| 播放 | player/mpv.go watchdog 超时 | Error | 取流悬挂主动报错 |
| 播放 | player/mpv.go ytdlpconf 失败 | Warn | 现有 log.Printf 升级 |
| 缓存 | cache CacheAsync/download | Debug | 下载开始（第 N 次尝试，ID+标题） |
| 缓存 | cache register 成功 | Info | 下载完成注册（ID+文件名） |
| 缓存 | cache Lookup 命中 | Debug | ID（校验失败删文件 Warn） |
| 缓存 | cache New 启动清理 | Info | 删缺失/损坏条目、超限淘汰（ID） |
| 缓存 | cache download 失败 | Warn | 现有 log.Printf 升级（err 已含 stderr tail） |
| 取流 | search Search/FetchPlaylist | Debug | yt-dlp 调用摘要（query/歌单 URL/条目数） |
| 取流 | search 失败 | Warn | 原因（err 已含 stderr tail） |
| 取流 | cache realDownload | Debug | yt-dlp 下载命令摘要（**header 只打键名不打值**） |
| 歌词 | lyrics enhanced identify | Debug | AI 识别开始/完成（清洗后标题-艺术家）、失败 Warn |
| 歌词 | lyrics 严格重查/中文源 | Debug | 命中/未命中；源失败 Warn（现有 2 处升级） |
| 歌词 | lyrics 最终结果 | Debug | 来源（AI/确定性/中文源） |
| UI | ui/root.go 历史/会话/歌词失败 | Warn | 现有 4 处 log.Printf 升级 |

安全约束：日志中**绝不打印** header 值、cookie 内容；header 仅打键名。
URL 含视频 ID 可打（与 UI 展示一致）。

### 5. 测试

- logger 包单测（TDD 先行）：
  - ParseLevel：四档合法 + 空/非法回落 info。
  - 级别过滤：低于级别不写文件。
  - 格式：时间戳前缀 `[LEVEL] ` 正确。
  - 轮转：写满 5MB（测试用小上限，包级变量可调）→ 生成 .1、旧 .1 被替换、
    新文件继续写。
  - 并发写：多 goroutine 并发写不丢行不竞态（-race）。
- config 测试：log.level 缺失/空/非法 → "info"；合法值保留；Default 含
  `"log": {"level": "info"}`。
- main_test.go 既有测试不动，全量 `go build ./...`、`go vet ./...`、
  `go test ./...`（含 -race）全绿。

### 6. 执行方式

- git worktree：`.worktrees/file-logger`，分支 `feat/file-logger`
  （.worktrees 已在 .gitignore，与其他并行会话隔离）。
- worker 拆分（API 契约先定死，可并行）：
  - W1：logger 包（TDD）+ config 扩展 + main.go 初始化与现有输出点迁移。
  - W2：各包日志接入（ui/player/cache/search/lyrics 的日志点）。
- reviewer 审查循环后合入。

## 非目标（YAGNI）

- 不做日志目录配置（log.file 路径覆盖）——用户选 A 方案固定 /tmp。
- 不做多实例隔离（pid 后缀）——用户接受单文件追加。
- 不做多文件按日轮转（.1 大小轮转已够用）。
- 不改 third_party/go-termimg 与 mpris 包内部（mpris 错误由 main 层记录）。

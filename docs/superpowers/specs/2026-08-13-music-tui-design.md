# music-tui 设计文档

## 1. 项目概述

一个终端（TUI）音乐播放器，基于 YouTube 搜索播放，支持同步歌词展示、播放历史、播放队列与命名播放列表。包含五个页面：

- **首页**：展示当前播放内容（封面、标题、歌手）、可滑动进度条、播放控制、同步歌词、队列位置与模式
- **队列页**：播放队列列表（当前曲高亮 + 序号），跳转播放/删除/清空/切换顺序随机
- **播放列表页**：命名播放列表管理（两级视图：概览 ↔ 详情），新建/重命名/删除/移除歌曲，整列表加载进队列播放；全局 p 键把选中歌曲添加到列表
- **搜索页**：搜索输入条 + YouTube 搜索结果列表（Enter 播放 / a 加入队列）
- **历史页**：最近播放记录，可重播/删除/清空（a 加入队列）

## 2. 目标与非目标

### 目标
- 搜索 → 播放 → 歌词 → 历史全链路可用
- 播放进度实时刷新（~50ms），歌词随进度逐行高亮
- 架构支持适配器扩展（后续可加中文音乐源）

### 非目标（YAGNI，第一版不做）
- 音量控制
- 中文音乐源适配器（网易云/QQ）
- 除首页外的封面图
- 歌词手动偏移调整
- MPV_PATH 等路径覆盖配置
- 搜索历史
- 图片终端的特殊优化（go-termimg Auto 自动探测即可）

## 3. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言/框架 | Go + bubbletea + bubbles + lipgloss | 组件库全覆盖（list/textinput/progress/viewport），并发内建，单二进制 |
| 播放后端 | mpv 进程 + JSON IPC | observe_property 约 50ms 推送进度，pause/seek 全支持，音频输出系统音响 |
| 搜索源 | YouTube（yt-dlp 子进程） | 无需 API key，ytsearch 搜索 + bestaudio 取流 |
| 歌词 | lrclib API | 免费无 key，/api/get 按时长 ±2s 精确匹配，标准 LRC 格式 |
| 封面渲染 | github.com/blacktop/go-termimg | Auto 模式自动探测 kitty/sixel/iTerm2，半块字符兜底，兼容所有终端 |
| 历史存储 | JSON 文件 | 数据量小，简单可靠 |
| mpv IPC 库 | dexterlb/mpvipc（或自写 JSON IPC，实现时定） | 成熟、活跃 |

### 外部依赖与检测策略
- 运行时依赖：**mpv**、**yt-dlp**，均需在 PATH 中
- 启动时 `exec.LookPath` 检测，缺失即报错退出，按平台打印安装命令：
  - Linux: `sudo apt install mpv yt-dlp`（dnf/pacman 同理）
  - macOS: `brew install mpv yt-dlp`
  - Windows: `winget install mpv` + pip 安装 yt-dlp
- 不做自动下载（Linux 无官方预编译二进制）、不做路径覆盖配置

## 4. 模块结构

```
music-tui/
├── main.go              # 入口：检测 mpv/yt-dlp → 启动 mpv 进程 → 组装各服务 → 启动 TUI
├── model/               # 共享数据结构：Track、PlaybackState（无依赖，被所有模块引用）
├── queue/               # 播放队列纯逻辑：顺序/随机、自动连播推进（只依赖 model）
├── session/             # 播放会话（队列+进度）JSON 持久化：续播恢复
├── ui/
│   ├── root.go          # 顶层 Model：页面切换（Tab）、全局按键、全局播放状态、队列编排
│   ├── home.go          # 首页：封面、歌曲信息、可滑动进度条、播放控制、同步歌词
│   ├── search.go        # 搜索页：搜索输入框、结果列表、加载/错误态
│   ├── history.go       # 历史页：历史列表、重播、删除、清空
│   ├── queue.go         # 队列页：队列列表（当前曲高亮）、跳转播放、删除、清空、模式切换
│   └── playlists.go     # 播放列表页：概览/详情两级视图、命名输入、p 键选择器
├── player/
│   ├── player.go        # Player 接口 + 事件类型
│   └── mpv.go           # mpv 实现：进程管理 + JSON IPC（socket 长连接、observe_property 推送）
├── search/
│   ├── search.go        # SearchAdapter 接口
│   └── youtube.go       # YouTube 适配器：yt-dlp 搜索 + 元数据提取
├── lyrics/
│   ├── client.go        # lrclib HTTP 客户端（/api/get → /api/search 两级查找）
│   └── lrc.go           # LRC 解析器 + 当前行计算（二分查找）
├── cover/
│   └── cover.go         # 封面获取与缓存：异步下载、404 降级链、磁盘/内存缓存
├── history/
│   └── history.go       # 历史记录：JSON 读写、去重置顶、100 条上限、清空
└── playlists/
    └── playlists.go     # 播放列表存储：多列表 CRUD、原子 JSON 持久化、损坏报错
```

### 依赖方向
- `main` 负责组装；`ui` 只依赖各服务的**接口**（Player、SearchAdapter），不依赖具体实现
- `queue` 只依赖 `model`，不依赖 `player` 与任何 IO（纯逻辑，可单测）
- `session` 只依赖 `queue` 与 `model`（JSON 持久化，原子写盘）
- `player / search / lyrics / cover / history` 只依赖 `model`，互不依赖
- `model` 为叶子包，不含逻辑

## 5. 核心数据结构

```go
// model/track.go
type Track struct {
    ID       string  // 来源内唯一 ID（YouTube video ID）—— 历史去重依据
    Title    string
    Artist   string
    Duration float64 // 秒
    URL      string  // 可直接交给 mpv 播放的地址（YouTube 页面 URL，mpv 内置 yt-dlp 解析取流）
    Source   string  // "youtube"（未来扩展 "netease" 等）
    CoverURL string  // 封面图 URL（maxresdefault 优先）
}

// model/playback.go —— 全局播放状态（root model 持有，页面共享）
type PlaybackState struct {
    Track    *Track  // nil = 未播放
    Position float64 // 秒
    Duration float64 // 秒
    Playing  bool
}

// player/ —— 事件类型（goroutine 从 mpv socket 读出，经 channel 注入 UI）
type Event interface{ isEvent() }
type ProgressEvent struct{ Position, Duration float64 } // ~50ms/次
type StateEvent struct{ Playing bool }                  // 暂停/恢复
type TrackStartedEvent struct{ Duration float64 }       // 新歌开始
type TrackEndedEvent struct{}                           // 播放结束
type ErrorEvent struct{ Err error }                     // 播放器异常

// lyrics/ —— 歌词结构
type LyricLine struct {
    Time float64 // 秒，该行开始显示的时刻
    Text string
}
type Lyrics struct {
    Lines []LyricLine // 按时间升序
    Plain string       // 无同步歌词时的纯文本兜底
}
// 同步算法：二分查找 "时间戳 ≤ 当前进度" 的最后一行
func (l *Lyrics) LineAt(pos float64) (idx int, text string)

// history/ —— 历史记录
type Entry struct {
    Track    model.Track // 嵌入完整 Track，重播无需再搜索
    PlayedAt time.Time
}
// 存储：JSON 文件（os.UserConfigDir()/music-tui/history.json）
// 规则：同 ID+Source 去重（更新 PlayedAt 并置顶）；上限 100 条，超出裁剪
```

### 关键接口

```go
type Player interface {
    Play(url string) error
    Pause() error
    Resume() error
    Seek(seconds float64) error
    Events() <-chan Event
}

type SearchAdapter interface {
    Search(ctx context.Context, query string) ([]model.Track, error)
}
```

## 6. 事件流设计

- `player` 包内一个 goroutine 长连 mpv socket（`--idle=yes --input-ipc-server=<socket>` 启动），`observe_property` 实时推送进度
- 通过 bubbletea `Program.Send()` 把事件注入 UI（官方 channel 注入模式），UI 侧无阻塞
- 订阅与连接绑定，保持长连接；退出时发送 quit 命令并清理 socket 文件

## 7. 页面布局与交互流程

### 顶部 Tab 标签栏（所有页面共用，占首行）

```
⏵ 首页   队列 (N)   播放列表   搜索   历史
```

- 当前页标签加粗粉色高亮（同歌词高亮行/队列当前曲目），其余弱化（faint）
- 首页标签带播放状态图标：`⏵` 播放中 / `⏸` 已暂停 / `⏹` 未播放（无曲目）
- 队列标签带数量（`队列 (5)`），空队列不带数量
- `Tab`/`Ctrl+→`/`Shift+Tab`/`Ctrl+←`（或 `1`/`2`/`3`/`4`/`5`）切换时高亮同步移动
- 鼠标支持（程序已启用鼠标捕获）：
  - 点击标签切换页面；点击 Tab 栏分隔/空白、或页面区域不拦截（歌词区 viewport 原生支持滚轮滚动；bubbles v1.0.0 列表暂无鼠标处理）
  - 悬停非当前页标签显示下划线提示，移出 Tab 栏清除；当前页高亮优先
  - 终端文本拖选需按住 Shift（AltScreen 鼠标捕获的通用行为）

### 首页（左右布局）
```
┌─────────────┬──────────────────────────────┐
│             │  晴天 - 周杰伦                 │
│   [封面图]  │  YouTube · 04:29              │
│             │  ━━━━━━━━━━━━●━━━━  02:31/04:29│
│             │  ⏸ 播放中                     │
├─────────────┴──────────────────────────────┤
│  歌词（当前行高亮，随进度自动滚动）           │
└────────────────────────────────────────────┘
```

### 搜索页
```
搜索: [输入关键词........]
────────────────────────────────
 1. 晴天        周杰伦    04:29
 2. 晴天        (翻唱)   03:52
 ...                  （↑↓ 选择 · Enter 播放）
```

### 历史页
```
最近播放 (3/100)
────────────────────────────────
 1. 晴天        周杰伦    昨天 20:31
 2. 七里香      周杰伦    昨天 19:02
 ...                  （Enter 重播 · d 删除 · c 清空）
```

### 键位约定

| 作用域 | 键 | 动作 |
|---|---|---|
| 全局 | `Tab` / `Ctrl+→` / `Shift+Tab` / `Ctrl+←`（或 1/2/3/4/5） | 切换首页/队列/播放列表/搜索/历史：`Tab`/`Ctrl+→` 下一页循环，`Shift+Tab`/`Ctrl+←` 上一页循环，1/2/3/4/5 直达 |
| 全局 | `p` | 弹出"添加到播放列表"选择器，把当前选中歌曲加入播放列表（无选中歌曲时提示；输入框聚焦时为输入字符） |
| 全局 | `空格` | 暂停/继续 |
| 全局 | `q` / `Ctrl+C` | 退出（关闭 mpv、保存历史） |
| 首页 | `←` / `→` | 进度条滑动 seek（-5s / +5s，可按住） |
| 搜索页 | `Enter` | 搜索 / 播放选中项 |
| 历史页 | `Enter` / `d` / `c` | 重播 / 删除单条 / 清空 |
| 播放列表页 | `Enter`/`n`/`r`/`d`/`a`/`Esc`/`←` | 概览：查看/新建/重命名/删除；详情：整列表播放/加入队列/移除歌曲/返回概览 |

### 核心链路 1：搜索 → 播放

```
搜索页输入关键词 → Enter
  → Cmd 异步调 SearchAdapter（yt-dlp ytsearch:20 + dump-json，10s 超时）
  → 结果列表展示（加载中 / 空态 / 错误态）
  → ↑↓ 选中 → Enter
  → ① 切到首页，PlaybackState 置为播放中
  → ② 并行触发四个动作：
       player.Play(track.URL)     mpv loadfile，进度事件开始推送（~50ms）
       lyrics.Fetch(track)        lrclib /api/get → 404 降级 /api/search → 解析 LRC
       cover.Fetch(track)         封面异步下载（不阻塞 UI）
       history.Add(track)         去重置顶 + 写盘
```

### 核心链路 2：播放状态流

```
mpv 进度事件（50ms）→ root 更新 PlaybackState.Position
  → 首页进度条刷新 + 歌词区二分查找当前行（LineAt）→ 高亮滚动
暂停/恢复事件 → 更新 ⏸/⏵ 状态
播放结束事件 → 停止在当前位置，等待用户操作（第一版无队列，不自动连播）
```

### 核心链路 3：历史重播

```
历史页选中 → Enter
  → player.Play(entry.Track.URL)（无需再搜索，Track 完整嵌入）
  → 切首页 → 歌词 + 封面重新加载（与链路 1 的 ② 相同，跳过搜索）
```

## 8. 封面图方案

- `Track.CoverURL`：yt-dlp `--dump-json` 的 thumbnail 字段（maxresdefault 优先）
- 渲染：go-termimg `ImageWidget`，Auto 模式自动探测终端协议（kitty → sixel → iTerm2 → 半块字符兜底）
- 下载管线（cover 包）：
  - goroutine 异步下载，完成后 tea.Msg 通知 UI，**绝不阻塞 Update()**
  - 404 降级链：maxresdefault → sddefault → hqdefault → mqdefault
  - 磁盘缓存 `os.UserCacheDir()/music-tui/covers/<videoID>.jpg`，避免重复下载
  - 内存缓存当前封面渲染结果，避免每帧重渲染
  - 失败不阻塞播放，显示占位框

## 9. 歌词同步机制

- 歌词加载异步（网络请求），期间歌词区显示"歌词加载中…"
- 拉取流程：`GET /api/get?track_name&artist_name&album_name&duration`（±2s 容差精确匹配）→ 404 时降级 `GET /api/search` 按 duration/artist 筛选最佳匹配
- 必须设置 User-Agent 头（`应用名 版本 (主页/邮箱)` 格式）；429 限流按 Retry-After 等待
- 同步算法：LRC 每行时间戳 = 该行开始显示时刻；给定进度 t，当前行 = 时间戳 ≤ t 的最后一行（二分查找）
- 三种展示态：同步歌词（高亮滚动）/ 纯文本歌词（整页展示）/ 无歌词（提示）

## 10. 错误处理矩阵

| 场景 | 处理 |
|---|---|
| mpv / yt-dlp 缺失 | 启动即报错退出，按平台打印安装命令 |
| 搜索失败 / 超时（10s） | 搜索页显示错误 + 重试；空结果显示"无结果" |
| 播放失败（URL 失效等） | mpv ErrorEvent → 首页显示错误，播放状态复位 |
| 歌词 404 / 网络失败 | 显示"暂无歌词"，**不影响播放**；429 按 Retry-After 等待 |
| 封面降级链全失败 | 显示占位框，**不影响播放** |
| mpv 进程崩溃 / socket 断开 | 首页报错 + 播放状态复位 |
| 历史写盘失败 | 记录日志，不阻塞播放 |
| 异常进度值（Position > Duration） | 过滤丢弃 |
| seek / 歌词时间戳越界 | clamp 到合法范围 |

核心原则：**歌词、封面、历史均为"尽力而为"附属链路，任何失败不影响播放主链路**。

## 11. 测试策略

| 模块 | 策略 |
|---|---|
| lyrics | LRC 解析器纯函数单测（多时间戳行、毫秒/百分秒格式、排序）+ lrclib 客户端 httptest mock |
| history | 去重置顶、100 条裁剪逻辑单测（临时文件） |
| cover | 降级链单测（httptest 模拟 404/200） |
| search | yt-dlp 输出解析与子进程解耦，解析函数用假 JSON 输出单测 |
| player | fake mpv socket server 模拟 IPC 回复，测协议解析 |
| ui | bubbletea tea.Program 集成测试：注入按键消息，断言页面状态切换 |

## 12. 后续扩展点（本期不实现）

- 中文音乐源适配器（实现 SearchAdapter 即可接入）
- 音量控制
- 搜索历史
- chafa 等外部工具增强封面画质

## 13. MPRIS 协议支持（追加需求，主体完成后实现）

### 13.1 概述
- 支持 Linux 桌面媒体控制协议 MPRIS（D-Bus），使桌面媒体键、playerctl、GNOME/KDE 媒体控件能控制播放器
- 仅 Linux 编译；非 Linux 平台提供 no-op 桩，不影响构建
- 技术路线：godbus/dbus v5（v5.2.2，2025 年末恢复活跃维护）+ godbus/prop 包手写服务端（无成熟现成库，社区共识路线；参考 mpd-mpris、go-musicfox）

### 13.2 模块结构
```
mpris/
├── mpris_linux.go        # //go:build linux：D-Bus 服务端实现
└── mpris_unsupported.go  # //go:build !linux：no-op 桩
```
- 服务名：org.mpris.MediaPlayer2.music-tui；对象路径：/org/mpris/MediaPlayer2
- 实现接口：org.mpris.MediaPlayer2（根）与 org.mpris.MediaPlayer2.Player

### 13.3 数据流（双向，复用 player 事件流）
播放器 → D-Bus（订阅 player.Events()）：
- ProgressEvent（~50ms）→ props.Set 静默更新 Position（EmitFalse，微秒单位，无总线流量）；检测到进度跳变 >2s（用户/外部 seek）→ conn.Emit 发 Seeked 信号
- StateEvent → PlaybackStatus（Playing/Paused/Stopped）+ Rate 同步
- TrackStartedEvent → Metadata 整包替换：mpris:trackid（/org/mpris/MediaPlayer2/TrackList/<id>）、mpris:length（微秒）、xesam:title、xesam:artist（as 数组）、mpris:artUrl（封面 URL）
- TrackEndedEvent → PlaybackStatus=Stopped；Metadata 保留最后曲目

D-Bus → 播放器（方法转调 Player 接口）：
- Play / Pause / PlayPause / Seek(offset) / SetPosition(校验 trackId 匹配后 Seek(abs-current))
- Stop → Pause + Seek(0)（无队列时最接近停止语义）
- Next / Previous：CanGoNext=CanGoPrevious=false，方法返回 NotSupported（第一版无播放队列）
- OpenUri：返回 NotSupported
- 错误统一返回 *dbus.Error

属性清单：
- PlaybackStatus / Position / Metadata / Rate（固定 1.0，Min=Max=1.0）/ LoopStatus（固定 None）/ Shuffle（false）/ Volume（读写，映射 mpv volume）
- CanControl / CanPlay / CanPause / CanSeek = true；CanGoNext / CanGoPrevious / CanQuit / CanRaise / HasTrackList = false

### 13.4 降级策略
- 先检查 DBUS_SESSION_BUS_ADDRESS 或使用 SessionBusPrivateNoAutoStartup（避免 godbus 自动 dbus-launch 泄漏进程）
- 连接失败 / RequestName 失败（被占用、无权限）→ log 警告并禁用 MPRIS，绝不影响播放器主功能

### 13.5 测试策略
- Seeked 判定、Metadata 构建、PlaybackStatus 映射等纯逻辑抽成纯函数单测
- D-Bus 集成测试依赖真实 session bus，CI 中跳过

### 13.6 与主实现的衔接
- 依赖主体完成的 player 包（Events() 通道、Play/Pause/Resume/Seek 接口）与 model 包（Track 含 CoverURL）
- 主体 10 个 Task 完成后，追加实现计划 Task 11 执行本章节

## 14. 播放队列（追加需求，主体完成后实现）

### 14.1 概述
- 第 2 个 Tab（队列页）+ 播放队列：顺序/随机播放、TrackEnded 自动连播
- 手动播放统一走**替换语义**：清空队列 → 该曲入队为当前（currentIdx=0）→ 播放
- 搜索页/历史页 `a` 键**追加**到队尾，不打断当前播放
- 队列页 Enter 走**跳转语义**：保留队列其余曲目，仅把当前指针移到所选曲目并播放

### 14.2 queue 包（纯逻辑，无 IO，只依赖 model）

```go
type Mode int
const (
    Sequential Mode = iota // 顺序播放
    Shuffle                // 随机播放
)

type Queue struct { tracks []model.Track; currentIdx int; mode Mode }

func New() *Queue
func (q *Queue) Add(t model.Track)         // 追加队尾；不设当前、不自动播放
func (q *Queue) Replace(t model.Track)     // 替换语义：清空 + 入队 + currentIdx=0
func (q *Queue) JumpTo(i int) bool         // 跳转语义：仅移动当前指针，保留队列
func (q *Queue) Remove(i int)              // 删当前曲顺延下一首；末位当前被删 → 无当前
func (q *Queue) Clear()                    // 清空；模式保留（用户偏好）
func (q *Queue) Next() (model.Track, bool) // 推进下一首；播完停止不循环；无当前时从头
func (q *Queue) SetMode(m Mode)            // 切随机只洗牌"当前曲之后"，不打断当前播放
func (q *Queue) Current() (model.Track, bool)
func (q *Queue) Tracks() []model.Track     // 副本，供 UI 展示
func (q *Queue) CurrentIndex() int         // -1 = 无当前曲目（UI 高亮）
func (q *Queue) Mode() Mode
func (q *Queue) Len() int
```

随机语义：`SetMode(Shuffle)` 时**一次性洗牌**当前曲之后的曲目，洗牌后数组顺序 = UI 显示顺序 = 实际播放顺序（所见即所播）；切回顺序保持当前数组顺序。顺序/随机播完列表均停止（不循环）。

### 14.3 播放驱动（ui/root 编排，复用现有事件流）

- `TrackEndedEvent`：原为"停止"，改为 `queue.Next()` → 有下一首则 `player.Play()` 继续播放（同步刷新首页/队列页，**不切换当前页面**），否则停止等待用户操作。删除当前曲目后（队列指针已顺延、mpv 仍播被删曲目）播完时**不推进**，直接播放顺延曲目；删除末位当前曲（无顺延）播完时从头开始
- 手动播放（搜索 Enter / 历史 Enter / 空格重播）统一走 `startPlay`：`queue.Replace(track)` + `player.Play()`；成功并行触发 歌词/封面/历史 三个异步 cmd（与第 7 章核心链路 1 相同）；连播曲目同样写入历史
- 队列页 Enter：`queue.JumpTo(index)` + 播放当前曲目（保留队列）
- 自动连播失败（Play 报错）走既有失败路径：重置状态 + 错误横幅，队列指针停在失败曲目

### 14.4 交互与键位

| 作用域 | 键 | 动作 |
|---|---|---|
| 全局 | `Tab`/`Ctrl+→`/`Shift+Tab`/`Ctrl+←`（或 1/2/3/4/5） | 切换首页/队列/播放列表/搜索/历史（循环） |
| 搜索页 | `Enter` | 搜索 / 替换语义播放选中项 |
| 搜索页 | `a` | 选中项追加到队尾（输入框聚焦时为普通字符） |
| 历史页 | `a` | 选中记录追加到队尾 |
| 队列页 | `↑↓` | 选择（默认跟随当前曲目，删除/追加后按 ID 保持选择） |
| 队列页 | `Enter` | 跳转播放选中曲目（保留队列） |
| 队列页 | `d` / `c` | 删除选中项（删当前曲顺延下一首）/ 清空队列 |
| 队列页 | `s` | 切换顺序/随机 |
| 首页 | — | 播放信息区显示 `位置/总数 · 模式`（如 `3/12 · 随机`） |

队列页展示：当前曲目 `▶` 标记 + 加粗高亮，全部条目带序号（`▶  1. 晴天`）；空队列显示空态提示。删除/清空不打断正在播放的 mpv（播完由 Next 自然衔接）。

### 14.5 测试策略

| 模块 | 策略 |
|---|---|
| queue | 纯函数单测：Add/Replace/JumpTo/Remove 四象限、Next 顺序推进与停止、SetMode 洗牌性质（集合不变/当前与前缀不动/显示序=播放序）、Clear 保留模式 |
| ui | tea.Program + update 驱动集成测试：Tab 五页循环（首页→队列→播放列表→搜索→历史）、Enter 替换语义、a 追加不打断、TrackEnded 自动连播与不切页、队列页跳转/删除/清空/模式切换、首页位置显示 |

## 15. 续播（播放会话持久化，追加需求，用户确认方案 B）

### 15.1 概述
- 记住播放队列（曲目 + 当前项 + 顺序/随机模式）与当前曲目进度；关闭后再次打开恢复为**暂停态**（不自动出声），按空格继续播放
- 保存时机（方案 B，用户已确认）：**退出时保存** + **播放中每 5 秒节流自动保存**（崩溃/断电也可恢复）
- 存储：`~/.config/music-tui/session.json`（与 history.json 同目录），原子写盘；损坏时备份后重建（与 loadHistory 同款降级，不阻止启动）

### 15.2 模块结构
```
session/
└── session.go        # Store：Load（不存在=无会话）/ Save（原子写盘）/ Clear / State
```

```go
// queue 包新增（纯逻辑）
type Snapshot struct {
    Tracks     []model.Track
    CurrentIdx int
    Mode       Mode
}
func (q *Queue) Snapshot() Snapshot          // 副本，修改不影响队列
func (q *Queue) Restore(s Snapshot)          // CurrentIdx 越界降级为 -1（防损坏数据）

// session 包
type State struct {
    Queue    queue.Snapshot // 队列快照
    Position float64        // 当前曲目进度（秒）
    Ended    bool           // 退出时当前曲目是否已播完
}
func NewStore(path string) (*Store, error)
func (s *Store) Save(st State) error
func (s *Store) State() *State   // nil = 无会话
func (s *Store) Clear() error    // 删除文件（无播放中曲目时退出调用）
```

### 15.3 播放器：静默加载（不发声）
- `Player` 接口新增 `PlayPaused(url string) error`：mpv 先 `set pause=true` 再 `loadfile`（pause 属性不随文件重置），新文件加载后保持暂停；随后 `Seek(position)` 定位到退出时进度
- 恢复路径**不写历史**（恢复的是已播放过的曲目）；恢复曲目经 `onTrack` 同步给 MPRIS

### 15.4 恢复流程（启动时）
```
main 加载 session.Store（损坏→备份重建）→ NewModel：
  → session 有状态且队列有当前曲目：
      queue.Restore(快照)
      state = {Track: 当前曲, Position: 保存进度, Duration: 曲目元数据, Playing: false}
      home 同步（进度条定位、歌词置加载中）
      Init 返回 resumeCmd：PlayPaused(url) → Seek(pos) → resumeResultMsg
      → 成功后异步加载歌词/封面（暂停态也可展示）
      → 失败（IPC 或 mpv 异步取流）：状态重置 + 错误横幅，**队列保留展示**
        （用户可查看/跳转播放其他曲目）；磁盘会话保留（下次启动重试，
        用户播放新曲或退出时自然覆盖/清除）
  → session 无状态 / 队列无当前曲目（损坏/手改）：从空态开始
恢复的 ended 语义：
  → Ended=false：暂停在保存进度
  → Ended=true 且有下一首：暂停在下一首开头（position=0）
  → Ended=true 且无下一首：当前曲从头（position=0）
```

### 15.5 保存流程
- **退出**（q / Ctrl+C）：同步保存（队列快照 + Position + ended）
- **播放中**：ProgressEvent 更新时按 `saveInterval=5s` 节流保存（lastSave 记录）
- **无播放中曲目时退出**：删除会话文件（会话自然结束，避免下次恢复陈旧状态）
- 保存失败仅记日志，不影响播放主链路（尽力而为）

### 15.6 已知问题与修复记录
- **恢复定位竞态（已修复）**：初版恢复流程为 `PlayPaused(url)` + 单独 `Seek(pos)`：loadfile 是异步的（yt-dlp 解析网络曲目需数秒），加载完成前 seek 被 mpv 拒绝（`error running command`）→ 恢复必然失败。修复（a7573e1）：`PlayPaused(url, start)` 用 mpv ≥0.38 的 4 参 loadfile 语法 `loadfile <url> replace -1 start=<pos>` 随加载原子定位；start=0 时用 2 参语法兼容旧版
- **tmux 下按键全部失效（已修复，32b4b3b）**：go-termimg 的 CSI 查询（14t/16t/sixel）与 TerminalQuerier 用无超时 goroutine 读 /dev/tty；Go 的 `SetReadDeadline` 在 `term.MakeRaw` 之后的 fd 上失效（poller 与 termios 交互问题，实测永久阻塞），fd 关闭不中断阻塞中的 read → 泄漏 goroutine 永久占用终端读取，与 bubbletea 抢同一终端、吞掉后续全部按键（封面渲染后空格/q/数字键均失效）。修复：third_party 本地副本（go.mod replace），查询改 `syscall.Select` 手动超时 + 同步读；home.go 在 tmux 下强制 Halfblocks 字符模式
- **实测注意**：YouTube 风控（403）为间歇性环境问题，恢复失败时队列保留展示（见 15.4），不阻塞用户查看/跳转

### 15.6 测试策略
| 模块 | 策略 |
|---|---|
| queue | Snapshot/Restore roundtrip、副本隔离、越界 CurrentIdx 降级 |
| session | 缺失=无会话、Save/State roundtrip（重启读回）、覆盖写、Clear、损坏报错、空文件 |
| player | fake mpv socket 断言命令序列：set pause=true → loadfile；超时/未连接表驱动扩展 |
| ui | 恢复：队列/模式/进度/暂停态断言、PlayPaused+Seek 调用、ended 两分支、失败重置、MPRIS onTrack 通知；保存：退出写盘、ProgressEvent 节流、无播放清除 |
| main | loadSession 缺失/损坏备份重建（与 loadHistory 同款） |

## 16. 播放列表（追加需求，主体完成后实现）

### 16.1 概述
- 第 3 个 Tab（播放列表页）+ 命名播放列表：多列表管理（新建/重命名/删除）、歌曲增删、整列表加载进队列播放、全局 p 键快速添加
- Tab 栏重排为 5 页：**首页(1) → 队列(2) → 播放列表(3) → 搜索(4) → 历史(5)**；数字键 1-5 直达，`Tab`/`Ctrl+→` 正向循环、`Shift+Tab`/`Ctrl+←` 反向循环
- 播放列表页为**两级视图**：概览（列表名 + 歌曲数 + 创建时间）↔ 详情（歌曲列表）
- 持久化：`~/.config/music-tui/playlists.json`（与 history/session 同目录），原子写盘，损坏降级与 history 同款

### 16.2 playlists 包（纯存储，只依赖 model）

```go
type List struct {
    Name      string        // 列表名（同名列表不允许，创建/重命名冲突报错）
    Tracks    []model.Track // 歌曲列表（保序；允许同一首歌重复添加）
    CreatedAt time.Time     // 创建时间（概览展示 "MM-DD 创建"）
}

type Store struct { ... } // 并发安全（mutex）；文件不存在视为空

func NewStore(path string) (*Store, error)            // 损坏/读失败返回错误（main 备份重建）
func (s *Store) Lists() []List                        // 全部列表副本（创建顺序）
func (s *Store) Create(name string) (List, error)     // 空白名/重名报错
func (s *Store) Rename(oldName, newName string) error // 旧名不存在/新名空白或重名报错
func (s *Store) Delete(name string) error             // 列表不存在返回 nil
func (s *Store) AddTrack(name string, track model.Track) error
func (s *Store) RemoveTrack(name string, index int) error // 下标越界报错
func (s *Store) Tracks(name string) []model.Track     // 副本；列表不存在返回 nil
```

- 命名策略：**同名列表不允许**（创建/重命名冲突报错，提示"已存在同名列表「xxx」"）；**同一首歌允许重复添加**（不做去重）
- 所有对外返回均为深拷贝副本（Tracks 切片隔离），外部修改不污染存储
- 每次变更立即原子写盘（见 16.6）

### 16.3 播放驱动（ui/root 编排）

- 播放列表详情 Enter → `plLoadMsg{name, index}` → `queue.ReplaceAll(tracks, index)`：**替换语义**——清空队列 → 整个列表入队 → 当前指针 clamp 到选中曲 → 切回首页 → 播放（与 startPlay 一致：重置重试预算、同步队列视图、歌词/封面/历史异步加载）
- 随机模式：ReplaceAll 保留当前模式；若为随机则**洗牌选中曲之后的尾部**（复用 SetMode 的 tail-shuffle 语义）——加载播放列表后随机直接可用，选中曲及之前的曲目保持原序不打断
- 详情 `a` → 复用追加语义（`emitTrackAppend`）：选中曲追加到队尾，不打断当前播放
- 数据流：root 持有 `*playlists.Store` 并执行全部 store 操作（页面不直接持有服务）；操作完成后 `setLists` 把最新数据推入页面——概览选中项按列表名尽量保持、列表收缩时 clamp 到邻近项；详情模式下当前列表被删除/重命名则自动退回概览
- 反馈横幅：失败走 `lastError`（红色 ⚠），成功提示走 `notice`（绿色 ✔，仅选择器场景使用）

### 16.4 交互与键位

| 作用域 | 键 | 动作 |
|---|---|---|
| 全局 | `Tab`/`Ctrl+→`/`Shift+Tab`/`Ctrl+←`（或 1/2/3/4/5） | 切换首页/队列/播放列表/搜索/历史（循环） |
| 全局 | `p` | 弹出"添加到播放列表"选择器（当前选中歌曲；无选中歌曲提示；输入框聚焦时为输入字符） |
| 播放列表页 | `Enter`/`n`/`r`/`d` | 概览：查看/进入详情、新建、重命名（预填旧名）、删除列表 |
| 播放列表页 | `Enter`/`a`/`d`/`Esc`/`←` | 详情：从选中曲播放整个列表、加入队列、移除歌曲、返回概览 |
| 播放列表页 | `Enter`/`Esc` | 命名输入：提交 / 取消 |

- 概览条目：`列表名` + 描述行 `N 首 · MM-DD 创建`；详情条目：`序号. 标题` + `歌手 · 时长`（序号样式与队列页一致）
- 空态提示：无列表 → "暂无播放列表，按 n 新建播放列表"；空列表详情 → "列表为空，在搜索/历史页选中歌曲后按 p 添加到播放列表"
- 命名输入：重命名预填旧名（光标在末尾）；提交成功退出输入框回到概览，失败（空白/重名）红字展示错误且**保留输入内容**便于修改；Esc 取消
- 删除/重命名后按名称保持选中；选中项消失时 clamp 到邻近项（参考 queue.sync 的保持逻辑）

### 16.5 选择器（全局 p 键）
- 作用域：**搜索页 / 历史页 / 播放列表详情**（有选中歌曲语义的页面）；首页/队列页无选中歌曲，按 p 提示"当前没有可添加的歌曲（请先在搜索/历史/播放列表页选中歌曲）"
- 输入框聚焦（搜索关键词/命名输入）时 p **让位为输入字符**（同空格/q 的处理）
- 浮层全屏替换页面内容（保留 Tab 栏与底部横幅）：列出全部播放列表 + 末尾固定"＋ 新建列表"入口（粉色加粗区分）
- `↑↓` 选择、`Enter` 确认、`Esc` 取消（Esc 后选择器关闭，不改动任何数据）
- 选择既有列表 → `AddTrack` → 关闭选择器 → 绿色横幅"✔ 已添加到「xxx」"（新按键分发时清除）
- 选择"＋ 新建列表" → 命名输入（占位"新列表名，Enter 创建并加入"）→ Enter 创建列表并加入当前曲目后关闭；空白名/重名错误红字展示（"⚠ 列表名不能为空"/"已存在同名列表「xxx」"），选择器不关闭、输入保留；Esc 返回选择列表

### 16.6 持久化与降级
- 路径：`~/.config/music-tui/playlists.json`（os.UserConfigDir()，与 history.json/session.json 同目录）
- 格式：JSON 数组（`MarshalIndent`），顶层为 `List` 数组，保持创建顺序
- 原子写：先写 `playlists.json.tmp` 再 `rename` 覆盖（每次 CRUD 变更即写盘）
- 损坏（崩溃/断电截断）降级（与 loadHistory/loadSession 同款）：`NewStore` 解析失败返回错误 → main 层把损坏文件备份为 `playlists.json.corrupt-<纳秒时间戳>` 并重建空 store，应用正常启动；备份失败（如权限问题）按原样返回错误
- 空文件视为空列表；父目录不存在时自动创建

### 16.7 测试策略
| 模块 | 策略 |
|---|---|
| playlists | 15 个单测（临时目录）：目录创建与空 store、Create/重名与空白拒绝、Rename（含保留歌曲）、Delete、AddTrack/RemoveTrack（含越界）、持久化重载、损坏文件报错、空文件、Lists/Tracks 副本隔离、并发访问 -race、CreatedAt 稳定 |
| ui | playlists_ui_test.go 19 个集成测试：空态、新建/重命名/删除流程、详情移除与 a 入队、详情 Enter 整列表加载播放（替换队列 + 切首页）、命名输入吞全局键（p/空格/q）、p 键选择器全路径（搜索/历史添加、新建列表、重名错误、Esc 取消、无选中提示、输入框聚焦让位）、窗口尺寸下发、notice 清除 |
| queue | ReplaceAll 3 个测试：从指定下标填充、越界 clamp（负数/超长）、空列表清空（无当前曲） |

## 17. 音频缓存（追加需求，用户确认方案）

### 17.1 概述
- 缓存最近播放过的歌曲音频文件，默认上限 100 首，**LRU 淘汰**（按最后播放时间；超限淘汰最久未播放的并删除缓存文件）
- 播放时**后台异步缓存、不阻塞播放**：首次播放走网络流，同时后台下载副本到缓存目录；再次播放同一首（按 Track.ID）优先播放本地文件（秒开、省流量、断网可播）
- 配置：`~/.config/music-tui/config.json`（JSON 格式，用户确认；与 history.json/session.json 同目录同模式），**本阶段仅缓存一个配置项**；首次运行生成默认配置；缺失/损坏用默认值（损坏走 `.corrupt-<ts>` 备份重建，沿用 loadHistory 模式）
- 缓存目录：`~/.cache/music-tui`（os.UserCacheDir），索引 `index.json` 位于缓存目录内（删除整个目录即清空缓存）

### 17.2 配置文件（config 包）
```json
{
  "cache": {
    "enabled": true,       // 缓存总开关，默认开
    "max_entries": 100,    // 缓存歌曲数上限，默认 100；<1 时回落默认
    "dir": "/home/user/.cache/music-tui"  // 缓存目录，空/缺省时用默认
  }
}
```
- `config.Load(path)`：缺失 → 生成默认配置文件并返回默认值；空文件 → 默认值；损坏 JSON → 报错（main 备份 `.corrupt-<ts>` 后重建）；部分字段缺失 → 逐项回落默认
- 模块：`config/config.go`（Config/Cache 结构 + Load/Save/Default），原子写盘（tmp + rename）
- 依赖方向：`config` 依赖 `cache`（嵌入 `cache.Options`，缓存配置的唯一真源在 cache 包）

### 17.3 cache 包（LRU 管理 + 异步下载）
```
cache/
├── options.go    # Options{Enabled, MaxEntries, Dir}——配置结构真源（json tag 即配置文件格式）
├── index.go      # LRU 索引：Entry{ID, File, LastPlayed}，JSON 持久化（原子写盘）
├── name.go       # SafeName(id)：文件名安全化
├── download.go   # 下载器：yt-dlp 取直链 + http 下载 + 失败重试 1 次
└── cache.go      # Manager：并发安全门面 + in-flight 去重 + 启动清理
```
- **索引条目**：`Track.ID → {文件名, 最后播放时间}`；文件名 = `SafeName(ID)`（保留 `[A-Za-z0-9._-]`，其余转 `_`，空结果用 `unknown`）+ 可选扩展名（yt-dlp `--print "%(url)s %(ext)s"` 一次取直链与格式）
- **LRU 语义**：按 LastPlayed 升序排列，最旧在前；`Register`（下载完成/新文件入册）追加到最新，超限淘汰最旧并删除文件；`Lookup` 命中即刷新 LastPlayed（一次播放 = 一次刷新）；`Remove` 删文件+条目
- **下载流程**（CacheAsync，goroutine，不阻塞播放）：去重（同 ID 在途跳过）→ 总超时 5 分钟 → `yt-dlp -f bestaudio --no-playlist --no-warnings --print "%(url)s %(ext)s" <URL>` 取直链（60s 超时）→ http GET 直链，失败重试 1 次（2s 退避）→ 写 `.part` 临时文件 → rename 为正式文件 → Register → 超限淘汰。任何失败仅日志，不影响播放
- **并发安全**：Manager.mu 保护索引与 in-flight 集合；下载在锁外执行；-race 全绿
- **启动清理**（New 时）：条目文件缺失 → 删条目；超限 → 淘汰最旧（删文件）；有变化则持久化。孤儿文件（无条目）不清理
- **降级**：缓存目录不可用/索引损坏且备份重建失败 → `cache.Disabled()` 禁用态（Lookup 永 miss、CacheAsync no-op），警告日志，绝不影响播放

### 17.4 播放链路集成（ui/root + main）
- **beginPlay 单点接入**（手动/重试/连播统一入口）：
  - `cache.Lookup(track.ID)` 命中（索引有条目且文件存在）→ `player.Play(本地路径)`，标记 `playingFromCache=true`
  - 未命中 → `player.Play(track.URL)`，`playingFromCache=false`，随即 `cache.CacheAsync(track)` 后台下载（不阻塞、不进事件循环）
- **损坏缓存自动回退**：`LoadFailedError` 重试分支中若 `playingFromCache` → 先 `cache.Remove(id)` 删除损坏文件，重试自然走网络 URL；续播恢复失败（resumeResultMsg err / resuming 中 LoadFailed）同样处理
- **续播恢复**：`resumeCmd` 同样先查缓存，命中则 `PlayPaused(本地路径, pos)`（断网可恢复）；`resumeResultMsg` 携带 `fromCache` 标记回填 `playingFromCache`
- **后台下载生命周期**：播放结束/换曲**不取消**在途下载（LRU 按播放时间管理，下载完照常入册，超限自然淘汰）；goroutine 有总超时兜底 + in-flight 去重，不泄漏
- **main.go**：`loadConfig`（损坏备份重建，同 loadHistory 模式）→ `loadCache`（New 失败：索引损坏先备份重建，仍失败则 Disabled 降级）→ 传入 `ui.NewModel`

### 17.5 测试策略
| 模块 | 策略 |
|---|---|
| config | 缺失生成默认、部分字段回落、<1 上限回落、损坏报错、空文件默认、原子写盘 |
| cache | SafeName 边界；LRU 淘汰/命中刷新/Remove/Prune（缺文件清条目、超限淘汰）；索引 roundtrip/损坏报错；下载器 httptest（200/404/500 重试后成功/超时）、取直链注入 stub；Manager 并发 -race、in-flight 去重、Disabled 态 |
| ui | 命中播本地路径、未命中播 URL + 后台下载启动不阻塞、LoadFailed 损坏回退删条目重试走 URL、恢复命中 PlayPaused 本地路径 |
| main | loadConfig 缺失/损坏备份重建（同 loadHistory 模式） |

### 17.6 已知限制
- **LoadFailed 陈旧事件可能误删新曲目的有效缓存**：**主场景已修复，残留亚毫秒级窗口**——player 层按 playlist_entry_id 归属过滤陈旧 end-file error（mpv ≥0.33 的 end-file 事件与 loadfile 命令响应均携带 playlist_entry_id，且响应先于事件到达）：事件 id 与最近一次成功 loadfile 的 id 不一致 = 旧曲取流失败晚到，丢弃防 UI 按“当前曲目”误删健康缓存；旧版 mpv 无该字段时过滤禁用、保守放行（loadfile 响应无 id 同样保守放行）。**残差**：旧曲取流失败事件恰在切歌的 loadfile 命令处理前写出（亚毫秒级窗口）时仍可能被归属为当前曲，后果为缓存条目被删后可重新下载自愈；UI 层无法区分同代际事件，接受该残差。UI 层同步区分：IPC 层恢复失败不再删缓存（与缓存文件无关），真实损坏由异步 LoadFailedError 路径移除条目，提示区分“缓存文件损坏”与网络取流失败
- **多实例共享缓存目录**：与 index.json/session.json 同款单实例假设，多实例并发写索引的互相覆盖问题仍为已知限制

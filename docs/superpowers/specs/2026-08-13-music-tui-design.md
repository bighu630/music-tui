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

### 顶部 Tab 标签栏（所有页面共用，占第 2 行，首行留空）

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
> 注：歌词只接受带时间轴的同步歌词（用户追加要求，见 19.1）——原纯文本整页展示态已删除，本节按现状描述。

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
├── download.go   # 下载器：yt-dlp 直接下载（-o 模板落盘）+ 403 风控整进程重跑
└── cache.go      # Manager：并发安全门面 + in-flight 去重 + 启动清理
```
- **索引条目**：`Track.ID → {文件名, 最后播放时间}`；文件名 = `SafeName(ID)`（保留 `[A-Za-z0-9._-]`，其余转 `_`，空结果用 `unknown`）+ 实际扩展名（yt-dlp `-o "<SafeName>.%(ext)s"` 模板直接落盘，扩展名由 yt-dlp 产出决定，成功路径返回产物 basename）
- **LRU 语义**：按 LastPlayed 升序排列，最旧在前；`Register`（下载完成/新文件入册）追加到最新，超限淘汰最旧并删除文件；`Lookup` 命中即刷新 LastPlayed（一次播放 = 一次刷新）；`Remove` 删文件+条目
- **下载流程**（CacheAsync，goroutine，不阻塞播放）：去重（同 ID 在途跳过）→ 总超时 5 分钟 → `yt-dlp -f bestaudio --no-playlist --no-warnings -o "<SafeName>.%(ext)s" <URL>` 直接下载落盘（单次尝试 90s 子超时）→ 产物校验（非空、非 .part）→ Register → 超限淘汰。YouTube 对音频直链有**概率性 403 风控**（同一 CDN/同一客户端，换 URL 换结果；该错误 yt-dlp 内部不重试）——因此失败**整进程重跑** = 重新运行 yt-dlp = 重新提取新 URL，每次重跑都是新“彩票”；预算内最多 5 次（2s 退避，总超时内）。任何失败仅日志，不影响播放
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
| cache | SafeName 边界；LRU 淘汰/命中刷新/Remove/Prune（缺文件清条目、超限淘汰）；索引 roundtrip/损坏报错；下载器假 yt-dlp 脚本（成功落盘/失败 stderr 诊断/未产出/0 字节/.part 残留与清理）、403 整进程重跑重试（前 2 败第 3 胜）；Manager 并发 -race、in-flight 去重、Disabled 态 |
| ui | 命中播本地路径、未命中播 URL + 后台下载启动不阻塞、LoadFailed 损坏回退删条目重试走 URL、恢复命中 PlayPaused 本地路径 |
| main | loadConfig 缺失/损坏备份重建（同 loadHistory 模式） |

### 17.6 已知限制
- **LoadFailed 陈旧事件可能误删新曲目的有效缓存**：**主场景已修复，残留亚毫秒级窗口**——player 层按 playlist_entry_id 归属过滤陈旧 end-file error（mpv ≥0.33 的 end-file 事件与 loadfile 命令响应均携带 playlist_entry_id，且响应先于事件到达）：事件 id 与最近一次成功 loadfile 的 id 不一致 = 旧曲取流失败晚到，丢弃防 UI 按“当前曲目”误删健康缓存；旧版 mpv 无该字段时过滤禁用、保守放行（loadfile 响应无 id 同样保守放行）。**残差**：旧曲取流失败事件恰在切歌的 loadfile 命令处理前写出（亚毫秒级窗口）时仍可能被归属为当前曲，后果为缓存条目被删后可重新下载自愈；UI 层无法区分同代际事件，接受该残差。UI 层同步区分：IPC 层恢复失败不再删缓存（与缓存文件无关），真实损坏由异步 LoadFailedError 路径移除条目，提示区分“缓存文件损坏”与网络取流失败
- **多实例共享缓存目录**：与 index.json/session.json 同款单实例假设，多实例并发写索引的互相覆盖问题仍为已知限制
- **下载 403 风控耗尽预算**：整进程重跑最多 5 次仍失败 → 本次放弃缓存（仅日志），不阻塞播放；下次播放再触发 CacheAsync 重新下载自愈
## 18. YouTube Music 播放列表同步（追加需求）

### 18.1 概述
- 目标：把 YouTube Music（YTM）账号下**全部歌单**同步为本地播放列表——本地是远端歌单的**快照**，离线可播（复用第 16 章 playlists.Store 的 Create/AddTrack/RemoveTrack，不新增存储）
- 登录：三种 cookie 登录方式（浏览器自动导出 / cookies.txt 文件 / 粘贴 Cookie 字符串），无需 OAuth
- 导入：任意歌单 URL 可导入（公开歌单无需登录；私有歌单需已登录 cookie）
- 同步语义：命名前缀 `YT: `、按 videoId 歌单内去重、刷新 = 整列表替换（保留 CreatedAt）、命名冲突自动加 ` (2)` 后缀、SyncAll 单歌单失败不中断（errors.Join 汇总）
- 代码：`ytm/` 包（登录配置 + cookie 导出解密 + InnerTube browse 客户端 + 同步编排）+ search.FetchPlaylist（yt-dlp 拉取）+ ui 集成；实现 commit 5464c73 / 0539eb8 / 77a1d27

### 18.2 cookie 登录
- 三种方式（LoginMethod）：
  - `MethodBrowser` 浏览器自动导出：从 Chromium 系浏览器配置目录解密导出 YouTube 域 cookie（懒导出——每次同步前重新导出，覆盖式更新 CookiesPath）
  - `MethodCookiesFile`：用户指定 cookies.txt 完整路径（保存时校验文件可读）
  - `MethodPasted`：粘贴 Cookie header 字符串（`name=value; ...`）→ 校验转换后落盘 ytm-cookies.txt（0600）；**先校验后落盘**：非法文本（无 name=value 结构）报错且不写文件、不改配置（不破坏既有登录）
- 存储：`~/.config/music-tui/ytm.json`（权限 0600，.tmp+rename 原子写；损坏由 main 备份重建，与 playlists 降级同款）+ cookie 文件 `~/.config/music-tui/ytm-cookies.txt`（0600）；`LoginConfig{Method, Browser, CookiesPath, UpdatedAt}`
- 浏览器支持矩阵：
  - chrome / chromium / brave / edge / vivaldi / opera：**Linux + macOS** 支持（自研解密）；**Windows 不支持**（v20 app-bound 加密），返回 ErrUnsupportedOS，UI 提示改用 cookies.txt 方式
  - firefox / safari：不提供浏览器选项（UI 提示用 cookies.txt 导出）
- 失效处理：YouTube 登录 cookie 有效期约 6 个月；失效后 browse 请求 HTTP≥400 → 返回 ErrSessionInvalid（"登录已失效，请重新导出 cookie"）；是否有效由 VerifyLogin（= ListPlaylists 走完整登录判定）异步校验，UI 显示验证结果
- 退出登录：ClearLogin 清除登录配置（保留已落盘 cookie 文件与 SyncEntry 映射），UI notice "已退出 YT Music 登录"

### 18.3 InnerTube browse
- 端点：`https://music.youtube.com/youtubei/v1/browse?prettyPrint=false`，**无需 API key**（实测无 key 参数 200）
- browseId：**`FEmusic_liked_playlists`**（用户方案原写的 `FEmusic_library_playlists` 实测已废弃返回 400；ytmusicapi/yutemal 均用 liked_playlists，实测 200）——语义为"库中歌单"（Music Library 的歌单 tab）
- clientVersion 动态日期：`"1." + time.Now().UTC().Format("20060102") + ".01.00"`（ytmusicapi 同款，实测有效）
- 请求头：`Cookie`（完整 header）、`Authorization: SAPISIDHASH <ts>_<hex>`、`Origin`/`X-Origin: https://music.youtube.com`、`X-Goog-AuthUser: 0`、`Referer: https://music.youtube.com/`、Chrome 桌面 UA、`Content-Type: application/json`
- SAPISIDHASH 签名：`{unix_ts}_{hex(sha1(ts + " " + sapisid + " " + "https://music.youtube.com"))}`；sapisid 从 Cookie header 提取，**优先 `__Secure-3PAPISID` 再 `SAPISID`**（ytmusicapi 用 3PAPISID、yutemal 用 SAPISID，两者兼容）；cookie 中两者皆缺 → 未登录错误（"cookie 缺少 SAPISID"）
- 登录判定（实测）：
  - HTTP ≥ 400 → 参数错/风控/失效 → `ErrSessionInvalid`（带状态码）
  - 200 但 `serviceTrackingParams` 中 `logged_in: "0"` **或**解析无歌单条目 → 未登录 `ErrNotLoggedIn`
  - 网络失败 → 透传包装错误
- 响应解析：容错**递归扫描**全部 `musicTwoRowItemRenderer`（不依赖固定 JSON 路径，参考 yutemal extractor.go fromJSON 思路）；title = `title.runs[0].text`（simpleText 兜底）；count = subtitle runs 中首个连续数字段（"5 首"→5，无则 0）；browseId 去重保序（键排序保证发现顺序稳定）
- **分页（continuation）**：歌单库首页 `sectionListRenderer.contents[]` 末尾可能出现 `continuationItemRenderer`，令牌在 `continuationEndpoint.continuationCommand.token`（兼容直存 `continuation` 与 commandExecutorCommand 嵌套，参考 ytmusicapi continuations.py）；分页请求 body 加 `"continuation": "<token>"`（context 不变，**无 browseId**），响应条目在 `onResponseReceivedActions[0].appendContinuationItemsAction.continuationItems`；循环拉取直至无令牌（安全上限 100 页防死循环），跨页按 playlistId 去重——**>25 个歌单不漏同步**

### 18.4 Chrome cookie 解密
- **v10**（Linux/macOS 现行格式）：值 = `"v10"` + AES-128-CBC 密文；
  - key = PBKDF2-HMAC-SHA1(password, salt=`"saltysalt"`, iterations, keylen=16)，手写 PBKDF2（对照 RFC 6070 类向量单测）；Linux iterations=1，password 固定 `"peanuts"`（v10）+ `""`（empty 兜底）；**Linux 不读 Local State 的 `os_crypt.encrypted_key`**——该字段仅 Windows/DPAPI 使用，yt-dlp Linux 分支同样只用 peanuts/keyring（旧实现读取它会挤掉正确密钥导致解密失败，审查 M3 修复）
  - IV = 16 个空格字节 `b' ' * 16`；PKCS7 填充校验 + UTF-8 校验
  - **meta_version ≥ 24 时明文前 32 字节为 SHA256 前缀，须剥离**（meta_version 读 Cookies DB `meta` 表 `version` 字段；本机 Chrome 144 实测 = 24）
- **v11**（Linux keyring）：password 存 Secret Service（D-Bus schema `chrome_libsecret_os_crypt_password_v1`，item 属性 application = `<浏览器> Safe Storage`），经 godbus/dbus v5 读取；**实现但标注未实测**（本机无 v11 cookie），任何失败返回 nil 降级 empty key
- **macOS**：`security find-generic-password -w -a <account> -s "<account> Safe Storage"` 取钥匙串密码 → PBKDF2-HMAC-SHA1(password, "saltysalt", **iterations=1003**, 16) → 同 CBC 解密；**无版本前缀的旧明文 cookie 原样返回**（yt-dlp MacChromeCookieDecryptor 同款：other 前缀视为旧数据）
- SQLite 读取：**modernc.org/sqlite**（纯 Go 无 cgo、支持 WAL）；浏览器运行中只读直开失败时复制 DB + `-wal` + `-shm` 到临时目录再打开（同 yt-dlp 策略）；secure/httponly 列名兼容新旧版本（is_secure/is_httponly vs secure/httponly）
- 导出只取 youtube 域 cookie（youtube.com / .youtube.com / www / music 子域），写 Netscape 格式（0600，原子写）；错误可区分：浏览器未安装/未找到配置、解密失败（N 条失败计数）、无 YouTube 登录 cookie
- 验证：Linux 路径已用本机真实 cookie 对照 yt-dlp 解密结果验证通过

### 18.5 同步语义
- 命名：本地列表名 = `"YT: " + 歌单标题`；SyncEntry 映射（playlistId → ListName，upsert 按 PlaylistID）持久化在 ytm.json
- 去重：**按 videoId，同一歌单内**去重（保留首次出现顺序，空 ID 条目跳过）；不跨歌单去重
- 刷新 = 整列表替换：从尾到头逐个 RemoveTrack 清空再写入去重后曲目，**保留列表 CreatedAt**（通过 SyncEntry 映射识别刷新目标；映射存在但本地列表被删过 → 重建新建）
- 命名冲突：本地已存在同名列表且 SyncEntry 映射到本歌单 → 刷新该列表；否则追加 ` (2)`/` (3)` 递增
- SyncAll：枚举全部歌单（browse，含 continuation 分页）→ 逐个拉取去重入库；**单个失败记录错误继续**，返回成功 results + `errors.Join` 汇总错误；全部完成后 upsert 全部 SyncEntry
- SyncOne：按 playlistID 刷新单个同步列表（详情页 r）——**经 SyncEntry 映射直接构造歌单 URL 拉取（不走枚举）**，URL 导入的共享歌单（不在库中）也能刷新；映射不存在（本地手工改名/从未同步过）报「该列表不是 YT Music 同步列表」；刷新后 upsert 且 **ListName 保持原名**（远端标题变更不重建列表）；ImportURL：任意歌单 URL（公开无需登录；已登录时携带 cookie 文件）
- 超时：VerifyLogin 30s / **SyncAll 动态预算 = 30s 枚举余量 + 30s×歌单数（上限 10min，先枚举计数再拉取）** / 导入与刷新各 2min（root 异步 cmd）；yt-dlp 歌单拉取本身 30s（playlistTimeout）
- Track 映射见 18.6；与第 16 章关系：快照直接复用 playlists.Store，普通列表与 YT 同步列表共存，删除/重命名等既有操作不受影响

### 18.6 URL 导入
- 实现：`yt-dlp --flat-playlist -J --no-warnings [--cookies <file>] <url>`，输出**顶层 playlist JSON**：`{_type:"playlist", id, title, entries:[...]}`（实测；YTM 歌单会被 yt-dlp 按 youtube.com 处理属正常现象）
- 无 cookie 时**不加 cookie 参数**即可导入公开歌单；已登录时传 cookies 文件（File 优先，FromBrowser 保留备用）；**已配置登录但 cookie 不可用（浏览器导出失败/文件缺失）时上抛错误**（仅未配置登录才静默降级）
- Track 映射（flat 条目）：ID=videoId、Title、Artist=channel、Duration=duration、URL=`music.youtube.com/watch?v={videoId}`、CoverURL=thumbnails[0].url（flat 无 singular thumbnail 字段）、Source="youtube"
- 映射键：URL 的 `list` 参数作 PlaylistID；无 list 参数时用 `url:<URL>` 作映射键
- 错误分支与 Search 一致：超时/取消/非零退出（携带截断 stderr 诊断）分别给出不同消息

### 18.7 UI 与键位
- 播放列表页概览**顶部状态区**（列表上方，空列表时也显示）：
  - 未登录：`YT Music · 未登录`（faint：`s 登录设置 · u 导入歌单链接`）
  - 已登录：`YT Music · 已登录`（faint：`y 同步全部 · s 设置 · u 导入`）
  - **验证失败：`YT Music · 已登录（验证失败）`**（VerifyLogin 失败后降级展示，设置页当前状态行同步为「已登录（验证失败） · …」；初始启动未验证/验证成功维持「已登录」）
  - 同步中：`YT Music · 同步中…`（无提示行）
- 键位：

| 作用域 | 键 | 动作 |
|---|---|---|
| 播放列表概览 | `s` | 登录设置（三种方式 + 退出登录） |
| 播放列表概览 | `y` | 同步全部歌单（异步 SyncAll） |
| 播放列表概览 | `u` | URL 导入输入框 |
| 播放列表详情 | `r` | 刷新当前列表（仅 YT 同步列表；非同步列表红字提示"该列表不是 YT Music 同步列表"） |

- 登录设置（plSyncSetup，列表视图）：主菜单四项——`浏览器读取`（→ 二级浏览器列表：Google Chrome/Chromium/Brave/Microsoft Edge/Vivaldi/Opera，说明行"自动导出浏览器 cookie（Windows 请改用 cookies.txt）"）、`cookies.txt 文件路径`（输入框，Enter 校验可读 → 保存配置）、`粘贴 Cookie 字符串`（输入框，Enter 落盘 → 保存配置）、`退出登录`；当前状态行 `已登录 · <方式> · MM-DD HH:MM`
- 流程：浏览器/cookies/粘贴保存成功 → 回概览 + notice「已保存登录配置，验证中…」→ 异步 VerifyLogin → notice「YT Music 登录有效」或 lastError「登录无效/失效：请重新导出 cookie」（ytVerifyErrorText 映射）
- URL 导入：输入框（占位"粘贴 YouTube Music 歌单链接，Enter 导入"）→ 异步 ImportURL → notice「已导入「标题」N 首」/ lastError「导入失败: …」；**失败后输入不保留，需重新按 `u` 再试**（提交即退出输入模式）
- 同步全部：异步 SyncAll → notice「已同步 N 个歌单 · 共 M 首」/ lastError「同步失败: …」；单歌单失败不中断（errors.Join 汇总各失败原因，成功歌单照常入库并刷新列表视图 + 同步映射）
- 同步/导入/刷新期间 `m.ytSyncing = true` 禁用重复触发；消息：ytVerifyDoneMsg / ytSyncDoneMsg / ytImportDoneMsg / ytRefreshDoneMsg（root 产出），页面 emit ytLoginMsg / ytLoginFileMsg / ytLoginPasteMsg / ytLogoutMsg / ytSyncAllMsg / ytImportMsg / ytRefreshMsg
- main.go：loadYTM 加载 ytm.json（损坏 .corrupt-<纳秒> 备份重建，与 loadPlaylists 同款）→ `ytm.NewClient(store, searchAdapter)` → NewModel 追加 `yt *ytm.Client` 参数（nil = 未集成/测试降级）

### 18.8 测试策略
| 模块 | 策略 |
|---|---|
| ytm/crypt（9 个） | PBKDF2 两档迭代（对照已知向量）、v10 解密（Go 加密构造密文）、meta≥24 去前缀、empty key 兜底、坏数据不 panic、PKCS7 校验 |
| ytm/sapisid（9 个） | 3PAPISID 优先/SAPISID 兜底/缺失报错、签名确定性（固定 ts 期望 hex）、timestamp 格式 |
| ytm/cookies（14 个） | Netscape 解析（注释/HTTPONLY/空行/制表符）、写出 0600、浏览器探测错误路径、域过滤（只导出 youtube 域）、Local State 密钥读取 |
| ytm/browse（9 个） | httptest mock：请求头（Cookie/Authorization 前缀/Origin/UA）、body browseId 与 clientVersion 格式；响应解析（构造 gridRenderer JSON）；logged_in=0 → 未登录；HTTP 403/400 → 失效；网络错误 |
| ytm/config（14 个） | 存取 roundtrip、0600 权限、损坏文件报错、SyncEntry upsert/remove/find、原子写、粘贴登录落盘与 Netscape 转换 |
| ytm/sync（14 个） | fake Fetcher：新建/刷新（同名映射）/命名冲突加后缀/去重保序/部分失败继续（errors.Join）/ImportURL 无 cookie 路径/URL 映射键 |
| ytm/ytm（5 个） | Client 组装、SupportedBrowsers 矩阵、登录门面方法 |
| search（10 个） | FetchPlaylist：-J 顶层 playlist JSON 解析（含 URL 兜底/空条目/坏 JSON）、cookie 参数透传（假 yt-dlp 子进程）、超时/取消/stderr 诊断 |
| ui（19 个，playlists_ui_test.go 的 TestYT*） | 状态区三态渲染、登录设置全流程（浏览器选择→保存→验证消息、cookies.txt 可读校验、粘贴登录、失败映射）、退出登录、URL 导入（成功/空忽略/失败保留输入）、同步全部（成功/未登录）、详情 r 刷新（同步列表/非同步提示）、键位作用域与输入框吞键 |
| main（3 个） | ytm store 缺失、损坏备份重建（.corrupt 时间戳）、默认模型常量一致性 |

### 18.9 已知限制
- **v11 keyring 路径未实测**（本机无 v11 cookie）：实现经 godbus/dbus v5 读 Secret Service（schema chrome_libsecret_os_crypt_password_v1），任何失败降级 empty key；遇真实 v11 环境需验证
- **零歌单账号显示"未登录"**：browse 无任何歌单条目时按契约判定为未登录（ErrNotLoggedIn），UI 显示"未登录"——空账号用户会被误导；属登录判定与空库无法区分的固有限制
- **分页已支持**：browse 歌单库经 continuation 循环拉取直至耗尽（跨页去重，安全上限 100 页），>25 个歌单不漏同步
- **Windows 不支持**浏览器自动导出（v20 app-bound 加密），UI 明确提示改用 cookies.txt 方式
- 运行中浏览器的 cookie 读取依赖 SQLite WAL 可读性：直开失败时复制 DB + WAL/SHM 到临时目录，极端情况（如 WAL 未落盘）可能读到旧数据或失败，失败会提示重试
- browseId 语义为**"库中歌单"**（FEmusic_liked_playlists 历史命名沿袭，实际对应 Music Library 的歌单列表），非"我喜欢的音乐"；如 YTM 结构调整可能需跟随 ytmusicapi/yutemal 更新
- 同步为**单向快照**：本地列表是远端快照，本地手工增删会被下次刷新整列表覆盖（Create/重命名仍可用，重命名后同步映射失效会重建新列表）
- 退出登录仅清除配置，已落盘 cookie 文件（ytm-cookies.txt）保留在磁盘（0600），需要时可手动删除
- 播放列表拉取经 yt-dlp（flat 模式），条目含时长/频道/封面元数据，但不含歌词等附加信息；YTM 歌单按 youtube.com 处理属 yt-dlp 正常行为

## 19. OpenAI 增强歌词匹配（追加需求，用户确认方案）

### 19.1 概述
现有歌词链路是确定性匹配（标题清洗 + lrclib /api/get ±2s / /api/search 30s 评分），对 YouTube 噪声标题（繁中、feat.、版本描述、频道名混入）命中率有限。本增强引入 **OpenAI 清洗标题 → 重查 lrclib** 的兜底路径：

> **歌词必须带时间轴（用户追加要求）**：全链路（确定性 + AI 路径）只接受 lrclib 的 syncedLyrics；只有纯文本 plainLyrics 的条目一律视为无歌词（ErrNotFound）——无时间轴的歌词没有使用价值。UI 的纯文本歌词态（lyricsPlain）已随此规则删除，`Lyrics` 结构不再有 Plain 字段。

> **AI 标题全局展示覆盖（用户追加要求）**：AI 识别出的清洗后歌名/歌手不只用于查询，还作为当前曲目的**展示标题**——首页控制栏、底部状态栏、队列页当前项（▶ 项）均显示「晴天 - 周杰伦」而非原始「【周杰倫】晴天 Official Music Video」；onTrack 以清洗后曲目副本重发（MPRIS 回调机制就绪，但服务端仅 TrackStartedEvent 发布元数据，实际保持原始标题，见 19.8）。歌词未到达时先显示原始标题，切歌时覆盖清空。

- **不配置 OpenAI = 完全禁用**：行为与现状逐字节一致（main 只组装确定性 `*lyrics.Client`）
- 纯 REST 调用（net/http，无 SDK），temperature 0.2（参考项目 /data/code/lyrics 同值），JSON 输出 `{is_song, title, artist}`
- 参考项目已实现同款功能（src/ai/openai.cpp + src/lyrics/provider.cpp + src/cache/cache.cpp），实现时**未复制其三个已知缺陷**：Lrcmux 接线复制粘贴错误、lrclib 降级重试结果被变量遮蔽丢弃、负缓存加载时被过滤丢弃

### 19.2 配置（config 包）
```json
{
  "cache": { ... },
  "openai": {
    "api_key": "sk-...",                          // 空/缺省 = AI 路径完全禁用（默认）
    "model": "gpt-4o-mini",                       // 缺省/空回落 gpt-4o-mini；可填三方模型名
    "base_url": "https://api.deepseek.com/v1"     // 可选：OpenAI 协议兼容服务；缺省 = 官方
  }
}
```
- `config.OpenAI` 结构 + Load 指针字段回落语义（与 cache 同款）：api_key 缺失/显式空 → 禁用；model 缺失/空 → 默认；base_url 缺失/空 → OpenAI 官方
- **支持任意 OpenAI 协议兼容服务**（DeepSeek/通义/自托管网关等）：base_url 填服务基地址（含 /v1），model 填该服务的模型名；客户端只依赖 chat/completions 标准协议与 temperature/JSON 输出能力
- main.go：`cfg.OpenAI.APIKey != ""` 时组装 `lyrics.NewEnhancedClient(lc, ai, <缓存目录>/lyrics)` 并替换传给 ui 的歌词服务（`lyrics.Fetcher` 接口：`*Client` 与 `*EnhancedClient` 都实现）；缓存初始化失败仅警告并降级确定性匹配（增强功能不影响主功能，与 loadCache 同哲学）
- **配置文件写盘权限 0600**：文件含 OpenAI API key，禁止其他本地用户读取（ytm-cookies.txt 同款惯例）；默认模型常量 `config.DefaultOpenAIModel` 与 `lyrics.DefaultAIModel` 有 main 层一致性测试锁住

### 19.3 匹配流程（混合架构）
```
播放 → 确定性匹配（标题清洗 + /api/get ±2s → /api/search 30s）
  ├─ 命中 → 用（Source 空，UI 不标注）
  └─ 未命中 且 配置了 OpenAI →
      AI 结果缓存查 key（规范化 title|artist）
        ├─ 命中 → 直接取识别结果
        └─ 未命中 → 调 OpenAI（瞬时错误重试 1 次；成功即缓存，含负缓存）
      is_song=false 或空 title → ErrNotFound（负缓存生效，不再调 AI）
      歌词缓存（清洗后 title-artist）命中 → 返回（Source=ai）
      未命中 → FetchForQuery：/api/get 优先（lrclib ±2s）→
               失败 /api/search 严格评分（≤3s）→ 命中入歌词缓存
无 OpenAI 配置 / 调用失败 → 降级确定性结果（ErrNotFound 原样返回）
```
- **AI 标题全局展示覆盖**：AI 路径成功时 `FetchResult.Title/Artist` 携带清洗后歌名/歌手（`Fetcher.Fetch` 返回值从 `*Lyrics` 扩展为 `FetchResult{Lyrics, Title, Artist}`）；ui 在歌词结果到达时对 home/queuePage 应用展示覆盖并立即重建队列视图、以清洗后曲目副本重发 onTrack（MPRIS 回调；服务端元数据仍为原始标题，见 19.8）、beginPlay 切歌清空覆盖
- prompt：英文指令 + JSON 形状约束 + 繁→简转换 + feat./版本描述剥离 + 5 组示例（含参考项目的「山吹菌/少年霜」刁钻例）；响应解析先取括号平衡的 JSON 对象（代码围栏/前后杂文/数组包裹均容错），截断或垃圾内容报错不静默
- 识别输入 = 标题 + 频道名（hint）：频道名可帮 AI 定位歌手（如「周杰倫官方頻道」），prompt 明示其仅为 hint 可能无关

### 19.4 时长匹配规则（用户明确要求）
- **AI 路径**用严格阈值 `maxAIDurationDelta = 3.0s`：候选歌词 duration 与目标歌曲差距 **≤3s 才采用，差距最小者优先**；所有候选都 >3s → 视为无歌词（ErrNotFound）——AI 结果可能张冠李戴（同名翻唱/现场版），时长是最可靠的判别信号
- /api/get 服务端本身按 ±2s 匹配，天然满足严格阈值；客户端对 get 命中同样校验 `|Δ|≤maxDelta`（防御不遵守契约的自托管服务）
- **确定性路径保持 30s 阈值不变**（`maxDurationDelta`）：现有行为零回归（无配置时行为不变的前提）
- 实现：`chooseBestWithin(songs, track, maxDelta)` 参数化，`chooseBest` 为 30s 包装，`Client.FetchForQuery(title, artist, duration)` 走 AI 查询（单候选不套 cleanCandidates——AI 已清洗）

### 19.5 双缓存
| 缓存 | 位置 | 格式 | 键 |
|---|---|---|---|
| AI 结果 | `<缓存>/lyrics/ai.jsonl` | JSONL 每行 `{key, is_song, title, artist}` | 长度前缀编码 `N:title|M:artist`（空白折叠；长度前缀消除 title/artist 中 `|` 的键碰撞） |
| 歌词 | `<缓存>/lyrics/lrc/` | 同步歌词 → `<title>-<artist>.lrc`（纯 LRC 文本，毫秒精度序列化；sync-only，无纯文本形态） | 清洗后的 title-artist（不安全字符替换、按字节截断 ≤200B） |

- **负缓存**：`is_song=false` 同样入库——同一标题不再重复调用 AI 烧钱（参考项目只缓存正结果，此处按需求改进）
- 歌词缓存命中直接读文件、不发 lrclib 请求（同标题不同 track ID 的重复播放零网络开销）
- AI 调用**失败不缓存**（瞬时错误下次重试）；重复 Put 同键不覆盖不追加
- **并发 single-flight**：同一 key 的并发识别合并为一次 AI 调用（等待者复用执行者结果，执行者失败不惊群重试）；缓存读写有互斥锁（-race 验证）
- 加载时损坏行（含超长行）跳过并原子重写清理；追加写失败仅丢该行（缓存是增强，绝不阻塞主流程）
- 缓存路径在 `cache` 配置的目录下，不占用音频缓存 LRU 名额，独立增长（歌词文件小，可接受）

### 19.6 UI 交互（AI 歌词来源标识）
- `lyrics.Lyrics` 新增 `Source` 字段：`LyricsSourceAI = "ai"` 标记 AI 增强路径产物（实时命中与歌词缓存命中都标注；确定性路径为空）
- 首页歌词列在 AI 来源歌词上方渲染一行 `〔AI 匹配〕`（faint 小字，在 viewport 外拼接，不参与滚动居中数学）；确定性来源不显示
- 默认模型 `gpt-4o-mini`（成本/质量平衡，可配置覆盖）

### 19.7 测试策略
| 模块 | 策略 |
|---|---|
| ai（19 个） | parseAIResponse：裸 JSON/代码围栏/前后杂文/数组包裹/非歌曲/缺 is_song 默认 true/截断/垃圾报错；Identify：httptest 校验请求形状（POST /chat/completions、Bearer、model、temperature 0.2、prompt 含标题与 feat. 示例）、内容提取、500 重试一次、401 不重试、429 重试耗尽、空 content/缺 choices 报错、默认 model/baseURL |
| client 严格阈值（6 个）+ sync-only（2 个） | FetchForQuery 六项 + 纯文本歌词拒绝（get 命中）、FetchResult 形状（确定性路径 Title/Artist 空） | FetchForQuery：get 命中优先、search 选差距最小、全部 >3s 弃用、3.0s 边界采用 / 3.01s 弃用、空 artist 跳过 get、get 命中超 3s 弃用降级 search |
| aicache（10 个） | 落盘 roundtrip、负缓存持久化、损坏行跳过 + 文件重写、键空白规范化、并发 Put（-race）、同键不重复、行格式 |
| lrccache（9 个） | 同步歌词 roundtrip（毫秒误差内）、sync-only（只产出 .lrc、.txt 不识别、旧 .txt 启动清理）、未知名 miss、清洗/截断/控制字符、空歌词不写、文件可 ParseLRC | synced/plain 存取 roundtrip（毫秒误差内）、未知名 miss、不安全字符清洗、超长截断（字节计）、sync/plain 分文件不覆盖、空歌词不写、文件为可 ParseLRC 纯文本 |
| enhanced（15 个） | 原 12 个 + AI 结果携带清洗标题（live/缓存命中两路径）、AI 路径纯文本拒绝  确定性命中不调 AI；无 AI 配置降级；AI 清洗→重查命中（Source 标注）；is_song=false 负缓存（二次不调 AI）；AI 失败降级 + 失败不缓存；全部候选 >3s 弃用；AI 结果缓存 + 歌词缓存命中免 AI/免 lrclib；AI 结果缓存命中仍重查 lrclib；空 title 拒绝；缓存跨重启落盘命中 |
| config（7 个新增） | openai 缺失禁用、key 存在 model 默认、显式空 key 禁用、显式值、显式空 model 默认、Save roundtrip |
| ui（6 个新增） | AI 来源歌词显示 `〔AI 匹配〕`、确定性来源不显示、AI 标识行视口高度预留（窄窗口不溢出）、控制栏/状态栏 AI 标题覆盖 + 切歌清空、队列当前项 AI 标题（非当前项保持原始）、MPRIS onTrack 收到清洗后曲目（无 AI 信息不触发） |

### 19.8 已知限制
- **MPRIS 元数据保持原始标题**：onTrack 二次回调已就绪（AI 结果到达时以清洗后曲目重发），但 mpris 服务端仅在 TrackStartedEvent 时发布 Metadata，AI 结果必然晚于该事件到达且下一曲的原始标题会先覆盖——playerctl 感知不到清洗后信息（回调机制保留，未来接入 SetMust 即可发布）
- **展示覆盖仅作用于当前播放曲目**：队列/历史/搜索/播放列表页中的未播放曲目没有 AI 信息（未识别过），保持原始标题；切歌瞬间（歌词未到达）短暂显示原始标题
- **AI 调用 429 无退避**：重试 1 次为立即重试（lrclib 客户端会尊重 Retry-After，AI 客户端未实现；429 后大概率仍 429，代价仅 1 次无效调用）
- **ai.jsonl 无条目上限**：唯一标题永久累积（~百字节/条，可接受）；如需可加 LRU 截断
- **歌词缓存键不含时长**：同 title-artist 的不同版本（如现场版与录音室版）共用缓存条目；AI 路径查询时 3s 严格规则已排除明显错配，但缓存命中路径不再校验时长——同名同歌手但时长差异极大的极端情形可能拿到另一版本歌词（与参考项目行为一致，可接受）
- **AI 调用共享 10s 歌词拉取预算**（fetchLyricsCmd 超时）：确定性路径已消耗时间时 AI 可用预算减少；超时降级为无歌词，不阻塞 UI
- **is_song=true 但空 title** 的识别结果按负缓存处理（不再调 AI），避免拿空标题空转 lrclib
- 仅支持 OpenAI 协议兼容服务（官方或自托管均可，baseURL 可注入）；未接 Gemini 等其它厂商（参考项目支持，本期不做）

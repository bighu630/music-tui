# music-tui 设计文档

## 1. 项目概述

一个终端（TUI）音乐播放器，基于 YouTube 搜索播放，支持同步歌词展示、播放历史与播放队列。包含四个页面：

- **首页**：展示当前播放内容（封面、标题、歌手）、可滑动进度条、播放控制、同步歌词、队列位置与模式
- **搜索页**：搜索输入条 + YouTube 搜索结果列表（Enter 播放 / a 加入队列）
- **历史页**：最近播放记录，可重播/删除/清空（a 加入队列）
- **队列页**：播放队列列表（当前曲高亮 + 序号），跳转播放/删除/清空/切换顺序随机

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
│   └── queue.go         # 队列页：队列列表（当前曲高亮）、跳转播放、删除、清空、模式切换
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
└── history/
    └── history.go       # 历史记录：JSON 读写、去重置顶、100 条上限、清空
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
⏵ 首页   搜索   历史   队列 (5)
```

- 当前页标签加粗粉色高亮（同歌词高亮行/队列当前曲目），其余弱化（faint）
- 首页标签带播放状态图标：`⏵` 播放中 / `⏸` 已暂停 / `⏹` 未播放（无曲目）
- 队列标签带数量（`队列 (5)`），空队列不带数量
- `Tab` / `1` / `2` / `3` / `4` 切换时高亮同步移动
- 鼠标支持（程序已启用鼠标捕获）：
  - 点击标签切换页面；点击 Tab 栏分隔/空白、或页面区域不拦截（列表/歌词区原生支持滚轮滚动与点击选择）
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
| 全局 | `Tab`（或 1/2/3） | 切换首页/搜索/历史 |
| 全局 | `空格` | 暂停/继续 |
| 全局 | `q` / `Ctrl+C` | 退出（关闭 mpv、保存历史） |
| 首页 | `←` / `→` | 进度条滑动 seek（-5s / +5s，可按住） |
| 搜索页 | `Enter` | 搜索 / 播放选中项 |
| 历史页 | `Enter` / `d` / `c` | 重播 / 删除单条 / 清空 |

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
- 第 4 个 Tab（队列页）+ 播放队列：顺序/随机播放、TrackEnded 自动连播
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
| 全局 | `Tab`（或 1/2/3/4） | 切换首页/搜索/历史/队列（循环） |
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
| ui | tea.Program + update 驱动集成测试：Tab 四页循环、Enter 替换语义、a 追加不打断、TrackEnded 自动连播与不切页、队列页跳转/删除/清空/模式切换、首页位置显示 |

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
      → 失败：状态重置 + 内存队列清空 + 错误横幅；磁盘会话保留（下次启动重试，
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

### 15.6 测试策略
| 模块 | 策略 |
|---|---|
| queue | Snapshot/Restore roundtrip、副本隔离、越界 CurrentIdx 降级 |
| session | 缺失=无会话、Save/State roundtrip（重启读回）、覆盖写、Clear、损坏报错、空文件 |
| player | fake mpv socket 断言命令序列：set pause=true → loadfile；超时/未连接表驱动扩展 |
| ui | 恢复：队列/模式/进度/暂停态断言、PlayPaused+Seek 调用、ended 两分支、失败重置、MPRIS onTrack 通知；保存：退出写盘、ProgressEvent 节流、无播放清除 |
| main | loadSession 缺失/损坏备份重建（与 loadHistory 同款） |

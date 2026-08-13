# music-tui 设计文档

## 1. 项目概述

一个终端（TUI）音乐播放器，基于 YouTube 搜索播放，支持同步歌词展示与播放历史。包含三个页面：

- **首页**：展示当前播放内容（封面、标题、歌手）、可滑动进度条、播放控制、同步歌词
- **搜索页**：搜索输入条 + YouTube 搜索结果列表
- **历史页**：最近播放记录，可重播/删除/清空

## 2. 目标与非目标

### 目标
- 搜索 → 播放 → 歌词 → 历史全链路可用
- 播放进度实时刷新（~50ms），歌词随进度逐行高亮
- 架构支持适配器扩展（后续可加中文音乐源）

### 非目标（YAGNI，第一版不做）
- 播放队列/自动连播
- 音量控制
- 中文音乐源适配器（网易云/QQ）
- 除首页外的封面图
- 歌词手动偏移调整
- MPV_PATH 等路径覆盖配置
- 搜索历史
- 续播（记住播放进度）
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
├── ui/
│   ├── root.go          # 顶层 Model：页面切换（Tab）、全局按键、全局播放状态
│   ├── home.go          # 首页：封面、歌曲信息、可滑动进度条、播放控制、同步歌词
│   ├── search.go        # 搜索页：搜索输入框、结果列表、加载/错误态
│   └── history.go       # 历史页：历史列表、重播、删除、清空
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
- 播放队列/自动连播
- 音量控制
- 续播（历史 Entry 增加进度字段）
- 搜索历史
- chafa 等外部工具增强封面画质

# 🎵 music-tui

一个基于 YouTube 的终端音乐播放器：搜索即播、同步歌词、播放队列、播放列表与历史，全部在一个 TUI 里完成。

> A terminal music player for YouTube, built with Go & Bubble Tea — search, queue, synced lyrics, playlists and history, all in one TUI.



https://github.com/user-attachments/assets/16f24f0f-6a86-4493-9ecd-60b59ad62277



## 📦 依赖

| 依赖 | 用途 | 安装 |
|---|---|---|
| [mpv](https://mpv.io/) | 播放后端（音频输出） | Linux：`sudo apt install mpv`（dnf/pacman 同理）· macOS：`brew install mpv` · Windows：`winget install mpv` |
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | YouTube 搜索与取流 | Linux：`sudo apt install yt-dlp` · macOS：`brew install yt-dlp` · Windows：`pip install yt-dlp` |

启动时自动检测，缺失会报错并提示对应平台的安装命令。

**可选**：OpenAI API key（启用 AI 歌词增强）、Chrome/Edge 等浏览器 cookie（用于 YT Music 歌单同步）。

## 🔨 构建与安装

需要 Go 1.25+：

```bash
go build -o music-tui .
./music-tui
```

## ⚙️ 配置

配置文件在首次运行时自动生成：`~/.config/music-tui/config.json`（macOS 为 `~/Library/Application Support/music-tui/`，Windows 为 `%AppData%`）。

```json
{
  "cache": {
    "enabled": true,
    "max_entries": 100,
    "dir": "/home/you/.cache/music-tui"
  },
  "openai": {
    "api_key": "",
    "model": "gpt-4o-mini",
    "base_url": ""
  },
  "ytdlp": {
    "headers": {}
  },
  "log": {
    "level": "info"
  }
}
```

| 配置项 | 说明 |
|---|---|
| `cache.enabled` | 音频缓存总开关（默认开） |
| `cache.max_entries` | 缓存歌曲数上限，超出按 LRU 淘汰（默认 100） |
| `cache.dir` | 缓存目录（默认 `~/.cache/music-tui`，删掉即清空缓存） |
| `openai.api_key` | OpenAI 协议兼容服务的 API key；**留空 = 禁用 AI 歌词增强**（行为与不带 key 完全一致） |
| `openai.model` | 模型名（默认 `gpt-4o-mini`） |
| `openai.base_url` | 兼容服务地址，可填 DeepSeek（`https://api.deepseek.com/v1`）等任何 OpenAI 协议兼容服务 |
| `ytdlp.headers` | 附加到每次 yt-dlp 调用的 HTTP 头（可选） |
| `log.level` | 日志级别：`debug` / `info` / `warn` / `error` |

## 🚀 使用方法

启动后进入首页。顶部为页面标签，底部常驻状态栏（歌曲名 + 当前歌词行 + 播放状态）。

### 全局键位

| 键 | 功能 |
|---|---|
| `Tab` / `Ctrl+→`、`Shift+Tab` / `Ctrl+←` | 切换页面（循环） |
| `1` ~ `5` | 直达首页 / 队列 / 播放列表 / 搜索 / 历史 |
| `空格` | 播放 / 暂停 |
| `a` | 把选中歌曲加入队列或播放列表（弹出选择器） |
| `q` / `Ctrl+C` | 退出（自动保存队列与进度） |

### 首页

| 键 | 功能 |
|---|---|
| `←` / `→` | 快退 / 快进 5 秒（也可点击进度条） |
| `,` / `.` | 上一首 / 下一首 |
| `m` | 切换播放模式（列表循环 / 随机 / 单曲循环） |

### 搜索页

| 键 | 功能 |
|---|---|
| `Enter` | 搜索；选中结果后回车播放 |
| `p` | 播放选中项（与 Enter 同义） |
| `Esc` | 聚焦输入框 |

### 队列页

| 键 | 功能 |
|---|---|
| `Enter` / `p` | 跳转播放选中曲目（保留队列） |
| `d` / `c` | 删除选中曲目 / 清空队列 |
| `s` | 切换顺序 / 随机 / 单曲循环 |
| `/` | 过滤（实时筛选，`Enter` 确认，`Esc` 退出） |
| `m` | 移动模式：`↑↓←→` / `hjkl` 移动歌曲，`Enter` / `Esc` 结束 |

### 历史页

| 键 | 功能 |
|---|---|
| `Enter` / `p` | 重播选中记录 |
| `d` / `c` | 删除单条 / 清空历史 |
| `/` | 过滤 |

### 播放列表页

| 键 | 功能 |
|---|---|
| `Enter` | 概览：进入列表详情；详情：从选中曲播放整个列表 |
| `p` | 概览：播放整个列表；详情：同 `Enter` |
| `n` / `r` / `d` | 新建 / 重命名 / 删除列表（详情页 `d` 为移除歌曲） |
| `Esc` / `←` | 详情页返回概览 |
| `s` / `y` / `u` | YT Music：登录设置 / 同步全部歌单 / 导入歌单链接（概览页） |
| `r` | 刷新当前列表（详情页，仅 YT Music 同步列表） |



## 📂 数据与日志

| 内容 | 位置 |
|---|---|
| 配置文件 | `~/.config/music-tui/config.json` |
| 播放历史 / 会话 / 播放列表 / YT Music 登录 | `~/.config/music-tui/`（`history.json` / `session.json` / `playlists.json` / `ytm.json`） |
| 音频缓存（含封面、歌词缓存） | `~/.cache/music-tui/` |
| 日志 | `/tmp/music-tui.log`（自动轮转） |

## ⚠️ 免责声明

本项目为个人自用工具，仅供学习交流。所有音乐、歌词与封面内容版权归原平台及权利人所有。

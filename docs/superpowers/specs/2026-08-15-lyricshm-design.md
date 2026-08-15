# 当前歌词行写入 /dev/shm/lyrics — 设计文档

日期:2026-08-15
状态:已确认(用户逐项确认:1a / 2a→改 / 3b / 切歌写歌曲名 / 格式 歌名-歌手)

## 需求

播放时把**当前歌词行**写入 Linux tmpfs 文件 `/dev/shm/lyrics`,供 OBS 歌词、桌面小部件、脚本实时读取。歌词行变化时更新(非每帧)。文件始终有内容:没有可显示歌词的时刻显示歌曲名。

## 行为表(用户确认)

| 时机 | 文件内容 |
|---|---|
| 切歌(resetForTrack) | `歌名 - 歌手`(trackLabel 格式) |
| 歌词加载中 | 保持歌名 |
| 歌词到达,首行出现(syncState 行切换 idx 0) | 歌词行(覆盖歌名) |
| 无歌词 / 加载失败(setLyrics 失败或空) | 保持歌名 |
| 行切换(syncState 中 idx != currentLine 且 idx >= 0) | 新歌词行文本 |
| 当前行是空行(TrimSpace == "") | 不写,保留上一行内容(1a) |
| idx == -1(歌词尚未开始,pos 早于首行) | 不写(文件保持当前内容) |
| 停止播放(Track == nil) | 保留最后内容,不清空(3b) |

无 `Clear()` 使用场景:文件从切歌起始终有内容(歌名或歌词)。

## 配置项(追加,2026-08-15 用户确认)

新增配置块 `lyric_file`(JSON,与现有 cache/openai/ytdlp/log 平级):

```json
{
  "lyric_file": {
    "enabled": true
  }
}
```

- `enabled` 开关:缺失 → 默认 `true`(保持现行为:默认开启);显式 `false` → 禁用,完全不写文件(等同功能未实现)
- **仅 Linux 生效**:非 Linux 平台即使配置 `enabled: true` 也禁用(运行时检测,维持现状,不加 build tag)
- 路径固定 `/dev/shm/lyrics`,不可配置(YAGNI)
- 实现:config 包 `LyricFile` 结构(跟随 `cache.Options.Enabled` 的 raw `*bool` 区分缺失/显式模式);main.go 中 `enabled == false` 时向 `NewModel` 传 `nil`(ui 层 nil 安全已有测试覆盖)

## 架构

### 新包 `lyricshm/`(仓库根目录,与现有包平行)

```go
// Package lyricshm 把当前歌词行写入共享内存文件(默认 /dev/shm/lyrics),
// 供 OBS/桌面小部件实时读取。仅 Linux 启用,静默降级。
package lyricshm

const DefaultPath = "/dev/shm/lyrics"

type Writer struct {
    path    string
    enabled bool
}

// New 检查平台与目录:runtime.GOOS != "linux" 或文件所在目录不存在
// → enabled=false(静默禁用,只打一条日志)。path 为空时用 DefaultPath。
func New(path string) *Writer

// WriteLine 写入 text+"\n"(覆盖写)。text 为空白串(TrimSpace=="")时跳过,
// 保留文件现有内容(空行保留上一行,1a)。禁用时 no-op。
func (w *Writer) WriteLine(text string)

// 私有:write(text string) 实际写文件;失败仅 logger.Warn,不 panic。
```

- 覆盖写用 `os.WriteFile(path, []byte(text+"\n"), 0o644)`,行变化频率低(秒级),无需节流
- 日志用项目 `logger` 包(与 main.go 一致的用法,如 `logger.Warn("lyricshm: ...")`);禁用/目录缺失用 `logger.Info` 一条

### 挂载点(ui/home.go,均在已有状态变化处)

| 方法 | 改动 |
|---|---|
| `homeModel` | 新增字段 `lyricFile *lyricshm.Writer`(nil = 不启用) |
| `syncState` 歌词高亮块 | `idx != m.currentLine` 且 `idx >= 0` 时:`m.lyricFile.WriteLine(m.lyrics.Lines[idx].Text)`(空行内部跳过);`idx == -1` 不写 |
| `resetForTrack` | 歌词/封面清空处追加:`m.lyricFile.WriteLine(m.trackLabel())`(写入歌名) |
| `setLyrics` 失败/空分支 | 不动作(保持歌名;由 resetForTrack 已写入) |
| `setAITrack` | AI 歌名到达时若 `lyricsState` 为 loading/none(文件里还是歌名),重写 `trackLabel()` 更新歌名 |

> 注:setLyrics 成功分支不改(首行出现由 syncState 行切换写)。

### 注入

- `NewModel` 增加参数 `lyricFile *lyricshm.Writer`(nil = 禁用),内部赋给 `m.home.lyricFile`。与现有 mprisCtrl 的 main 注入模式一致。
- `main.go` 调用处传 `lyricshm.New(lyricshm.DefaultPath)`。
- root_test.go 的 `newTestModel` helper 传 nil(现有测试零影响);新增测试注入临时目录 writer。

## 测试(TDD)

- `lyricshm/` 包单测(临时目录):
  - WriteLine 写入文本(内容 = text+"\n")
  - 空行/空白行跳过,文件内容不变
  - 重复写同文本覆盖写
  - 禁用(目录不存在)时 New 返回 enabled=false,WriteLine/Clear no-op 不报错
  - path 为空用默认路径
- `ui/home_test.go` 触发逻辑(注入临时目录 writer):
  - syncState 行变化 → 文件内容 = 新行文本
  - 行未变化(同 position 重复 syncState)→ 文件内容不变
  - 当前行为空行 → 保留上一行
  - resetForTrack → 文件 = 歌名
  - setLyrics 失败/无歌词 → 文件保持歌名(不变)
  - 停止播放(Track == nil syncState)→ 文件保持
  - lyricFile == nil → 不 panic,现有测试回归

## 验证

- `go build ./...`、`go vet ./...`、`go test ./... -race` 全绿
- 用户实测:播放歌曲,`cat /dev/shm/lyrics` 跟随歌词行变化;切歌显示歌名;无歌词歌曲显示歌名

# 全局 Cookie + 自定义 Header 接入 yt-dlp 取流链路 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 music-tui 所有 yt-dlp 调用（搜索/歌单/缓存下载/播放取流）全局附加可选的 cookie 与自定义 headers，降低 YouTube 403 风控取流失败率；配置可选（不配置行为与现状完全一致）；取流失败时错误信息给出可操作的 cookie 配置引导。

**Architecture:** 四层接入，全部可选：
1. `config` 包新增 `ytdlp.headers`（map[string]string，自定义 header）
2. cookie 复用 `ytm.Store.CookieFile()`（YTM 登录配置一次，全局生效）——不新增独立 cookie 配置项
3. music-tui 直接构造的 yt-dlp 调用（search.Search / search.FetchPlaylist / cache 下载）：命令行 `--cookies <file>` + 多次 `--add-header "Name:Value"`
4. 播放取流链路（mpv 内置 ytdl_hook 调 yt-dlp）：mpv 启动参数 `--ytdl-raw-options` 追加 `cookiefile=<path>` 与 `config-location=<临时配置文件>`；临时配置文件 = 用户 yt-dlp 默认配置（如代理）原文合并 + 排序后的 `add-header Name:Value` 行（mpv raw-options 是 key→value 字典、重复 key 覆盖，多 header 只能走配置文件；见 mpv issue #6492）

**Tech Stack:** Go 1.25 / yt-dlp / mpv（--ytdl-raw-options / ytdl_hook）

**已确认设计决策（用户 2026-08-15 确认）：**
- headers 配置格式：**map**（`"ytdlp": {"headers": {"Accept-Language": "zh-CN,zh;q=0.9"}}`），生成参数时按键排序保证确定性
- **默认不覆盖 UA**（yt-dlp 自带模拟 Chrome UA，与登录 cookie 匹配度最高）；用户需要时显式配置
- cookie **复用 ytm.Store**（YTM 登录配置一次全局生效），不新增独立 cookie 配置项
- mpv 取流链路用 **config-location 临时配置合并方案**（保留用户 yt-dlp 的代理等配置不丢失）
- 已知限制：mpv 为长驻进程，取流参数启动时固定——**配置 cookie/header 后需重启应用生效**

**git 纪律（全任务强制）：** 可能与其他会话并行（YT Music 同步、缓存、歌词等）。commit 只 `git add` 自己负责的文件，**绝不 `git add -A`**；commit 前 `git status` 检查。当前 worktree：`/data/code/music-tui/.worktrees/ytdlp-cookie-headers`（分支 feat/ytdlp-cookie-headers）。

**环境：** `export PATH=/data/GO/go24/bin:$PATH`（系统 PATH 无 go）

---

## API 契约（跨任务对齐，实现前先读此节）

```go
// config 包（Task 1）
type Ytdlp struct {
    Headers map[string]string `json:"headers"` // 自定义 HTTP header；nil/空 = 未配置
}
type Config struct {
    Cache  cache.Options `json:"cache"`
    OpenAI OpenAI        `json:"openai"`
    Ytdlp  Ytdlp         `json:"ytdlp"` // 新增
}
// Default() 中 Ytdlp 为零值（Headers=nil）；Load 解析 "ytdlp" 节，Headers 缺失/null → nil。

// search 包（Task 2）
func (a *YouTubeAdapter) SetGlobalYTDlp(cookieFile string, headers map[string]string)
// 语义：cookieFile 非空 → 所有调用附加 --cookies <file>；headers 非空 → 所有调用附加 --add-header（键排序）。
// FetchPlaylist 的 CookieArgs 参数优先：参数非空用参数，参数全空才回落全局 cookieFile。
// headers 在 Search/FetchPlaylist 中总是附加（无论 CookieArgs）。

// cache 包（Task 3）
func New(opts Options, ytdlpPath string, cookieFile string, headers map[string]string) (*Manager, error)
// realDownload 同样附加 --cookies / --add-header（排序）。Disabled() 签名不变。

// player 包（Task 4）
func NewMpvPlayer(binPath, socketPath string, cookieFile string, headers map[string]string) *MpvPlayer
// startProcess 的 --ytdl-raw-options 改为动态拼接（保持 socket-timeout=15,retries=2 打底）：
//   cookieFile 非空 → 追加 cookiefile=<path>
//   headers 非空 → 生成临时配置文件（新文件 player/ytdlpconf.go）+ 追加 config-location=<path>
// raw-options 值含逗号/双引号 → 用双引号包裹并转义内部双引号（mpv 语法 key="va,lue"）。
// Close() 删除临时配置文件。

// ui 包（Task 5）
func NewModel(..., setTrack ..., ytdlpConfigured bool) Model  // 末尾追加 bool 参数
// ytdlpConfigured = cookieFile != "" || len(headers) > 0。
// 取流失败提示：未配置且失败与风控相关（hint 含 "风控"/"拒绝访问" 或 file_error 含 403）时，
// 在 hint 末尾追加引导："；可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效"

// main.go（Task 6 整合）
// cookieFile, _ := ytStore.CookieFile()（错误 → ""）；headers := cfg.Ytdlp.Headers
// 依次接入 searchAdapter / cache.New / player.NewMpvPlayer / ui.NewModel
```

**add-header 参数格式统一为 `Name:` + strings.TrimSpace(Value)（冒号后无空格），全链路一致（命令行参数与配置文件行均如此）。** 配置文件行格式：`add-header Name:Value`；值含空格/双引号/反斜杠/`#` 时用双引号包裹并转义 `\` 与 `"`（yt-dlp 配置用 shlex POSIX 解析）。

---

### Task 1: config 包新增 ytdlp 节

**Files:**
- Modify: `config/config.go`
- Test: `config/config_test.go`

- [ ] **Step 1: 写失败测试**（config_test.go 追加）
  - `TestYtdlpHeadersDefault`: Default() 的 Ytdlp.Headers 为 nil
  - `TestYtdlpHeadersLoad`: 配置 `{"ytdlp":{"headers":{"Accept-Language":"zh-CN"}}}` → Load 后 Headers 恰为 1 项
  - `TestYtdlpHeadersMissing`: 无 ytdlp 节 / `"ytdlp":{}` / `"ytdlp":{"headers":null}` → Headers 为 nil（不 panic）
  - `TestYtdlpHeadersEmpty`: `"ytdlp":{"headers":{}}` → 空 map（len==0，非 nil 也可接受，断言 len==0）
  - `TestYtdlpHeadersRoundTrip`: Save 后再 Load → Headers 等值
- [ ] **Step 2: 跑测试确认失败**（go test ./config/）
- [ ] **Step 3: 实现**：Config 加 `Ytdlp Ytdlp` 字段与 `Ytdlp` 类型；Load 的 raw 结构加 `Ytdlp struct{ Headers map[string]string \`json:"headers"\` }`，`c.Ytdlp.Headers = raw.Ytdlp.Headers`（nil 透传，无需指针区分——nil 与空 map 语义一致均为"未配置"）
- [ ] **Step 4: 跑测试确认通过**
- [ ] **Step 5: commit**（仅 config 两个文件）
  ```bash
  git add config/config.go config/config_test.go
  git commit -m "feat(config): ytdlp.headers 自定义 header 配置（可选，缺省空）"
  ```

### Task 2: search 包全局 cookie/headers

**Files:**
- Modify: `search/youtube.go`
- Test: `search/youtube_test.go`（现有 fake-ytdlp.sh 模式见 158 行附近：写临时 shell 脚本记录 $@ 到文件，再断言参数）

- [ ] **Step 1: 写失败测试**（沿用现有 fake 脚本模式）
  - `TestSearchGlobalYTDlp`: SetGlobalYTDlp("/tmp/c.txt", {"Accept-Language":"zh-CN", "User-Agent":"ua/1"}) 后 Search → 脚本收到的参数依次含 `--cookies /tmp/c.txt`、`--add-header Accept-Language:zh-CN`、`--add-header User-Agent:ua/1`（按 key 排序）；未设置全局的 adapter → 参数不含 --cookies/--add-header（不回归）
  - `TestFetchPlaylistGlobalCookieFallback`: 全局 cookieFile 已设 + CookieArgs{} → 用全局；全局已设 + CookieArgs{File:"/x"} → 只用参数不用全局（断言不含全局路径）
  - `TestFetchPlaylistHeadersAlways`: 全局 headers 已设 → FetchPlaylist（无论 CookieArgs 是否为空）都附加 --add-header
  - 空值边界：cookieFile="" / headers 含空 value（trim 后为空则跳过该条）→ 不附加对应参数
- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**
  - `YouTubeAdapter` 加字段 `cookieFile string`、`headers map[string]string`
  - `SetGlobalYTDlp(cookieFile string, headers map[string]string)` 赋值
  - 辅助 `func (a *YouTubeAdapter) globalArgs() []string`：返回 `--cookies <file>`（cookieFile 非空）+ 排序后的 `--add-header K:V` 列表；headers 值 TrimSpace 后为空跳过
  - Search：`args := a.globalArgs()` + 现有 `--dump-json --no-warnings --flat-playlist ytsearch...`（顺序：全局参数在前不影响行为，与现有测试断言兼容即可——注意现有测试断言 args 的方式，别破坏）
  - FetchPlaylist：cookie 参数 = CookieArgs 非空 ? CookieArgs : 全局；headers 总是 globalArgs 的 add-header 部分（不含 --cookies，避免与参数 cookie 冲突——即 globalArgs 拆成两个辅助或直接内联构造）
- [ ] **Step 4: 跑测试确认通过**（go test ./search/ 全绿，含现有测试）
- [ ] **Step 5: commit**（仅 search 两个文件）
  ```bash
  git add search/youtube.go search/youtube_test.go
  git commit -m "feat(search): 全局 cookie/headers 附加到搜索与歌单拉取（可选，缺省不回归）"
  ```

### Task 3: cache 包下载附加 cookie/headers

**Files:**
- Modify: `cache/cache.go`、`cache/download.go`
- Test: `cache/cache_test.go`、`cache/download_test.go`（现有 fake yt-dlp 脚本模式）

- [ ] **Step 1: 写失败测试**
  - `TestDownloadGlobalYTDlp`: New(opts, ytdlpPath, "/tmp/c.txt", {"Accept-Language":"zh-CN"}) → 触发下载 → 脚本参数含 `--cookies /tmp/c.txt` 与 `--add-header Accept-Language:zh-CN`
  - 不配置 → 参数不含（现有测试已覆盖，保持不回归）
  - 空值边界同 Task 2
- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**
  - `New` 签名加 `cookieFile string, headers map[string]string` 两个参数；Manager 存字段
  - `realDownload` 加同两个参数；args 构造：`--no-playlist --no-warnings -f bestaudio` + `--cookies <file>`（非空）+ 排序 `--add-header K:V` + `-o <destBase>.%(ext)s <url>`
  - 更新包内所有 `cache.New(` 调用点（cache_test.go / download_test.go / main.go 由 Task 6 处理，本任务内测试文件全部更新）
- [ ] **Step 4: 跑测试确认通过**（go test ./cache/ 全绿）
- [ ] **Step 5: commit**（仅 cache 包文件）
  ```bash
  git add cache/
  git commit -m "feat(cache): 缓存下载附加全局 cookie/headers（可选，缺省不回归）"
  ```

### Task 4: player 包 mpv 取流链路（cookiefile + config-location）

**Files:**
- Modify: `player/mpv.go`
- Create: `player/ytdlpconf.go`（临时配置文件生成）、`player/ytdlpconf_test.go`
- Test: `player/mpv_test.go`（先读现有测试如何断言启动参数——fake mpv 脚本/socket 模式，适配）

- [ ] **Step 1: 写失败测试**
  - `TestYtdlpConfContent`: headers 含 2 项 → 生成文件内容含排序后的 `add-header` 行；值含空格 → 引号包裹（如 `add-header "User-Agent: Mozilla/5.0"`）；值含引号/反斜杠 → 转义
  - `TestYtdlpConfMergesUserConfig`: 在临时 HOME 构造 `~/.config/yt-dlp/config`（内容 `proxy http://127.0.0.1:7890`）→ 生成文件含该原文行 + add-header 行（先用户内容后 add-header）
  - `TestYtdlpConfPermissions`: 0600
  - `TestMpvRawOptionsComposition`: NewMpvPlayer(bin, sock, "/tmp/c.txt", {"Accept-Language":"zh-CN"}) 启动 → 断言 mpv 收到的参数含单一 `--ytdl-raw-options=socket-timeout=15,retries=2,cookiefile=/tmp/c.txt,config-location=<path>`（cookiefile 与 config-location 顺序固定；无配置时保持原样 `socket-timeout=15,retries=2`）
  - `TestRawOptionsQuoting`: cookie 路径含逗号 → `cookiefile="a,b"` 引号包裹
  - 现有测试更新 NewMpvPlayer 调用点（加空串/空 map 参数）并保持全绿
- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**
  - `MpvPlayer` 加字段 `cookieFile string`、`headers map[string]string`、`ytdlpConfPath string`
  - `NewMpvPlayer(binPath, socketPath string, cookieFile string, headers map[string]string)` 赋值
  - `player/ytdlpconf.go`：
    ```go
    // buildYtdlpConf 生成临时 yt-dlp 配置（合并用户默认配置 + headers add-header 行），
    // 返回路径。仅 headers 非空时调用。文件 0600。覆盖写（重连重新生成幂等）。
    func buildYtdlpConf(headers map[string]string) (string, error)
    // userConfigPaths 返回候选用户配置：$XDG_CONFIG_HOME/yt-dlp/config、
    // ~/.config/yt-dlp/config、darwin 加 ~/Library/Application Support/yt-dlp/config、
    // /etc/yt-dlp.conf（按序合并存在的）
    // quoteConfValue：含空格/双引号/反斜杠/# 时双引号包裹并转义 \ 与 "
    ```
  - `startProcess` 的 args：`--ytdl-raw-options=` 拼接 `socket-timeout=15,retries=2` + `,cookiefile=<v>`（非空，值按 mpv 语法处理：含逗号/引号时 `key="v"`）+ `,config-location=<path>`（headers 非空时；生成失败仅 log 并跳过该段——取流功能不因 header 配置问题崩溃，错误提示由 stderr/日志承载）
  - 幂等：startProcess 每次重新 buildYtdlpConf（覆盖写同路径）
  - `Close()` 里 `os.Remove(p.ytdlpConfPath)`（若非空）
- [ ] **Step 4: 跑测试确认通过**（go test ./player/ 全绿，含 -count=1；该包测试较慢属正常）
- [ ] **Step 5: commit**（仅 player 包文件）
  ```bash
  git add player/
  git commit -m "feat(player): mpv 取流链路附加 cookiefile 与 headers（config-location 合并用户配置，可选缺省不回归）"
  ```

### Task 5: ui 包取流失败错误引导

**Files:**
- Modify: `ui/root.go`
- Test: `ui/root_test.go`（及所有调用 NewModel 的测试文件）

- [ ] **Step 1: 写失败测试**
  - `TestLoadFailureHintCookieGuide`: 构造 Model（ytdlpConfigured=false）→ 模拟取流失败（LoadFailedError{FileError:"no audio or video data played"}）→ toast 消息含 "配置 YT Music 登录" 引导；ytdlpConfigured=true → 不含引导
  - 非风控类失败（FileError:"Couldn't resolve host"）→ 即使未配置也不追加引导
  - 现有测试更新 NewModel 调用点（末尾加 bool 参数）
- [ ] **Step 2: 跑测试确认失败**
- [ ] **Step 3: 实现**
  - `Model` 加字段 `ytdlpConfigured bool`；`NewModel` 末尾追加参数并赋值
  - 新增 `func (m Model) failureHint(le *player.LoadFailedError) string`：`h := loadFailureHint(le.FileError)`；当 `!m.ytdlpConfigured` 且（h 含 "风控" 或 "拒绝访问"，或 le.FileError 含 "403"）时 `h += "；可在设置中配置 YT Music 登录（cookie）降低风控失败，重启生效"`；返回 h
  - 取流失败 toast 调用处（root.go 1154-1232 区间，三处 hint 使用点：恢复失败/重试中/重试耗尽）改用 m.failureHint(le)
  - 注意：**只改取流失败（LoadFailedError）路径**；LoadTimeoutError 与缓存损坏路径不加引导
- [ ] **Step 4: 跑测试确认通过**（go test ./ui/ 全绿；该包测试慢，属正常）
- [ ] **Step 5: commit**（仅 ui 包文件）
  ```bash
  git add ui/
  git commit -m "feat(ui): 取流失败提示 cookie 配置引导（未配置且风控类失败时）"
  ```

### Task 6: main.go 接线 + 全量验证

**Files:**
- Modify: `main.go`、`main_test.go`（若构造受影响）

- [ ] **Step 1: 接线**
  - run() 中：`ytdlpHeaders := cfg.Ytdlp.Headers`；`cookieFile, _ := ytStore.CookieFile()`（错误忽略 → ""，不阻止启动）
  - `searchAdapter.SetGlobalYTDlp(cookieFile, ytdlpHeaders)`
  - `loadCache(cfg.Cache, ytdlpPath, cookieFile, ytdlpHeaders)`（loadCache 签名同步更新）
  - `player.NewMpvPlayer(mpvPath, sockPath, cookieFile, ytdlpHeaders)`
  - `ui.NewModel(..., mprisSrv.SetTrack, cookieFile != "" || len(ytdlpHeaders) > 0)`
  - 注释注明：cookie 为启动时快照（浏览器 cookie 过期需重启应用刷新，与 mpv 参数限制一致）
- [ ] **Step 2: 全量验证**
  ```bash
  export PATH=/data/GO/go24/bin:$PATH
  go build ./...
  go vet ./...
  go test ./...          # 全绿
  go test -race ./...    # 全绿（ui/player 包慢属正常，可加大 timeout）
  ```
- [ ] **Step 3: commit**
  ```bash
  git status   # 确认只动 main.go / main_test.go
  git add main.go main_test.go
  git commit -m "feat(main): 全局接入 ytdlp cookie/headers（search/cache/player/ui）"
  ```

### Task 7（收尾）：合并回 master

- [ ] 全部任务完成后：`git checkout master && git merge feat/ytdlp-cookie-headers`（在本仓库根目录执行），删除 worktree 分支
- [ ] 向用户汇报：验证方法 = 配置 `config.json` 的 `ytdlp.headers` + YTM 登录 cookie → 重启 → 实测取流失败率变化；失败提示含 cookie 引导

---

## 自检（Self-Review）

- 需求覆盖：cookie 全局（Task 2/3/4/6）、headers 全局（Task 2/3/4/6）、配置可选不回归（各任务"缺省不回归"测试）、错误引导（Task 5）、复用 ytm cookie（Task 6 接 ytStore.CookieFile）✅
- 已知限制：mpv 取流参数启动时固定（重启生效）；cookie 启动时快照（重启刷新）；临时配置文件由 Close 清理、崩溃残留为 /tmp 下无敏感内容（仅 header 行，0600）
- 风险：mpv raw-options 值含逗号引号 → 引号包裹规则有测试覆盖；用户 yt-dlp 配置合并 → 有测试覆盖

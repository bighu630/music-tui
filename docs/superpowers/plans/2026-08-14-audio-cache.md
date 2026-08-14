# 音频缓存 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 music-tui 实现音频缓存：config 包（仅缓存设置项，JSON）+ cache 包（LRU 100 首、后台异步下载不阻塞播放、命中优先本地）+ player 播放链路集成，TDD 全绿交付。

**Architecture:** 三个新单元：`config`（配置加载/保存，嵌入 `cache.Options`）、`cache`（LRU 索引 + 下载器 + Manager 门面）、`ui`+`main` 集成（beginPlay/resumeCmd/LoadFailed 三处接入）。依赖方向：model ← cache ← config ← main；ui 依赖 cache。

**Tech Stack:** Go 1.25，标准库（encoding/json、net/http、os/exec、sync），无新依赖。测试：标准库 testing + httptest。

**Worktree:** `/data/code/music-tui/.worktrees/cache`（分支 feat/audio-cache）。
**重要环境说明：** `go` 不在默认 PATH。每个 shell 先执行 `export PATH=$HOME/go-sdk/go/bin:$PATH`。
**git 纪律：** commit 只 `git add` 自己任务的文件路径，绝不 `git add -A`；commit 前 `git status` 检查。本工作树可能与并行会话共存于同一仓库（不同 worktree），互不干扰。

---

### Task 1: config 包（TDD）

**Files:**
- Create: `cache/options.go`（**已由 feature lead 预写**，先 `read` 确认存在，不要修改）
- Create: `config/config.go`
- Create: `config/config_test.go`

**契约（必须遵守）：**
```go
// cache/options.go（已存在）
package cache
type Options struct {
    Enabled    bool   `json:"enabled"`
    MaxEntries int    `json:"max_entries"`
    Dir        string `json:"dir"`
}
```

config 包 API（Task 2/3 依赖，签名不可变）：
```go
package config

const DefaultMaxEntries = 100 // 缓存歌曲数上限默认值

type Config struct {
    Cache cache.Options `json:"cache"`
}

// Default 返回默认配置：Enabled=true、MaxEntries=100、
// Dir=os.UserCacheDir()/music-tui（UserCacheDir 失败返回错误）。
func Default() (*Config, error)

// Load 加载配置文件：
//   - 文件不存在 → MkdirAll 父目录 + 写默认配置（首次运行生成）+ 返回默认值
//   - 空文件 → 返回默认值（不写盘）
//   - JSON 损坏 → 返回错误（main 负责备份重建）
//   - 部分字段缺失 → 逐项回落默认；MaxEntries<1 → 100；Dir=="" → 默认
// 返回的 Config 始终是规范化后的完整值。
func Load(path string) (*Config, error)

// Save 原子写盘（tmp + rename），MarshalIndent("", "  ")。
func (c *Config) Save(path string) error
```

**步骤：**

- [ ] **Step 1: 写失败测试** `config/config_test.go`，覆盖：
  1. `TestLoadMissingCreatesDefault`：不存在路径 → 返回默认值（Enabled=true、MaxEntries=100、Dir=UserCacheDir()/music-tui）+ 文件已生成且内容可解析回相同值
  2. `TestLoadExistingOverrides`：预写 `{"cache":{"enabled":false,"max_entries":7,"dir":"/tmp/xyz"}}` → 读回三者
  3. `TestLoadPartialFallsBack`：`{"cache":{"enabled":false}}` → enabled=false、max_entries=100、dir=默认
  4. `TestLoadMaxEntriesBelowOneClamps`：max_entries=0 和 -5 → 100
  5. `TestLoadCorruptReturnsError`：内容 `{invalid` → 返回 error（且不写盘覆盖原文件）
  6. `TestLoadEmptyFileDefaults`：空文件 → 默认值
  7. `TestSaveRoundtrip`：Save 后 Load 读回相同；文件无 `.tmp` 残留
  8. `TestDefault`：Default() 字段断言
  （测试目录一律 `t.TempDir()`）

- [ ] **Step 2: 运行测试确认失败**（`go test ./config/ -v`，预期编译失败：package 不存在）

- [ ] **Step 3: 实现** `config/config.go`：按契约实现。要点：
  - `Load` 先 `os.ReadFile`：IsNotExist → Default + Save + 返回；len==0 → Default；`json.Unmarshal` 失败 → 返回 `fmt.Errorf("解析配置文件: %w", err)`
  - 规范化：`c.Cache.MaxEntries < 1` → DefaultMaxEntries；`c.Cache.Dir == ""` → 默认（调用 Default 取默认目录；注意避免循环：先 Default() 得到默认值再覆盖非零字段，或拆 normalize 函数）
  - `Save`：`os.MkdirAll(filepath.Dir(path), 0o755)` + 写 `path+".tmp"` + `os.Rename`（照抄 history.saveLocked 模式）

- [ ] **Step 4: 运行测试确认通过**：`go test ./config/ -v` 全绿 + `gofmt -l config/` 无输出

- [ ] **Step 5: Commit**
```bash
git add config/config.go config/config_test.go
git commit -m "feat(config): 配置文件（仅缓存设置项：开关/上限/目录，JSON，首跑生成默认+损坏备份模式预留）"
```

---

### Task 2: cache 包（TDD）

**Files:**
- Create: `cache/name.go`
- Create: `cache/index.go`
- Create: `cache/download.go`
- Create: `cache/cache.go`
- Create: `cache/index_test.go`, `cache/name_test.go`, `cache/download_test.go`, `cache/cache_test.go`

**契约（Task 3 依赖，签名不可变）：**
```go
package cache

// Options 见 Task 1（cache/options.go，已存在，勿改）

// Entry 一条缓存记录。
type Entry struct {
    ID         string    `json:"id"`
    File       string    `json:"file"`       // 缓存目录内的文件名（SafeName 结果，可含扩展名）
    LastPlayed time.Time `json:"last_played"`
}

// SafeName 文件名安全化：保留 [A-Za-z0-9._-]，其余字符转 '_'；
// 结果为空时返回 "unknown"。
func SafeName(id string) string

// Manager 音频缓存门面；所有方法并发安全。
type Manager struct { /* 私有字段 */ }

// New 创建 Manager：MkdirAll(opts.Dir) → 加载索引（缺失=空，损坏=返回错误）
// → 启动清理（条目文件缺失删条目；超限按 LastPlayed 淘汰最旧并删文件；有变化则持久化）。
// opts 规范化：MaxEntries<1 → config.DefaultMaxEntries（为避免循环 import，本地常量 100）；Dir=="" → 错误。
// ytdlpPath 用于后台下载取直链。
func New(opts Options, ytdlpPath string) (*Manager, error)

// Disabled 返回禁用态 Manager（Lookup 恒 miss、CacheAsync/Remove 为 no-op、Register 返回 nil）。
func Disabled() *Manager

// Enabled 返回缓存开关状态。
func (m *Manager) Enabled() bool

// Lookup 命中判定：开关开 + 索引有条目 + 文件存在（os.Stat）。
// 命中 → 刷新 LastPlayed=now 并持久化，返回缓存文件完整路径；
// 索引有条目但文件缺失 → 移除条目（不持久化），返回 miss。
func (m *Manager) Lookup(id string) (path string, ok bool)

// CacheAsync 后台异步下载并注册（不阻塞、立即返回）：
// 开关关/在途去重（同 ID 已有下载/条目已存在）→ no-op。
// goroutine 生命周期：总超时 downloadTimeout（包级 var，测试可调）兜底，完成/失败必退出。
// 失败仅 log.Printf，绝不 panic。
func (m *Manager) CacheAsync(track model.Track)

// Register 把已存在的缓存文件注册进索引（下载完成/测试预置用）：刷新 LastPlayed、
// 持久化、超限淘汰最旧（删文件）。不校验文件是否存在（Lookup 会校验）。
func (m *Manager) Register(id string) error

// Remove 删除缓存文件 + 索引条目 + 持久化；不存在返回 nil。
func (m *Manager) Remove(id string) error

// DownloadTimeout 等测试可调变量（包级）：
var DownloadTimeout = 5 * time.Minute // 单次后台下载总超时
var ExtractTimeout  = 60 * time.Second // yt-dlp 取直链超时
var DownloadRetryBackoff = 2 * time.Second // 下载失败重试间隔
```

**实现要点（分文件）：**

- `name.go`：SafeName 纯函数（逐 rune 判断 `(r>='a'&&r<='z')||(r>='A'&&r<='Z')||(r>='0'&&r<='9')||r=='.'||r=='_'||r=='-'`；否则 `_`；空→"unknown"）。注意 Unicode：非 ASCII（如中文 ID）全转 `_`，可接受（YouTube ID 本就 ASCII）。

- `index.go`：LRU 索引（内部结构体 `index`，Manager 私有使用）：
  - `entries []Entry`（**按 LastPlayed 升序，entries[0] 最旧**，evict 直接 `entries[0]`）
  - `get(id) (Entry, bool)`、`upsert(id string, at time.Time)`（存在→更新 LastPlayed 并重新排序，不存在→append）、`remove(id) bool`、`len() int`、`oldest() (Entry, bool)`
  - 排序：upsert 后 `sort.SliceStable(entries, func(i,j) bool { return entries[i].LastPlayed.Before(entries[j].LastPlayed) })`（条目少，简单可靠）
  - `load(path)`：ReadFile（IsNotExist → 空；len==0 → 空；Unmarshal 失败 → error）
  - `save(path)`：MarshalIndent("", "  ") + 写 `path+".tmp"` + Rename（照抄 history.saveLocked）

- `download.go`：
  - `type extractFunc func(ctx context.Context, url string) (streamURL, ext string, err error)`
  - `realExtract(ctx, url)`：`exec.CommandContext(ctx, ytdlpPath, "--no-playlist", "--no-warnings", "-f", "bestaudio", "--print", "%(url)s %(ext)s", url)`，`cmd.Output()`；输出按行解析（取第一行，`strings.Fields`）：≥1 字段 → streamURL=fields[0]；≥2 字段且 `safeExt(fields[1])` 合法 → ext。字段不足 → error
  - `safeExt(s)`：`^[A-Za-z0-9]{1,8}$` 才合法
  - `downloadFile(ctx, client, url, dest string) (int64, error)`：GET（非 2xx → error）、io.Copy 到 `dest+".part"`（Create/Truncate 覆盖），关闭后 rename；写入 0 字节 → error
  - 重试：downloadFile 失败（含 5xx）→ 等 `DownloadRetryBackoff` 重试 1 次（总 2 次尝试）
  - 错误统一 `fmt.Errorf("缓存下载失败(%s): %w", id, err)` 语义（外层 log）

- `cache.go`：Manager 组装：
  - 字段：`mu sync.Mutex`、`enabled bool`、`dir string`、`idx index`、`inflight map[string]bool`、`ytdlpPath string`、`client *http.Client`
  - `New`：规范化（MaxEntries<1 → 100；Dir=="" → error）→ MkdirAll → load 索引 → prune（遍历：条目文件 os.Stat 失败 → remove；`idx.len() > maxEntries` → evict 最旧并 `os.Remove(filepath.Join(dir, entry.File))`；有变化 → save）→ 返回
  - `Lookup`：锁内操作（os.Stat 也在锁内，µs 级可接受）；命中后 save（忽略错误）
  - `CacheAsync`：锁内查 inflight + 标记；`go m.download(track)`；`download` 内 `defer` 清除 inflight（锁）
  - `download(track)`：`ctx, cancel := context.WithTimeout(context.Background(), DownloadTimeout)` → realExtract（用 ExtractTimeout 子 ctx）→ GET 下载到 `filepath.Join(dir, SafeName(id))`（ext 非空则 `SafeName(id)+"."+ext`）→ `Register(id)`；任一步失败 `log.Printf("缓存下载失败: %v", err)`
  - `Register`：锁内 upsert(now) → evict 超限（删文件）→ save（错误返回）
  - `Remove`：锁内 remove + 删文件 + save
  - `Disabled`：`&Manager{enabled: false}`；`Remove` 需判 `m.enabled` 或字段判空——**注意 Disabled 的 mu 零值可用，dir 空串时文件操作用 `if m.dir == "" { return nil }` 防 panic**
  - 方法内凡用到 `m.dir` 处：`if m.dir == "" { return nil / 返回 miss }` 守卫（Disabled 安全）

**测试清单（每个文件对应测试文件）：**
- `name_test.go`：YouTube ID（含 `-` `_`）原样保留；`a/b c` → `a_b_c`；`中文` → `___`；空串 → "unknown"
- `index_test.go`：upsert 排序（新条目在末尾=最新）、同 ID 刷新后移、remove、save/load roundtrip、损坏 JSON → error、空文件 → 空
- `download_test.go`（httptest）：
  - 200 下载成功：文件内容正确、`.part` 无残留、ext 拼接正确
  - 404 → error 且无文件
  - 500 两次 → error（重试 2 次尝试）；500 后 200 → 成功（重试生效，`DownloadRetryBackoff` 测试中调 0）
  - 响应体为空 → error
  - realExtract：注入 fake yt-dlp（测试里写一个可执行 shell 脚本 `#!/bin/sh\necho "http://stream.example/a.m4a m4a"`，chmod +x，ytdlpPath 指向它）→ 解析出 streamURL+ext；脚本输出垃圾 → error
- `cache_test.go`：
  - New：目录创建、无索引 → 空、损坏索引 → error、Prune（预置索引条目指向不存在文件 → 清条目；超限 → 删最旧文件）
  - Lookup：未注册 → miss；Register 后（文件存在）→ hit + 路径正确；文件被删 → miss 且条目移除；Disabled → miss
  - Register：超限淘汰最旧（文件被删 + 条目移除）；同 ID 重复 Register 刷新时间
  - Remove：删文件+条目；不存在 → nil
  - CacheAsync 去重：注入 fake extract（计数）→ 并发调 10 次同 ID → extract 只调 1 次（用 channel 或原子计数等待 goroutine 结束）；不同 ID 各调 1 次
  - Disabled：全 no-op
  - 全部测试跑 `go test -race ./cache/`

**步骤：**

- [ ] **Step 1: 先写 name_test + index_test + 实现 name.go/index.go**，红→绿循环
- [ ] **Step 2: 写 download_test + 实现 download.go**（extractFunc 注入点：Manager 内部用包级变量 `var extract = realExtract`，测试可替换——注意测试替换后要恢复，或用 Manager 字段 `extract extractFunc`（New 默认 realExtract；测试直接构造 Manager 注入 stub）。**推荐 Manager 字段方案**）
- [ ] **Step 3: 写 cache_test + 实现 cache.go**
- [ ] **Step 4: 全量**：`go test -race ./cache/ ./config/` 全绿；`gofmt -l cache/ config/` 无输出；`go vet ./cache/ ./config/`
- [ ] **Step 5: Commit**
```bash
git add cache/ config/
git commit -m "feat(cache): LRU 音频缓存（索引持久化/安全文件名/yt-dlp 取直链+http 下载/并发去重/启动清理）"
```

---

### Task 3: ui + main 集成（TDD）

**前置：** Task 1、2 已完成（config 包 + cache 包可用）。**先 `git log --oneline -3` 确认。**

**Files:**
- Modify: `ui/root.go`（NewModel、beginPlay、resumeCmd、LoadFailedError 分支、resumeResultMsg 分支）
- Modify: `ui/root_test.go`（newTestModel helper + 新测试）
- Modify: `ui/resume_test.go`（NewModel 调用点）
- Modify: `main.go`（loadConfig、loadCache、NewModel 调用）
- Modify: `main_test.go`（loadConfig 测试）

**集成契约：**

1. `ui.Model` 增加字段：`cache *cache.Manager`、`playingFromCache bool`
2. `NewModel` 签名追加参数（顺序：`..., pls *playlists.Store, cm *cache.Manager, onTrack func(*model.Track))`）：
```go
func NewModel(p player.Player, s search.SearchAdapter, l *lyrics.Client, c *cover.Fetcher, h *history.Store, sess *session.Store, pls *playlists.Store, cm *cache.Manager, onTrack func(*model.Track)) Model
```
3. `beginPlay` 中 `m.player.Play(track.URL)` 之前插入：
```go
target := track.URL
if path, ok := m.cache.Lookup(track.ID); ok {
    target = path
    m.playingFromCache = true
} else {
    m.playingFromCache = false
    m.cache.CacheAsync(track) // 后台下载，不阻塞播放
}
if err := m.player.Play(target); err != nil {
```
4. `resumeCmd`：改为在 cmd 内 resolve（返回 msg 携带 fromCache）：
```go
type resumeResultMsg struct {
    err        error
    fromCache  bool
}
func resumeCmd(m Model) tea.Cmd {
    track := m.resume.track
    pos := m.resume.pos
    return func() tea.Msg {
        target := track.URL
        fromCache := false
        if path, ok := m.cache.Lookup(track.ID); ok {
            target = path
            fromCache = true
        }
        if err := m.player.PlayPaused(target, pos); err != nil {
            return resumeResultMsg{err: err, fromCache: fromCache}
        }
        return resumeResultMsg{fromCache: fromCache}
    }
}
```
5. `case resumeResultMsg`：成功与失败分支都先 `m.playingFromCache = msg.fromCache`；失败分支中若 `msg.fromCache` → `m.cache.Remove(track.ID)`（删除损坏缓存，下次启动走网络；用 `m.resume.track.ID`，注意 `m.resume` 可能为 nil——先判空）
6. `case *player.LoadFailedError`：
   - `m.resuming` 分支（恢复中取流失败）：若 `m.playingFromCache && m.state.Track != nil` → `m.cache.Remove(m.state.Track.ID)`，`m.playingFromCache = false`
   - `m.retryCount < maxPlayRetries` 分支：同上先 Remove + 复位标记，再调度 retryPlayCmd（重试的 beginPlay 会走 URL）
7. `main.go`：
```go
// 在 covers 初始化之后、mpv 启动之前：
cfg, err := loadConfig(filepath.Join(cfgRoot, "music-tui", "config.json"))
if err != nil { return fmt.Errorf("加载配置失败: %w", err) }
cm := loadCache(cfg.Cache, ytdlpPath)

// NewModel 调用追加 cm：
model := ui.NewModel(mpv, ..., sess, pls, cm, mprisSrv.SetTrack)
```
```go
// loadConfig：与 loadHistory 同款降级（损坏 → .corrupt-<ts> 备份重建）
func loadConfig(path string) (*config.Config, error)

// loadCache：New 失败 → 索引存在则备份 .corrupt-<ts> 重试 → 仍失败 log 警告 + cache.Disabled()
func loadCache(opts cache.Options, ytdlpPath string) *cache.Manager
```
   import 增加 `"music-tui/cache"`、`"music-tui/config"`。

**测试要点：**
- `ui/root_test.go` 的 newTestModel helper：构造 cache Manager：
```go
cm, err := cache.New(cache.Options{Enabled: true, MaxEntries: 100, Dir: filepath.Join(t.TempDir(), "cache")}, "/nonexistent/yt-dlp")
```
  （ytdlpPath 指向不存在路径 → CacheAsync 的 goroutine 立即 exec 失败退出，无网络请求、无泄漏）
- 新增测试（在 root_test.go 或新文件 `ui/cache_test.go`，参照现有 TestPlayFlow 的模式——先 read root_test.go 里 fake player 与 TestPlayFlow 的实现再写）：
  1. `TestPlayFromCacheUsesLocalPath`：预置（写文件 `filepath.Join(dir, SafeName(id))` + `cm.Register(id)`）→ startPlay → 断言 fake player 收到的 target == 缓存文件路径；断言 `m.playingFromCache == true`
  2. `TestPlayCacheMissUsesURL`：无预置 → 断言 fake player 收到 track.URL；`playingFromCache == false`
  3. `TestLoadFailedFromCacheEvictsThenRetriesNetwork`：预置缓存 → 播放 → 注入 `playerEventMsg{ev: &player.ErrorEvent{Err: &player.LoadFailedError{}}}` → 断言缓存条目已被移除（`cm.Lookup(id)` miss）→ 等待 retryPlayMsg（测试用 `retryBackoff` 调小）→ 断言 fake player 第二次收到 URL
  4. `TestResumeFromCacheUsesLocalPath`：仿照 resume_test.go 现有恢复测试：预置缓存 → NewModel + Init → 断言 fake player 收到 PlayPaused(本地路径, pos)
- `main_test.go` 新增：`TestLoadConfigMissing`（返回默认）+ `TestLoadConfigCorruptBackup`（损坏文件 → 备份 .corrupt-* 存在 + 重建默认配置 + 无错误，照抄 TestLoadHistoryCorruptBackup 模式）
- 现有测试适配：仅 newTestModel helper（root_test.go）与 resume_test.go:53 两处 NewModel 调用点

**步骤：**

- [ ] **Step 1:** 先 read `ui/root_test.go`（fake player、TestPlayFlow、newTestModel helper）与 `ui/resume_test.go` 恢复测试的写法
- [ ] **Step 2:** 红：先写 ui 新测试（1-4）+ main 新测试，跑 `go test ./ui/ ./...`（预期编译失败：NewModel 签名/cache 字段未定义）
- [ ] **Step 3:** 绿：实现 root.go 三处接入 + NewModel + main.go + 更新 2 处测试调用点；跑 `go test ./ui/ ./main` 全绿
- [ ] **Step 4:** 全量：`go test -race ./...`、`go vet ./...`、`go build ./...`、`gofmt -l .`（排除 third_party）
- [ ] **Step 5: Commit**
```bash
git add ui/root.go ui/root_test.go ui/resume_test.go main.go main_test.go
git commit -m "feat(cache): 播放链路集成（命中播本地/未命中后台下载/LoadFailed 损坏回退/恢复命中本地）+ main 配置与缓存装配"
```

---

### Task 4: 文档与收尾（feature lead 执行，worker 不参与）

- [ ] 设计文档第 17 章已追加（已完成，无需操作）
- [ ] TODO.md 追加音频缓存章节（master 根目录 TODO.md 为未跟踪文件，只更新不提交）
- [ ] 全量验证：`go test -race ./...`、`go vet ./...`、`go build ./...`
- [ ] 审查循环（reviewer）

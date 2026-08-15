# 文件日志系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 music-tui 增加写入 /tmp/music-tui.log 的文件日志：级别过滤（debug/info/warn/error，config 可配 log.level）+ 5MB 大小轮转（保留 .1 一份），现有输出点统一接入，播放/缓存/取流/歌词关键链路补日志。

**Architecture:** 新叶子包 `logger/`（只依赖 stdlib）提供全局线程安全日志 API（Init/SetLevel/Debug/Info/Warn/Error + ParseLevel/NormalizeLevel），main.go 最先 Init（默认 info，config 加载后 SetLevel 调整）；config 包新增 `log.level` 字段（缺失/非法回落 "info"）；各包删除 stdlib log、改调 logger 并补关键链路日志点。

**Tech Stack:** Go 1.25 标准库（os/filepath/sync/time/fmt），无新依赖。

**Spec:** `docs/superpowers/specs/2026-08-15-file-logger-design.md`

**Worktree:** `/data/code/music-tui/.worktrees/file-logger`（分支 feat/file-logger）。git 纪律：只 `git add` 自己改的文件，绝不 `git add -A`，commit 前 `git status` 检查。

---

### Task 1: logger 包核心（级别/解析/写文件/过滤/格式）

**Files:**
- Create: `logger/logger.go`
- Test: `logger/logger_test.go`

- [ ] **Step 1: 写失败测试** `logger/logger_test.go`（测试用 `setup` 辅助把日志重定向到 t.TempDir()，包级变量 `logPath`/`MaxFileSize` 可改）：

```go
package logger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setup 把日志重定向到临时文件并重新 Init；t.Cleanup 恢复默认状态。
func setup(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	logPath = path
	Init(LevelDebug)
	t.Cleanup(func() {
		mu.Lock()
		if file != nil {
			file.Close()
			file = nil
		}
		mu.Unlock()
		logPath = filepath.Join(os.TempDir(), "music-tui.log")
		MaxFileSize = 5 * 1024 * 1024
	})
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"INFO", LevelInfo}, // 大小写敏感：非法回落
		{"verbose", LevelInfo},
	}
	for _, c := range cases {
		if got := ParseLevel(c.in); got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeLevel(t *testing.T) {
	if got := NormalizeLevel("warn"); got != "warn" {
		t.Errorf("NormalizeLevel(warn) = %q", got)
	}
	if got := NormalizeLevel(""); got != "info" {
		t.Errorf("NormalizeLevel('') = %q, want info", got)
	}
	if got := NormalizeLevel("bogus"); got != "info" {
		t.Errorf("NormalizeLevel(bogus) = %q, want info", got)
	}
}

func TestLevelFiltering(t *testing.T) {
	path := setup(t)
	SetLevel(LevelInfo)
	Debug("debug line")
	Info("info line")
	Warn("warn line")
	Error("error line")
	content := readFile(t, path)
	if strings.Contains(content, "debug line") {
		t.Error("debug 行不应写入（当前级别 info）")
	}
	for _, want := range []string{"info line", "warn line", "error line"} {
		if !strings.Contains(content, want) {
			t.Errorf("缺少 %q", want)
		}
	}
}

func TestLineFormat(t *testing.T) {
	path := setup(t)
	Info("hello %s", "world")
	content := readFile(t, path)
	if !strings.Contains(content, "[INFO] hello world") {
		t.Errorf("行格式错误: %q", content)
	}
	// 时间戳前缀形如 2006-01-02 15:04:05.000（索引 4/10/23 为分隔符）
	if len(content) < 24 || content[4] != '-' || content[10] != ' ' || content[23] != ' ' {
		t.Errorf("时间戳前缀错误: %q", content)
	}
}

func TestInitFailureDegrades(t *testing.T) {
	// 日志路径指向不存在目录 → Init 失败 → 写不 panic、不创建文件
	logPath = filepath.Join(t.TempDir(), "no-such-dir", "x.log")
	Init(LevelDebug)
	Info("should not panic")
	SetLevel(LevelInfo)
}

func TestConcurrentWrites(t *testing.T) {
	path := setup(t)
	const goroutines = 8
	const perG = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				Info("g%d line %d", g, i)
			}
		}(g)
	}
	wg.Wait()
	content := readFile(t, path)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("行数 = %d, want %d（并发写丢行）", len(lines), goroutines*perG)
	}
	for _, ln := range lines {
		if !strings.Contains(ln, "[INFO] g") {
			t.Fatalf("行格式异常: %q", ln)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test ./logger/`
Expected: FAIL（logger 包不存在）

- [ ] **Step 3: 实现 `logger/logger.go`**（完整内容）：

```go
// Package logger 提供 music-tui 的文件日志：写入 os.TempDir()/music-tui.log，
// 带级别过滤与大小轮转（默认单文件 5MB，超限轮转到 .1，保留最近一份）。
// 进程内全局单例，所有函数并发安全；Init 失败静默降级为 no-op——
// 日志是辅助设施，绝不阻断启动。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level 是日志级别，数值越大越严重。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// 包级可调变量：测试可改 logPath 与 MaxFileSize 验证轮转。
var (
	// logPath 是日志文件路径（默认 os.TempDir()/music-tui.log）。
	logPath = filepath.Join(os.TempDir(), "music-tui.log")
	// MaxFileSize 是单文件大小上限（字节），超限轮转到 .1。
	MaxFileSize = int64(5 * 1024 * 1024)
)

// NormalizeLevel 返回 s 的规范化级别字符串；空/非法回落 "info"。
func NormalizeLevel(s string) string {
	switch s {
	case "debug":
		return "debug"
	case "warn":
		return "warn"
	case "error":
		return "error"
	default:
		return "info"
	}
}

// ParseLevel 解析级别字符串；空/非法回落 LevelInfo。
func ParseLevel(s string) Level {
	switch NormalizeLevel(s) {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

var (
	mu    sync.Mutex
	level = LevelInfo
	file  *os.File
	size  int64
)

// Init 打开/创建日志文件（追加，0600）并设置级别；重复调用重新打开
// （测试替换 logPath 后重新 Init 生效）。失败静默降级：后续日志 no-op。
func Init(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
	open()
}

// SetLevel 运行中调整级别（config 加载后调用）。
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// Debug/Info/Warn/Error 按级别写日志；低于当前级别直接丢弃。
func Debug(format string, args ...any) { logf(LevelDebug, format, args...) }
func Info(format string, args ...any)  { logf(LevelInfo, format, args...) }
func Warn(format string, args ...any)  { logf(LevelWarn, format, args...) }
func Error(format string, args ...any) { logf(LevelError, format, args...) }

// levelName 返回级别显示名。
func levelName(l Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	default:
		return "ERROR"
	}
}

// open 打开日志文件并记录当前大小；失败置 file=nil（调用方持 mu）。
func open() {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		file = nil
		return
	}
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	} else {
		size = 0
	}
	file = f
}

// rotate 轮转：关闭当前文件 → 删旧 .1 → rename 为 .1 → 重新打开（调用方持 mu）。
func rotate() {
	if file != nil {
		file.Close()
		file = nil
	}
	_ = os.Remove(logPath + ".1")
	_ = os.Rename(logPath, logPath+".1")
	open()
}

// logf 写一行日志：级别过滤 → 轮转检查 → 写入。写失败（磁盘满等）关闭
// 文件降级 no-op，避免每次写都失败（调用方持 mu）。
func logf(l Level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if l < level || file == nil {
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), levelName(l), fmt.Sprintf(format, args...))
	if size+int64(len(line)) > MaxFileSize {
		rotate()
		if file == nil {
			return
		}
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		file = nil
		return
	}
	size += int64(len(line))
}
```

- [ ] **Step 4: 运行确认通过**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test -race ./logger/`
Expected: PASS（TestLevelFiltering/TestLineFormat/TestParseLevel/TestNormalizeLevel/TestConcurrentWrites/TestInitFailureDegrades 全过）

- [ ] **Step 5: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add logger/logger.go logger/logger_test.go
git commit -m "feat(logger): 文件日志核心——级别过滤/时间戳格式/并发安全写"
```

---

### Task 2: logger 轮转

**Files:**
- Modify: `logger/logger_test.go`（追加测试）

- [ ] **Step 1: 写失败测试**（追加到 logger_test.go；每行约 65 字节，MaxFileSize=140 时第 3 行、第 5 行触发轮转）：

```go
func TestRotation(t *testing.T) {
	path := setup(t)
	MaxFileSize = 140
	for i := 0; i < 5; i++ {
		Info("%s", strings.Repeat(string(rune('A'+i)), 30))
	}
	content := readFile(t, path)
	// 主文件：第二次轮转后写入的最新行（E）
	if !strings.Contains(content, "EEEE") {
		t.Errorf("轮转后主文件应含最新行 E: %q", content)
	}
	if strings.Contains(content, "AAAA") {
		t.Errorf("轮转后主文件不应含首行 A: %q", content)
	}
	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("轮转文件缺失: %v", err)
	}
	// .1：第二次轮转的 chunk（C、D），最早 chunk（A、B）已被替换
	if !strings.Contains(string(old), "CCCC") || !strings.Contains(string(old), "DDDD") {
		t.Errorf(".1 应含 C、D: %q", string(old))
	}
	if strings.Contains(string(old), "AAAA") {
		t.Errorf(".1 不应含最早的 A（旧 .1 应被替换）: %q", string(old))
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test -race ./logger/ -run TestRotation`
Expected: FAIL（rotate 未实现，MaxFileSize 未生效——`TestRotation` 编译失败或断言失败）

- [ ] **Step 3: 实现轮转**

在 `logger/logger.go` 中实现 `rotate()`（见 Task 1 Step 3 完整代码，含 `MaxFileSize` 检查与 `rotate` 调用）。若 Task 1 已含完整实现，本步仅确认测试通过。

- [ ] **Step 4: 运行确认通过**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test -race ./logger/`
Expected: PASS（含 TestRotation）

- [ ] **Step 5: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add logger/logger_test.go
git commit -m "test(logger): 轮转测试——超限生成 .1、旧 .1 替换、最新行保留"
```

---

### Task 3: config 增加 log.level

**Files:**
- Modify: `config/config.go`
- Modify: `config/config_test.go`

- [ ] **Step 1: 写失败测试**（追加到 config/config_test.go；先读文件现有测试风格，保持同款写法）：

```go
func TestLogLevelDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, err := Load(path) // 文件不存在 → 生成默认
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("默认 Log.Level = %q, want info", cfg.Log.Level)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"level": "info"`) {
		t.Errorf("默认配置文件应含 log.level: %s", data)
	}
}

func TestLogLevelParse(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"log": {"level": "debug"}}`, "debug"},
		{`{"log": {"level": "warn"}}`, "warn"},
		{`{"log": {"level": "error"}}`, "error"},
		{`{"log": {"level": "bogus"}}`, "info"},    // 非法回落
		{`{"log": {"level": ""}}`, "info"},         // 空回落
		{`{"cache": {"enabled": false}}`, "info"},  // 缺失回落（不破坏既有字段）
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(c.in), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", c.in, err)
		}
		if cfg.Log.Level != c.want {
			t.Errorf("Load(%s) Log.Level = %q, want %q", c.in, cfg.Log.Level, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test ./config/ -run TestLogLevel`
Expected: FAIL（Config 无 Log 字段，编译失败）

- [ ] **Step 3: 实现 config 扩展**

`config/config.go`：
1. import 增加 `"music-tui/logger"`（注意现有 import 块格式）。
2. 新增类型：

```go
// Log 是日志配置：Level 为 "debug"/"info"/"warn"/"error"，
// 缺失/空/非法回落 "info"（默认级别）。
type Log struct {
	Level string `json:"level"`
}
```

3. `Config` struct 增加字段：`Log Log \`json:"log"\``（注释同步更新：顶层配置描述加"日志配置"）。
4. `Default()` 返回值增加：`Log: Log{Level: "info"}`。
5. `Load()` 的 raw struct 增加：

```go
		Log struct {
			Level *string `json:"level"`
		} `json:"log"`
```

6. `Load()` 解析后增加（放在 Ytdlp 处理之前）：

```go
	// Log：level 缺失 → 默认 "info"；显式值经 logger 规范化（非法回落 "info"）
	if raw.Log.Level != nil {
		c.Log.Level = logger.NormalizeLevel(*raw.Log.Level)
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go test ./config/`
Expected: PASS（含既有测试 + TestLogLevelDefault/TestLogLevelParse）

- [ ] **Step 5: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add config/config.go config/config_test.go
git commit -m "feat(config): log.level 配置项（默认 info，缺失/非法回落）"
```

---

### Task 4: main.go 初始化与现有输出点迁移

**Files:**
- Modify: `main.go`

- [ ] **Step 1: 实现迁移**（main.go，逐处修改）：

1. import：删除 `"log"`，增加 `"music-tui/logger"`（按字母序放在 history 前）。
2. `run()` 函数体第一行（依赖检测之前）：

```go
	// 0. 日志初始化：先默认 info（config 损坏/未加载也有日志），
	// config 加载成功后调整级别
	logger.Init(logger.LevelInfo)
```

3. `loadConfig` 成功返回后（紧跟其错误检查块之后）：

```go
	logger.SetLevel(logger.ParseLevel(cfg.Log.Level))
```

4. MPRIS（原 `log.Printf`）→：

```go
		logger.Warn("MPRIS 服务不可用（不影响播放器）: %v", err)
```

5. AI 歌词缓存警告（保留 stderr 输出行不变，追加）→：

```go
			fmt.Fprintf(os.Stderr, "music-tui: 警告：AI 歌词缓存初始化失败，已降级确定性匹配: %v\n", err)
			logger.Warn("AI 歌词缓存初始化失败，已降级确定性匹配: %v", err)
```

6. `loadHistory`/`loadPlaylists`/`loadConfig`/`loadSession`/`loadYTM` 五个损坏降级警告：每处保留 `fmt.Fprintf(os.Stderr, ...)` 行不变，紧接其后追加对应 `logger.Warn`（文案同 stderr 去前缀，如）：

```go
	fmt.Fprintf(os.Stderr, "music-tui: 警告：历史文件损坏，已备份至 %s 并重建\n", backup)
	logger.Warn("历史文件损坏，已备份至 %s 并重建", backup)
```

对应文案：播放列表文件损坏 / 配置文件损坏 / 会话文件损坏 / YT Music 配置文件损坏。
7. `loadCache`：缓存索引损坏警告保留 stderr + 追加 `logger.Warn("缓存索引损坏，已备份至 %s 并重建", backup)`；结尾 `log.Printf("缓存初始化失败（已降级为禁用）: %v", err)` → `logger.Warn("缓存初始化失败（已降级为禁用）: %v", err)`。

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./...`
Expected: 全 PASS（main_test 既有测试不动；run() 未执行时 logger 未 Init，日志调用为 no-op 不影响测试）

- [ ] **Step 3: 冒烟**（可选，本机有 mpv 才跑）：`go run . 2>&1 | head -3`，然后 `ls -la /tmp/music-tui.log && tail -3 /tmp/music-tui.log`（无 mpv 环境则跳过，不阻塞）。

- [ ] **Step 4: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add main.go
git commit -m "feat(main): logger 初始化 + 现有 stderr/log.Printf 输出点迁移"
```

---

### Task 5: ui/root.go 播放链路日志

**Files:**
- Modify: `ui/root.go`

- [ ] **Step 1: 实现**（逐处）：

1. import 块：删除 `"log"`，增加 `"music-tui/logger"`。
2. `beginPlay` 缓存判定处（`if path, ok := m.cache.Lookup(track.ID); ok {` 两分支内）：

```go
	if path, ok := m.cache.Lookup(track.ID); ok {
		target = path
		m.playingFromCache = true
		logger.Info("播放(缓存命中): %s - %s (id=%s) 文件=%s", track.Title, track.Artist, track.ID, path)
	} else {
		m.playingFromCache = false
		logger.Info("播放: %s - %s (id=%s) url=%s", track.Title, track.Artist, track.ID, track.URL)
		// 缓存预热不在 beginPlay 触发（见上方注释）：TrackStarted 统一启动
	}
```

3. `beginPlay` 的 `if err := m.player.Play(target); err != nil {` 分支开头追加：

```go
		logger.Error("播放命令失败: %s - %s (id=%s): %v", track.Title, track.Artist, track.ID, err)
```

4. `resumeCmd` 的匿名函数内（Lookup 之后、PlayPaused 调用之前）：

```go
		logger.Info("续播恢复: %s - %s (id=%s) pos=%.1f fromCache=%v", track.Title, track.Artist, track.ID, pos, fromCache)
		if err := m.player.PlayPaused(target, pos); err != nil {
			logger.Error("续播恢复失败: %v", err)
			return resumeResultMsg{err: err, fromCache: fromCache}
		}
```

5. `player.TrackEndedEvent` 分支（`case player.TrackEndedEvent:`）：三处 `return m.beginPlay(tr)` 前分别加 Info；`return m.stopAfterEnd()` 前加 Info：

```go
	case player.TrackEndedEvent:
		m.retryCount = 0
		m.loadingSince = time.Time{}
		if m.queueSkip {
			m.queueSkip = false
			if tr, ok := m.queue.Current(); ok {
				logger.Info("曲目结束(删除解耦): 连播顺延曲目 %s - %s", tr.Title, tr.Artist)
				return m.beginPlay(tr)
			}
			if tr, ok := m.queue.Next(); ok {
				logger.Info("曲目结束(删除解耦): 连播下一首 %s - %s", tr.Title, tr.Artist)
				return m.beginPlay(tr)
			}
			logger.Info("曲目结束: 队列为空，停止播放")
			return m.stopAfterEnd()
		}
		if tr, ok := m.queue.Next(); ok {
			logger.Info("曲目结束: 连播下一首 %s - %s", tr.Title, tr.Artist)
			return m.beginPlay(tr)
		}
		logger.Info("曲目结束: 无下一首，停止播放")
		return m.stopAfterEnd()
```

6. `player.ErrorEvent` 分支：在 `var le *player.LoadFailedError` 之前的取流失败分支入口（`if errors.As(ev.Err, &le) || isLoadTimeout {` 内、hint 计算之后）追加：

```go
			logger.Warn("播放失败(%s): %v", hint, ev.Err)
```

7. 同分支重试处（`if m.retryCount < maxPlayRetries { m.retryCount++` 之后）：

```go
				logger.Warn("播放失败，自动重试 %d/%d: %s - %s (id=%s)", m.retryCount, maxPlayRetries, m.state.Track.Title, m.state.Track.Artist, m.state.Track.ID)
```

8. 同分支跳过处（`if skip != nil && ...` 块内 `m2, cmd := m.skipFailedTrack(*skip, hint)` 之前）：

```go
				logger.Warn("重试耗尽，跳过失败曲目继续连播: %s - %s (id=%s)", skip.Title, skip.Artist, skip.ID)
```

9. 同分支停止处（`m.ended = true` 之前）：

```go
			logger.Error("播放失败，重试 %d 次耗尽，停止播放: %v", maxPlayRetries, ev.Err)
```

10. 非取流错误分支（`m.retryCount = 0` 之后、该分支其余逻辑开头）追加：`logger.Warn("播放器错误: %v", ev.Err)`（注意该分支还有重连中/恢复失败等子分支，插在分支入口即可，不重复）。
11. `togglePlay`：Playing 分支 `return m, func() tea.Msg { return playerOpResultMsg{err: m.player.Pause()} }` 前加 `logger.Debug("用户暂停")`；Resume 分支前加 `logger.Debug("用户继续播放")`。
12. seek 处理处（root.go 约 1564 行 `return playerOpResultMsg{err: p.Seek(target)}` 所在函数入口）加：`logger.Debug("seek 到 %.1fs", target)`（先读该函数确认变量名）。
13. 既有 4 处 `log.Printf` → `logger.Warn`（文案不变）：歌词拉取失败（lrclib 链）、写入历史失败、清除会话失败、保存会话失败。

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./ui/`
Expected: 全 PASS（root_test 既有断言不涉及日志）

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add ui/root.go
git commit -m "feat(ui): 播放链路日志——beginPlay/连播/重试跳过/暂停恢复/seek + log.Printf 迁移"
```

---

### Task 6: player/mpv.go 播放器日志

**Files:**
- Modify: `player/mpv.go`

- [ ] **Step 1: 实现**（逐处）：

1. import 块：删除 `"log"`，增加 `"music-tui/logger"`。
2. `Play`：函数体开头 `logger.Info("Play: %s", url)`；loadfile 失败分支（`return fmt.Errorf("loadfile: %w", err)` 前）加 `logger.Error("loadfile 失败: %s: %v", url, err)`。
3. `PlayPaused`：函数体开头 `logger.Info("PlayPaused: %s start=%.1f", url, start)`；失败分支（`return fmt.Errorf("loadfile: %w", err)` 前，注意该函数有两个失败返回点：set pause 与 loadfile）各加 `logger.Error("PlayPaused 失败: %s: %v", url, err)`（按分支文案区分，如 "set pause 失败" / "loadfile 失败"）。
4. `Pause`/`Resume`/`Seek`：函数体开头分别加 `logger.Debug("Pause")` / `logger.Debug("Resume")` / `logger.Debug("Seek: %.1fs", seconds)`。
5. `SetLoop`/`SetVolume`：函数体开头加 `logger.Debug("SetLoop: %v", loop)` / `logger.Debug("SetVolume: %.1f", percent)`。
6. `ytdlpconf` 失败（约 136 行 `log.Printf("生成 yt-dlp 临时配置失败...`）→ `logger.Warn`（文案不变）。
7. `onDisconnect` 两处 `log.Printf`（进程已退出 / IPC 连接断开）→ `logger.Warn`（文案不变）。
8. `doReconnect` 成功返回前（读该函数确认成功路径位置）加 `logger.Info("mpv 重连成功")`。
9. `watchdogLoop` 的 `if expired { p.emit(...) }` 处加 `logger.Error("mpv 加载看门狗超时（取流悬挂 %s）", loadWatchdogTimeout)`。
10. `pump` 的 `case "end-file"` → `case "error"` 分支（`p.emit(ErrorEvent{Err: &LoadFailedError{FileError: fileErr}})` 前）加 `logger.Warn("mpv end-file 错误: %s", fileErr)`（fileErr 空时文案仍可读，如 "（无诊断文本）"，按 fileErr == "" 分支处理）。

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./player/`
Expected: 全 PASS（mpv_test 为 mock 环境，不依赖真实 mpv）

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add player/mpv.go
git commit -m "feat(player): 播放/暂停/seek/重连/看门狗/end-file 日志 + log.Printf 迁移"
```

---

### Task 7: cache 包日志（下载/命中/淘汰/校验）

**Files:**
- Modify: `cache/cache.go`
- Modify: `cache/download.go`

- [ ] **Step 1: 实现**（逐处）：

`cache/cache.go`：
1. import 块：删除 `"log"`，增加 `"music-tui/logger"`。
2. 新增辅助方法（放 `register` 附近，替换 New 与 register 内两处重复的淘汰循环，DRY）：

```go
// evictIfOverLimit 超限淘汰最旧条目（删文件）；调用方持 m.mu。
func (m *Manager) evictIfOverLimit() {
	max := m.maxEntries
	if max < 1 {
		max = defaultMaxEntries
	}
	for m.idx.len() > max {
		e, _ := m.idx.oldest()
		logger.Info("缓存超限淘汰: %s (%s)", e.ID, e.File)
		os.Remove(filepath.Join(m.dir, e.File))
		m.idx.remove(e.ID)
	}
}
```

3. `New()` 启动清理循环：三处删除分支各加 Info/Debug——
   - 非法文件名分支：`logger.Warn("缓存启动清理: 非法文件名，删条目 %q", e.File)`
   - 文件缺失分支：`logger.Info("缓存启动清理: 条目文件缺失，删条目 %s (%s)", e.ID, e.File)`
   - 非音频分支：`logger.Warn("缓存启动清理: 条目非音频(损坏)，删文件+条目 %s (%s)", e.ID, e.File)`
   - `.part` 清理循环内：`logger.Debug("缓存启动清理: 删除 .part 残留 %s", e.Name())`
   - 末尾淘汰循环（`for m.idx.len() > m.maxEntries { ... }`）整块替换为（保持调用位置在 `m.idx.entries = kept` 之后、`if changed {` 之前）：

```go
	if m.idx.len() > m.maxEntries {
		m.evictIfOverLimit()
		changed = true
	}
```
4. `Lookup`：
   - 命中分支（`m.idx.upsert(id, time.Now())` 前）：`logger.Debug("缓存命中: %s (%s)", id, full)`（注意 Lookup 持锁，logger 调用无锁冲突——logger 独立锁，安全）。
   - 文件缺失分支（`m.idx.remove(id)` 前）：`logger.Info("缓存条目文件缺失，移除: %s (%s)", id, full)`
   - 校验失败分支：`logger.Warn("缓存校验失败，删除损坏文件: %s (%s)", id, full)`
5. `download`：
   - 尝试循环开头（`for attempt := 0; ...` 内第一行）：`logger.Debug("缓存下载开始(%s) 第 %d/%d 次: %s - %s", track.ID, attempt+1, MaxDownloadAttempts, track.Title, track.Artist)`
   - 成功分支（`if err == nil {` 内，register 成功时）：`logger.Info("缓存下载完成(%s): %s", track.ID, file)`
   - 原 3 处 `log.Printf("缓存下载失败...` → `logger.Warn`（文案不变）。
6. `register`：原淘汰循环（`max := m.maxEntries ... for m.idx.len() > max { ... }`）整块替换为 `m.evictIfOverLimit()`；校验失败分支（isAudioFile 非 ok/err）加 `logger.Warn("缓存注册校验失败: %s (%s): %v", id, file, err)`（err 可能为 nil，按 `ok=false` 分支给文案 "内容非音频"）。

`cache/download.go`：
7. import 增加 `"music-tui/logger"`（保留其余）。
8. `realDownload` 参数组装完成后（`cmd := exec.CommandContext(...)` 前）：

```go
	// 命令摘要日志：header 只打键名不打值（值可能含敏感信息）
	headerKeys := make([]string, 0, len(headers))
	for k := range headers {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)
	logger.Debug("yt-dlp 下载: %s 目标=%s headers=%v", url, filepath.Base(destBase), headerKeys)
```

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./cache/`
Expected: 全 PASS

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add cache/cache.go cache/download.go
git commit -m "feat(cache): 下载开始/完成/命中/淘汰/校验失败日志 + evictIfOverLimit 去重 + log.Printf 迁移"
```

---

### Task 8: search 包取流日志

**Files:**
- Modify: `search/youtube.go`

- [ ] **Step 1: 实现**：

1. import 增加 `"music-tui/logger"`。
2. `Search`：`cmd := exec.CommandContext(...)` 前加 `logger.Debug("yt-dlp 搜索: %s", query)`；成功返回前（`return parseYTDLPOutput(out)` 前）加 `logger.Debug("yt-dlp 搜索完成: %d 条 (%s)", len(tracks), query)`——注意先存结果：`tracks, err := parseYTDLPOutput(out)` 再分支日志/返回；错误分支（两个 return nil 处）各加 `logger.Warn("yt-dlp 搜索失败: %v", err)`（err 已含 stderr tail）。
3. `FetchPlaylist`：`cmd := exec.CommandContext(...)` 前加 `logger.Debug("yt-dlp 拉取歌单: %s", playlistURL)`；成功返回前加 `logger.Debug("yt-dlp 歌单完成: %s (id=%s) 条目=%d", pl.Title, pl.ID, len(pl.Tracks))`（先存结果再分支）；错误分支各加 `logger.Warn("yt-dlp 歌单拉取失败: %v", err)`。

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./search/`
Expected: 全 PASS

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add search/youtube.go
git commit -m "feat(search): yt-dlp 搜索/歌单调用与失败原因日志"
```

---

### Task 9: lyrics 包歌词链路日志

**Files:**
- Modify: `lyrics/enhanced.go`

- [ ] **Step 1: 实现**：

1. import 块：删除 `"log"`，增加 `"music-tui/logger"`。
2. `Fetch`：
   - AI 未配置分支（`if e.ai == nil { return e.lrclib.Fetch(ctx, track) }` 前）加 `logger.Debug("歌词: AI 未配置，走确定性匹配: %s - %s", track.Title, track.Artist)`。
   - AI 识别失败兜底分支（`if !ok || !res.IsSong || ...` 内、`return e.lrclib.Fetch(ctx, track)` 前）加 `logger.Debug("歌词: AI 未命中(%v/%v)，确定性兜底: %s - %s", ok, res.IsSong, track.Title, track.Artist)`。
   - lrcCache 命中（`if cached, ok := e.lrcCache.Get(...); ok {` 内）加 `logger.Debug("歌词: AI 结果缓存命中: %s / %s", res.Title, res.Artist)`。
   - 严格重查成功（`if err == nil {` 内）加 `logger.Debug("歌词: lrclib 严格重查命中: %s / %s", res.Title, res.Artist)`。
   - 中文源命中（`if ly, ok := e.fetchCN(...); ok {` 内）加 `logger.Debug("歌词: 中文源命中: %s / %s", res.Title, res.Artist)`。
   - 确定性兜底命中（`ly = det.Lyrics` 后）加 `logger.Debug("歌词: 确定性兜底命中: %s - %s", track.Title, track.Artist)`。
3. `identify`：
   - 成功（`e.aiCache.Put(key, r)` 前）加 `logger.Debug("AI 识别完成: %q → %q / %q (is_song=%v)", track.Title, res.Title, res.Artist, res.IsSong)`。
   - 既有 `log.Printf("AI 歌词识别失败（降级确定性结果）...` → `logger.Warn`（文案不变）。
4. `fetchCN`：两处 `log.Printf`（源搜索失败/取词失败）→ `logger.Warn`（文案不变）。

- [ ] **Step 2: 验证**

Run: `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test ./lyrics/`
Expected: 全 PASS

- [ ] **Step 3: Commit**

```bash
cd /data/code/music-tui/.worktrees/file-logger && git status
git add lyrics/enhanced.go
git commit -m "feat(lyrics): AI 识别/缓存命中/中文源/兜底路径日志 + log.Printf 迁移"
```

---

### Task 10: 全量验证与文档（feature_lead 执行）

- [ ] `cd /data/code/music-tui/.worktrees/file-logger && go build ./... && go vet ./... && go test -race ./...` 全绿
- [ ] 确认全仓无残留 `log.Printf`/`log.Fatal` 业务调用（third_party 除外）：`grep -rn "log\." --include="*.go" . | grep -v third_party | grep -v _test.go` 应只剩注释
- [ ] `git status` 干净；`git log --oneline` 呈现 8 个左右 feat/test commit
- [ ] TODO.md 追加「文件日志系统」已完成段落（记录：位置 /tmp/music-tui.log、5MB 轮转 .1、log.level 配置、tail -f 用法、接入点摘要）
- [ ] commit TODO.md

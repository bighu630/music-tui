# lyricshm 配置项(enabled 开关)实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为歌词文件写入功能增加 `lyric_file.enabled` 配置开关(默认开启;关闭时完全不写文件;仅 Linux 生效保持现状)。

**Architecture:** config 包新增 `LyricFile` 结构(Enabled bool,raw 解析用 `*bool` 区分缺失/显式,缺失默认 true,跟随 `cache.Options.Enabled` 模式);main.go 在构造 `ui.NewModel` 前根据配置决定传 `lyricshm.New(...)` 还是 `nil`(ui/lyricshm 层零改动,nil 安全已有测试)。

**Tech Stack:** Go 标准库 encoding/json(现有 config 机制)。

**Spec:** `docs/superpowers/specs/2026-08-15-lyricshm-design.md`「配置项」章节(本 worktree 已含)

---

## 文件结构

- 修改 `config/config.go` — LyricFile 结构 + Config 字段 + Default + Load(raw `*bool`)
- 修改 `config/config_test.go` — 3 个新用例(缺失默认 true / 显式 false / 显式 true)
- 修改 `main.go` — enabled 判断后传 writer 或 nil
- 全量验证 + 交叉编译

环境:go 路径 `~/go-sdk/go/bin/go`。git 只 add 上述文件,禁止 `git add -A`;commit 前 `git status` 检查。

---

### Task 1: config 包 LyricFile 开关(TDD)

**Files:**
- Modify: `config/config.go`
- Test: `config/config_test.go`

- [ ] **Step 1: 先读现有模式**

读 `config/config_test.go` 中与 `cache.enabled` 相关的测试(搜索 `enabled` 或 `Enabled`),确认测试写法(构造 JSON 字符串 → Load → 断言字段),以及 `cache.Options` 的 `Enabled bool` 在 Load 中如何被 raw `*bool` 规范化。后续 LyricFile 完全照抄该模式。

- [ ] **Step 2: 写失败测试**

在 `config/config_test.go` 追加(位置:现有 cache enabled 相关测试附近,风格一致):

```go
func TestLoadLyricFileEnabled(t *testing.T) {
	// 缺失 → 默认 true(开启)
	c, err := Load(filepath.Join(t.TempDir(), "cfg.json"))
	// ↑ 若现有测试用其他方式构造临时配置(如写文件再 Load),跟随现有测试的构造方式
	if err != nil {
		t.Fatal(err)
	}
	if !c.LyricFile.Enabled {
		t.Error("lyric_file 缺失时应默认开启")
	}

	// 显式 false → 关闭
	c2 := loadRawConfig(t, `{"lyric_file": {"enabled": false}}`)
	if c2.LyricFile.Enabled {
		t.Error("lyric_file.enabled=false 时应关闭")
	}

	// 显式 true → 开启
	c3 := loadRawConfig(t, `{"lyric_file": {"enabled": true}}`)
	if !c3.LyricFile.Enabled {
		t.Error("lyric_file.enabled=true 时应开启")
	}
}
```

> `loadRawConfig` 若不存在,跟随现有测试中最接近的 helper(搜索现有测试如何加载原始 JSON 配置,如 `loadConfig(t, json)`);若现有测试直接在测试内写临时文件 + Load,同样照做。测试最终形态以现有模式为准,语义三断言不变。

- [ ] **Step 3: 运行确认失败**

```bash
export PATH=$PATH:~/go-sdk/go/bin
go test ./config/ -run 'TestLoadLyricFile' -count=1
```
期望:编译失败(LyricFile 字段不存在)。

- [ ] **Step 4: 实现**

`config/config.go`:

(a) 新增类型(放在 Log 结构之后、Config 之前):

```go
// LyricFile 是歌词文件写入配置:Enabled 为开关,缺失 → true(默认开启),
// 显式 false → 禁用(完全不写文件)。仅 Linux 平台生效(非 Linux 恒禁用)。
type LyricFile struct {
	Enabled bool `json:"enabled"`
}
```

(b) Config 结构新增字段:

```go
type Config struct {
	Cache     cache.Options `json:"cache"`
	OpenAI    OpenAI        `json:"openai"`
	Ytdlp     Ytdlp         `json:"ytdlp"`
	Log       Log           `json:"log"`
	LyricFile LyricFile     `json:"lyric_file"`
}
```

(c) Default() 中新增(与 Log 平级):

```go
	}, Log: Log{
		Level: "info",
	}, LyricFile: LyricFile{
		Enabled: true,
	}}, nil
```

(d) Load() 的 raw 结构新增:

```go
		Log struct {
			Level *string `json:"level"`
		} `json:"log"`
		LyricFile struct {
			Enabled *bool `json:"enabled"`
		} `json:"lyric_file"`
```

(e) 规范化初始值(与 Log 平级):

```go
	}, Log: Log{
		Level: "info",
	}, LyricFile: LyricFile{
		Enabled: true,
	}}
```

(f) 反序列化后套用(放在 Log 套用之后):

```go
	if raw.LyricFile.Enabled != nil {
		c.LyricFile.Enabled = *raw.LyricFile.Enabled
	}
```

- [ ] **Step 5: 运行确认通过**

```bash
go test ./config/ -count=1
```
期望:全 PASS(含新增 3 断言 + 既有 config 测试)。

- [ ] **Step 6: Commit**

```bash
git status   # 确认只有 config/ 下两个文件
git add config/config.go config/config_test.go
git commit -m "feat(config): lyric_file.enabled 开关(缺失默认开启/显式 false 禁用)"
```

---

### Task 2: main.go 按配置接线

**Files:**
- Modify: `main.go`

- [ ] **Step 1: 读 main.go 现有接线**

读 main.go 中 `ui.NewModel(` 调用附近(约 130-150 行):`cfg` 变量已存在;确认 `lyricshm` import 已存在(Task 主功能已合并)。

- [ ] **Step 2: 修改**

在 `ui.NewModel(` 调用之前(约 cookieFile/ytdlpHeaders 处理之后)新增:

```go
	// 歌词文件写入器:lyric_file.enabled=false 时不创建(传 nil,ui 层 no-op);
	// 非 Linux 平台 lyricshm.New 内部自动禁用(仅 Linux 生效)。
	var lyricFile *lyricshm.Writer
	if cfg.LyricFile.Enabled {
		lyricFile = lyricshm.New(lyricshm.DefaultPath)
	}
```

并把 `ui.NewModel(` 调用末尾参数 `lyricshm.New(lyricshm.DefaultPath),` 替换为 `lyricFile,`。

- [ ] **Step 3: 编译 + 相关测试**

```bash
go build ./...
go vet ./...
go test ./config/ ./ui/ -count=1
```
期望:全绿。

- [ ] **Step 4: Commit**

```bash
git status   # 确认只有 main.go
git add main.go
git commit -m "feat(main): lyric_file.enabled=false 时不启用歌词文件写入"
```

---

### Task 3: 全量验证

- [ ] **Step 1: 全量验证**

```bash
export PATH=$PATH:~/go-sdk/go/bin
go build ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
GOOS=darwin go build ./...
GOOS=windows go build ./...
```
期望:全绿。

- [ ] **Step 2: git 状态干净确认**

```bash
git status
git log --oneline -5
```

---

## 验收标准

- `lyric_file.enabled` 缺失 → 默认开启;显式 false → 完全不写文件;显式 true → 开启
- 非 Linux 恒禁用(现状保持,交叉编译验证)
- 全量测试含 -race 全绿
- 实测:配置 enabled:false 后播放歌曲,`/dev/shm/lyrics` 不再更新(需用户配合验证,实现侧通过传 nil 保证)

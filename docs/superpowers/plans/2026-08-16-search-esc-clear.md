# 搜索页 Esc 清空结果 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搜索页结果态按 Esc 返回输入框时清空结果列表（保留输入框文字），状态回到 searchIdle。

**Architecture:** 只改 `ui/search.go` 的 `Update` 中 `"esc"` 分支：非输入框聚焦时，置 `state = searchIdle`、`results = nil`、`list.SetItems(nil)`，然后聚焦输入框。输入框文字天然保留（不触碰 `input`）。测试复用现有 `ui/search_test.go` 的 fake 流程。

**Tech Stack:** Go + bubbletea，`~/go-sdk/go/bin/go`（见 AGENTS.md，勿用系统 go）

---

### Task 1: 写失败测试 TestSearchEscClearsResults

**Files:**
- Modify: `ui/search_test.go`（文件末尾追加）

- [ ] **Step 1: 追加测试**

在 `ui/search_test.go` 末尾追加以下测试（复用现有 `newTestModel`/`fakeSearchAdapter`/`execSearchCmds` 辅助函数，见同文件 `TestSearchEnterPlaysSelected` 的既有写法）：

```go
func TestSearchEscClearsResults(t *testing.T) {
	fa := &fakeSearchAdapter{tracks: []model.Track{testTrack("t1"), testTrack("t2")}}
	m := newTestModel(t, newFakePlayer(), fa, nil)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}) // 数字键直达搜索页
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("晴天")})
	_ = execCmds(cmd)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	msgs := execSearchCmds(cmd)
	var res searchResultsMsg
	for _, msg := range msgs {
		if sm, ok := msg.(searchResultsMsg); ok {
			res = sm
		}
	}
	m, _ = update(m, res)
	if m.searchPage.state != searchDone || len(m.searchPage.results) != 2 {
		t.Fatalf("前置: state = %v, results = %d, want searchDone/2", m.searchPage.state, len(m.searchPage.results))
	}

	// Esc 返回输入框：结果清空、文字保留、状态复位
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("Esc 不应产生 cmd, got %v", cmd)
	}
	if m.searchPage.state != searchIdle {
		t.Fatalf("state = %v, want searchIdle", m.searchPage.state)
	}
	if len(m.searchPage.results) != 0 {
		t.Fatalf("results = %d, want 0", len(m.searchPage.results))
	}
	if n := len(m.searchPage.list.Items()); n != 0 {
		t.Fatalf("list items = %d, want 0", n)
	}
	if !m.searchPage.input.Focused() {
		t.Error("Esc 后输入框应聚焦")
	}
	if got := m.searchPage.input.Value(); got != "晴天" {
		t.Fatalf("input = %q, want 保留 晴天", got)
	}

	// 子用例：清空后 Enter 可立即重新搜索（adapter 再次被调用）
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = execSearchCmds(cmd)
	if fa.calls != 2 {
		t.Fatalf("adapter calls = %d, want 2（清空后可重新搜索）", fa.calls)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /data/code/music-tui && ~/go-sdk/go/bin/go test ./ui/ -run TestSearchEscClearsResults -v
```

Expected: FAIL — 当前 esc 分支只聚焦不清空，`state` 仍为 `searchDone`（`results` 仍为 2）

- [ ] **Step 3: 提交失败测试**

```bash
git add ui/search_test.go && git commit -m "test(ui): 搜索页 Esc 清空结果——先写失败测试"
```

---

### Task 2: 实现 esc 分支清空

**Files:**
- Modify: `ui/search.go:106-112`（`Update` 中 `"esc"` 分支）

- [ ] **Step 1: 修改 esc 分支**

把：

```go
		case "esc":
			if !s.typing() {
				return s, s.input.Focus()
			}
			return s, nil
```

改为：

```go
		case "esc":
			if !s.typing() {
				// 从结果/空结果态退回输入框：清空结果列表，回到干净的未搜索态
				// （输入框文字保留，可直接 Enter 重新搜索）。
				s.state = searchIdle
				s.results = nil
				s.list.SetItems(nil)
				return s, s.input.Focus()
			}
			return s, nil
```

- [ ] **Step 2: 运行测试确认通过**

```bash
cd /data/code/music-tui && ~/go-sdk/go/bin/go test ./ui/ -run 'TestSearch' -v
```

Expected: PASS（含既有 `TestSearchTypingAndEnter`、`TestSearchErrorState`、`TestSearchEmptyResults`、`TestSearchEnterPlaysSelected`、`TestSearchEmptyQueryIgnored` 与新用例）

- [ ] **Step 3: 提交**

```bash
git add ui/search.go && git commit -m "feat(ui): 搜索页 Esc 退回输入框时清空结果列表"
```

---

### Task 3: 全量验证

- [ ] **Step 1: 定向回归（搜索相关 + root 全局键位）**

```bash
cd /data/code/music-tui && ~/go-sdk/go/bin/go test ./ui/ -run 'Search|Esc|Tab|Spinner' 2>&1 | tail -5
```

Expected: ok（root_test 中有全局 Esc/Tab 键位测试，确认无回归）

- [ ] **Step 2: build + vet**

```bash
cd /data/code/music-tui && ~/go-sdk/go/bin/go build ./... && ~/go-sdk/go/bin/go vet ./...
```

Expected: 无输出（成功）

- [ ] **Step 3: 提交计划产物并汇报**

```bash
git add docs/superpowers/plans/ && git commit -m "docs(plan): 搜索页 Esc 清空结果实现计划" --quiet
```

## 自审

- **Spec 覆盖**：Esc 清空 ✅（Task 2）、文字保留 ✅（Task 1 断言 + Task 2 不触碰 input）、state 复位 ✅、重新搜索 ✅（子用例）、错误态不受影响 ✅（esc 分支 typing 时不变，错误态输入框已聚焦）
- **占位符**：无，所有步骤含完整代码与命令
- **类型一致性**：`searchIdle`/`searchDone`/`results`/`list.Items()`/`input.Focused()`/`input.Value()`/`fa.calls` 均与现有代码一致

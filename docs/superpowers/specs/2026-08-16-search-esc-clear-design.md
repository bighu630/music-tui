# 搜索页 Esc 清空结果 — 设计

日期：2026-08-16
状态：已与用户确认

## 需求

搜索页从结果列表按 Esc 退出回输入框时，**清空下方搜索结果列表**，回到干净可重新搜索的状态。

用户原话："搜索里面退出到搜索输入框里面，清理下面的搜索结果"

## 已确认细节

| 问题 | 结论 |
|------|------|
| 触发键 | Esc（现有"回输入框"键，语义延续，不加新键） |
| 输入框文字 | **保留**（只清结果列表；按 Enter 可立即重新搜索） |
| 清空后状态 | 回到 `searchIdle`（未搜索态），输入框聚焦 |

## 现状（ui/search.go）

- 状态机：`searchIdle`（输入框聚焦）→ `searchLoading`（搜索中）→ `searchDone`（结果/错误/空，输入框失焦）
- 当前 Esc 分支：非输入框聚焦时仅 `s.input.Focus()`，**结果列表保留**

## 改动

仅 `ui/search.go` 的 `Update` 中 `"esc"` 分支：

```go
case "esc":
    if !s.typing() {
        s.state = searchIdle
        s.results = nil
        s.list.SetItems(nil)
        return s, s.input.Focus()
    }
    return s, nil
```

- 输入框文字不动（`s.input.Value()` 天然保留）
- `s.err` 无需处理：错误态时输入框已聚焦，Esc 走 typing 分支不触发清空，错误提示可继续 Enter 重试

## 边界情况（已核对，自动安全）

- **a 键误加曲目**：`selectedTrack()` 要求 `state == searchDone`，清空后返回 false，旧结果无法再被加入播放列表
- **spinner 残留**：`spinnerNeeded()` 只看 `searchLoading`，state 回 `searchIdle` 后自然停链
- **空结果态**：本来就没有 items，清空无害，行为统一
- **切页往返**：searchModel 状态在 Model 中常驻，从结果态切走再切回仍可 Esc 清空，行为一致

## 测试（TDD）

新增 `ui/search_test.go` 用例 `TestSearchEscClearsResults`：

1. 搜索出结果（复用现有 fakeSearchAdapter 流程）→ 按 Esc
2. 断言：`state == searchIdle`、`results` 为空、list items 为空、输入框聚焦、**输入框文字保留**
3. 追加子用例：清空后直接 Enter → adapter 再次被调用（calls == 2），可重新搜索

## 验证

- `go build ./...`、`go vet ./...`
- `go test ./ui/ -run Search`（定向），通过后跑 `go test ./ui/`（避免全仓库全量，按 AGENTS.md 要求）

# music-tui 实现阶段任务清单（Subagent-Driven Development）

- [x] Task 1: 项目初始化 ✅
- [x] Task 2: model 包 ✅
- [x] Task 3: lyrics 包 ✅（3 轮审查）
- [x] Task 4: history 包 ✅
- [x] Task 5: cover 包 ✅（并发修复）
- [x] Task 6: search 包 ✅（错误可诊断性 + thumbnails 兜底）
- [x] Task 7: player 包 ✅（真实 mpv 冒烟验证 + 3 轮修复）
- [x] Task 8: ui 包 ✅（7 项计划缺陷修复 + ended 状态机）
- [x] Task 9: main.go 入口 ✅（损坏历史降级）
- [x] Task 10: 手动验收清单 ✅（文档 + PTY 实测，3 项待真实终端确认）
- [x] 最终全量验证（build/vet/test/-race 全绿）+ 最终整体审查 ✅
- [x] 最终审查 3 个 🟡 健壮性修复 ✅（e6e970b）

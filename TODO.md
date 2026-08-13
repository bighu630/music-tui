# music-tui 实现阶段任务清单（Subagent-Driven Development）

计划：docs/superpowers/plans/2026-08-13-music-tui.md（10 个 Task，每 Task 走 TDD 五步 + 两阶段审查）

- [x] Task 1: 项目初始化（go mod、依赖、目录结构）✅ spec+quality 通过
- [x] Task 2: model 包（Track、PlaybackState）✅ spec+quality 通过
- [x] Task 3: lyrics 包（LRC 解析器、lrclib 客户端）✅ spec+quality 通过（3 轮审查）
- [x] Task 4: history 包（JSON 存储、去重置顶、上限裁剪）✅ spec+quality 通过
- [x] Task 5: cover 包（封面下载、404 降级链、磁盘缓存）✅ spec+quality 通过
- [x] Task 6: search 包（yt-dlp 搜索适配器）✅ spec+quality 通过
- [x] Task 7: player 包（mpv 进程管理与 JSON IPC 事件分发）✅ spec+quality 通过（真实 mpv 冒烟验证）
- [x] Task 8: ui 包（三页面、全局按键与事件路由）✅ spec+quality 通过
- [ ] Task 9: main.go 入口（依赖检测、服务组装、退出清理）
- [ ] Task 10: 手动验收清单（文档 + 验收执行）
- [ ] 最终全量验证（go build / vet / test）+ 最终代码审查

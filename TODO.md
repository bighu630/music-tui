# music-tui 设计阶段任务清单

- [x] 探索项目上下文（目录现状、技术栈/搜索源/播放器/lrclib 调研）
- [x] 决策技术栈：Go (bubbletea + bubbles + lipgloss)
- [x] 澄清需求：搜索源=YouTube 主源；播放环境=系统音频输出；依赖策略=检测缺失即报错
- [x] 澄清需求：历史记录=只记录播放过的歌曲；首页进度条可滑动 seek
- [x] 提出 2-3 个架构方案并推荐（方案 A 确认）
- [x] 分节呈现设计并逐节确认（模块结构/数据结构/交互流程/错误处理）
- [ ] 写设计文档 docs/superpowers/specs/ 并提交（已派 worker）
- [ ] 设计文档自查
- [x] 用户审阅设计文档并确认
- [x] 写实现计划 docs/superpowers/plans/（4874 行/10 Task，已自查）
- [x] 用户确认执行方式（Subagent-Driven）
- [x] 创建 feature_lead 接手实现（session_1033a216-f42，进行中）

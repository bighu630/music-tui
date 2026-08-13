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

---

## MPRIS 追加需求（用户已确认方案）

- [x] 方案确认（godbus/dbus v5 手写服务端，仅 Linux，主体完成后实现）
- [x] 设计文档追加第 13 章 MPRIS 设计（commit 8c360bf）
- [x] 创建 feature_lead 处理 MPRIS（session_81ff29e1-8c2，进行中）
- [x] MPRIS 实现完成（player 广播+音量 / mpris 包 / ui 回调 / main 集成）
- [x] 验收：playerctl 实测（dbus-run-session：发现播放器、status、volume 读写、无 D-Bus 降级）
- [x] 验收（加分项）：PTY 驱动真实搜索→播放后，playerctl status=Playing / metadata 有值 /
      position 递增 / play-pause 生效 / position 10 跳转（均通过，见下方实测记录）
- [x] 已知限制：MPRIS 的 Next/Previous/OpenUri 返回 NotSupported（队列在 ui 层，MPRIS 服务未接入，可作后续迭代）；
      无 Introspectable 接口；Raise/Quit no-op；DesktopEntry 为空

### MPRIS playerctl 实测记录（Task 11.3，dbus-run-session 临时总线）

空态：`playerctl -l` 列出 music-tui；status=Stopped；volume 0.3 写入后读回 0.300000；
play-pause 空态 no-op 不崩溃；日志无 panic。

播放态（加分项，PTY 输入驱动真实搜索播放）：status=Playing；xesam:title/artist 有值；
position 1.47s→3.50s 递增；play-pause Paused⇄Playing 生效；position 10 → 11.98（误差为轮询延迟）；
退出后无 mpv 残留进程、无 socket 残留。

实测发现并修复一个崩溃 bug：真实 YouTube ID 含 `-`（如 sF80I-TQiW0）时，MPRIS trackid
对象路径非法，godbus 封送直接 panic 带崩整个应用；已改为 hex 编码 ID 为合法对象路径
（mpris/trackIDPath + 回归测试）。

## 播放队列追加需求（用户已确认设计）

- [x] 设计确认：queue/ 纯逻辑包 + 第 4 个 Tab（队列页）+ Enter 替换语义 + a 追加 + 顺序/随机播放
- [x] 创建 feature_lead 实现播放队列（与 MPRIS 并行，注意 git 协作）✅（commit 5f560c5 + 16d1f06，已合并 master）
- [x] 验收：连播、随机、添加/删除/清空、首页位置显示 ✅（测试全绿含 -race；review 循环修复删除当前曲连播衔接缺陷 9bfec4e；真机听感验收待用户确认）

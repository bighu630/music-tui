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

## 续播追加需求（用户已确认方案 B）

- [x] 设计确认：记住队列+进度，重启暂停恢复；退出保存 + 5s 节流自动保存
- [x] 实现：session/ 包 + queue Snapshot/Restore + player PlayPaused + ui 恢复/保存 ✅（commit 261bd92 + 4f4820f + 修复 7335251，已合并 master）
- [ ] 验收：真机验证（播放中退出→重启→空格继续；崩溃恢复）

## Bug 修复（bugfix_lead session_ba4ba596-692 处理中）

- [x] 创建 bugfix_lead：修复 "播放失败: mpv 未连接"（连接断开不重连）
- [x] 追加现象：用户另报 "⚠ mpv 播放出错（end-file reason=error）"——需排查与未连接是否同源（mpv 播放出错→退出→断连），以及 end-file error 的根因（yt-dlp 取流失败/视频不可用等）
- [x] 修复提交 + 验证 ✅（commit e54b5b5 + d04bbcb，已合并 master）
  - 根因：MpvPlayer 断开后无任何恢复机制——pump 只发一次 ErrorEvent，conn/cmd 残留死状态，
    所有命令永久报 "mpv 未连接" 直到重启应用
  - 方案：pump 断开→诊断日志（mpv 退出码，区分被杀/崩溃/socket 异常）+ 清理死状态 + 后台自动重连
    （最多 3 次 × 1s 间隔）；Play/Pause/Resume/Seek/SetVolume/Volume 惰性重连（单飞合并并发请求、
    失败 2s 冷却防风暴）；重连全失败发明确 ErrorEvent（不静默）；SetVolume/Volume 不再触发
    mpvipc IsClosed 数据竞争
  - 测试：helper 进程模式模拟真实 mpv 死亡/重启——自动重连、惰性重连、失败降级（启动次数≤3）、
    单飞（并发只启动一次）、crash-loop 双循环、重连中再断恢复；全量 -race + -count=3 全绿
  - 验证：断开后无需重启应用，按空格/回车自动恢复播放；重连失败时界面显示明确错误；
    下次断线时终端日志（mpv 进程已退出: signal: killed / exit status N）可定位断开原因
- [x] end-file reason=error 根因排查 + 修复 ✅（分支 fix/mpv-endfile-error，已实现待合并）
  - 根因（实证）：取流链路 ui → mpv loadfile → mpv 内置 ytdl_hook 调系统 yt-dlp 解析成功 →
    mpv/ffmpeg 打开 googlevideo 直链时**间歇性 HTTP 403 Forbidden**（YouTube 服务端风控/限流，
    本机 mpv v0.41 + yt-dlp 2026.07.04 复现，8 次试验失败 2 次）→ end-file reason=error，
    file_error="no audio or video data played"；失败后立即重试（重新解析拿新签名 URL）均成功——瞬态错误
  - 修复：
    1. pump 的 end-file error 透传 file_error 诊断文本（新类型 player.LoadFailedError，错误消息保留原前缀）
    2. mpv 启动参数 + --ytdl-format=bestaudio（纯音频播放器只需音频流，避免同时开视频+音频两个流，403 暴露面减半）
    3. UI 取流失败自动重试：每曲最多 2 次（延迟 2s，重新 loadfile=重新取流拿新 URL），
       代际计数器（playGen）保证用户换曲后过期重试丢弃；重试耗尽后队列有下一首则跳过继续连播
       （不再中断整个连播），单曲则停止并提示手动重试；file_error 映射为可操作中文提示
  - 测试：LoadFailedError 携带/缺失 file_error、--ytdl-format 启动参数、重试成功/耗尽跳过/耗尽停止/过期重试丢弃、
    hint 映射；全量 build/vet/test -race + player/ui -count=3 全绿

## 顶部 Tab 标签栏追加需求（用户已确认样式/图标细节）

- [x] 设计确认：四标题 + 当前页高亮（Bold+212 粉）+ 首页播放状态图标（⏵/⏸/⏹）+ 队列数量（仅 >0 时显示）+ Tab 栏占首行高度减 1
- [x] 实现：ui/tabs.go（tabBar 纯函数）+ root.go（View 拼 tabBar、WindowSizeMsg 四页 height-1）+ 补 queuePage.setSize 既有遗漏 ✅（commit 3bd374c + f2a7d12 + 7df653b，已合并 master）
- [x] 审查：reviewer 两轮批准（go mod tidy + 测试复用 tabStyle + 注释修正）✅

## Tab 栏鼠标交互追加需求（用户确认：点击切换 + hover 高亮）

- [x] 设计确认：点击标签切换页面（MouseMsg 0-based 首行 Y==0 + 标签列区间命中）+ 悬停下划线高亮（Underline，当前页高亮优先，移出清除）+ 启用 WithMouseAllMotion
- [x] 实现：main.go 加 WithMouseAllMotion；ui/tabs.go 重构 tabSegments/tabHitAt + tabHoverStyle；ui/root.go onMouse + hoverTab 字段 ✅（commit 22a1b4d + 4e69755，merge 7e87b3a 已合并 master）
- [x] 审查：reviewer 两轮批准（文档措辞如实修正：bubbles v1.0.0 列表无鼠标处理，仅歌词 viewport 滚轮；补幂等/hover 清除断言）✅
- [ ] 验收：全量 build/vet/test -race 全绿；真机确认——标签栏视觉（高亮样式/图标/队列数量）+ 鼠标点击切换 + 悬停效果

## 播放列表追加需求（用户已确认设计）

- [x] 设计确认：Tab 重排 5 页（首页/队列/播放列表/搜索/历史，数字键 1-5 直达、Tab/Ctrl+→ 正向循环、Shift+Tab/Ctrl+← 反向循环）+ 播放列表页两级视图（概览↔详情）+ 全局 p 键选择器（搜索/历史/播放列表详情页添加到列表）+ JSON 持久化（~/.config/music-tui/playlists.json，损坏 .corrupt-N 备份重建）
- [x] 实现：playlists 包（多列表 CRUD/原子持久化/损坏返回错误）+ queue.ReplaceAll 整列表替换（指针 clamp）+ ui/playlists.go 两级视图页与 plPicker 选择器 + root/main 集成（5 Tab 重排、全局 p、notice 绿色横幅）✅（commit dc2c152 + fb9bd57，已合并 master）
- [x] 测试：playlists 15 单测 + ui 19 集成测试 + queue ReplaceAll 3 测试，全量 build/vet/test -race 全绿 ✅（commit 58b428c）
- [ ] 验收：真机验收——创建/重命名/删除列表、搜索页 p 添加、播放列表加载播放连播/随机

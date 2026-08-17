# 封面高清渲染：等比例 ScaleFit（最终方案） 设计

日期：2026-08-17
状态：已定稿（最终方案 = 半块自绘；协议渲染已尝试后放弃）

## 背景与问题

- 封面源已是高清：`cover.Fetch` 沿 `maxresdefault→sddefault→hqdefault→mqdefault` 降级链下载，
  实测缓存 72/80 个 YouTube 封面为 1280×720（maxresdefault），本地内嵌封面 800~5120px。
  问题不在源，在渲染。
- 现状渲染：`ui/home.go` 固定 `coverW=30, coverH=17`（不随窗口变化）；`renderCoverArt`
  把整张图**拉伸填满** 30×34px 网格（16:9 的 YouTube 封面被竖向压扁约 50%，即"压缩感"来源）；
  最近邻采样导致大图缩小后摩尔纹/糊。

## 决策（用户拍板，含演进）

1. **封面框大小不变**：保持 30×17 字符格（布局数学、歌词列宽、鼠标命中、占位框全部不动）。
2. **等比例 ScaleFit**（最终方案核心）：图片在封面框内等比例缩放（contain），
   不超出封面框、不裁切、不畸变；图片居中，框内留白输出背景色空格。
3. **协议渲染（kitty/sixel 真图）已尝试并放弃**：
   - 第一版（自写协议序列嵌入 view 字符串）：bubbletea 渲染管线把 `\x1b_G`/`\x1bPq`
     载荷当文本单元格计算 → 整页崩坏；
   - 第二版（go-termimg 委托 + 绝对坐标外带覆盖层）：真实终端上图像位置/时序不稳
     （帧差量、六边形行数、放置时机不可控）；
   - 结论：本 TUI（bubbletea cell 模型渲染）不适合承载图形协议字节，
     **最终只保留半块自绘路径**。"高清"由高清源图（1280×720）+ 双线性降采样体现，
     对任意终端布局恒定、零协议字节风险。

## 最终实现（已合入）

### 1. 几何纯函数（ui/covergeom.go）
`scaleFit(srcW, srcH, boxW, boxH) (imgW, imgH)`
像素空间等比例缩放：`scale = min(boxW/srcW, boxH/srcH)`，Round 取整不超框、
比例保持（取整误差 <1px）、不小于 1×1；输入含 0/负返回 (0,0)。

### 2. 半块渲染器（ui/coverart.go）
`renderCoverArt(img, w, h)`：
- 源图 ScaleFit 到 w 列 × (h*2) 像素框（2px/格纵向）内并居中；
- **双线性采样**（4 邻点按分数权重混合，越界钳位）替代最近邻——降采样抗摩尔纹/防糊；
- 框内留白输出背景色空格；输出恒 w 列 × h 行、纯 256 色 SGR、无尾随换行、
  与终端特性无关、布局恒定。

### 3. 集成（ui/home.go，原始代码，未改动）
- `setCover` 解码 → `renderCoverArt(img, coverW, coverH)` → `coverRenderCache`；
- 帧固定 30×17，resize 不重渲、缓存不失效；歌词列宽/鼠标命中/占位框不变。

## 测试

- `scaleFit` 几何：方形/16:9/竖图/极小/超宽/放大填框/非法输入（不超框、比例保持）；
- `renderCoverArt`：16:9 图上下留白无 ▀ 且中间含 ▀（居中）、恒定网格、
  单色退化全空格、1×1 不 panic、空图 ""、256 色 SGR；
- 既有 home 测试（封面缓存 17 行、窄窗口裁剪、全屏撑满等）全部保持通过。

## 不做（YAGNI / 已放弃）

- 封面框随窗口动态变化（用户明确保持 30×17）
- 旧缓存刷新重下（已验证缓存已是高清源）
- 动画/多图嵌入（无需求）
- iTerm2 协议（无需求）

## 最终演进（真实终端验证后定稿）

1. **kitty 内联协议渲染**（自动启用，`KITTY_WINDOW_ID`/能力查询判定）：封面显示真图。
   关键实现细节：
   - 几何按**像素空间**（框 = width×cellW × height×cellH，cell 高≠宽），方形封面全宽显示；
   - 序列为行内 APC（零宽）+ U+10EEEE 占位符网格 + a=p 放置，bubbletea 逐行原始直通
     已验证可承载；cell 尺寸由 CSI 16t 查询（kitty 支持）或 env 覆盖。
2. **sixel 仅显式启用**（`MUSIC_TUI_COVER=sixel`）：foot 等网格驻留型终端在图像区域
   写入任何字符即擦除图像（foot 源码 sixel.c:`sixel_overwrite_by_row` 实测），自动
   启用经验不可靠；konsole 等覆盖型终端可持久。默认关闭。
3. **全部其它终端（foot/konsole/普通）→ 像素风**（半块自绘 ScaleFit+双线性，纯 256 色
   SGR，任何环境布局恒定）。

## 遗留说明

- 16:9 封面在 30×17 框内 ScaleFit 后上下留白（约 4 行）——等比例缩放的固有结果，
  用户确认接受（保证不畸变、不裁切）。
- go.mod/go.sum 已还原（不再依赖 third_party/go-termimg）。
# 封面高清渲染：等比例 ScaleFit + kitty/sixel 真图 设计

日期：2026-08-17
状态：已与用户确认（双路径统一等比例、封面框 30×17 不变）

## 背景与问题

- 封面源已是高清：`cover.Fetch` 沿 `maxresdefault→sddefault→hqdefault→mqdefault` 降级链下载，
  实测缓存 72/80 个 YouTube 封面为 1280×720（maxresdefault），本地内嵌封面 800~5120px。
  问题不在源，在渲染。
- 现状渲染：`ui/home.go` 固定 `coverW=30, coverH=17`（不随窗口变化）；`renderCoverArt`
  把整张图**拉伸填满** 30×34px 网格（16:9 的 YouTube 封面被竖向压扁约 50%，即"压缩感"来源）；
  最近邻采样导致大图缩小后摩尔纹/糊；`setSize` 明确不清缓存不重渲染。

## 决策（用户拍板）

1. **渲染后端**：半块自绘为底座 + **终端支持图片协议时渲染真图**（kitty/sixel）。
2. **封面框大小不变**：保持 30×17 字符格（27.9K home.go 的布局数学、歌词列宽、
   鼠标命中、占位框全部不动）。
3. **两条路径统一等比例 ScaleFit**：图片不超出封面框、不裁切、不畸变；
   差异仅在清晰度（真图 vs 半块色块）。

## 几何模型（纯函数，TDD 核心）

```
scaleFit(srcW, srcH int, boxW, boxH int) (imgW, imgH int)
  scale = min(float64(boxW)/srcW, float64(boxH)/srcH)
  imgW = max(int(srcW*scale), 1); imgH = max(int(srcH*scale), 1)
  // 不变量：imgW <= boxW && imgH <= boxH，比例保持（单元格取整误差 < 1px）
```

- 半块路径框：boxW=30, boxH=34（30 列 × (17 行×2px/格)）
- 协议路径框：boxW=30×cellW, boxH=17×cellH（cellW/H = 终端字体像素尺寸）

## 渲染路径 A：kitty 协议

前提：探测命中 kitty/ghostty/wezterm 且非 tmux 默认回退（见探测节）。

序列结构（整串缓存进 coverRenderCache，换歌时重建，resize 不重建）：

1. **传输**：源图双线性缩放到目标像素尺寸 → PNG 编码 → `a=t,f=100,U=1,i=<id>;b64` 分块发送
2. **占位符网格**：U+10EEEE + 变音符（编码行列/id）网格，**恒 30 列×17 行 in-flow**
   —— 这是布局确定性的根本契约。图像子矩形按几何模型居中放置，框内其余格输出背景空格
3. **放置**：`a=p,i=<id>,q=2` 让终端将占位符替换为图像（网格驻留，随文本滚动）

协议要点（已核实 kitty 官方文档 / tmux PR #5274 实现）：
- `c/r` = 图像占用单元格尺寸，单一指定时按原图比例自动推另一维；
  本设计传输时已预缩放，显式给满 c/r 避免终端二次换算
- 占位符方案让图像在 tmux（外层为真 kitty）下也可网格驻留；本设计在 tmux 内默认仍回退半块
- 传输尺寸 s/v = 目标像素尺寸（由 cellW/H 决定），配合 c/r 使终端按原生分辨率显示

## 渲染路径 B：sixel 协议

1. 源图 ScaleFit 后**合成到全帧画布**（boxW×boxH 像素，留边用深色 #101010 填充）
   —— 避免 sixel 透明背景兼容问题，整块输出天然占满 17 行
2. sixel 编码 → DCS 序列 `\x1bPq ... \x1b\`
3. 按 `ceil(imgPxH/cellH)` 补足空白行，**输出恒 17 行**

## 回退路径 C：半块自绘（普通终端/tmux/非交互）

- `renderCoverArt(img, w, h)` 改为 **ScaleFit + 双线性采样**：
  - 源图按几何模型缩放到框内网格（30 列×17 行 = 30×34px）
  - 双线性采样替代最近邻（降采样防摩尔纹/糊，体现"高清"）
  - 图像居中，框内其余格输出背景色空格（沿用 256 色 SGR 格式，输出恒 30 列×17 行）
- 输出格式不变：纯 256 色 SGR、无尾随换行、恒定 w×h 网格

## 能力探测（新文件 ui/coverproto.go，进程级缓存）

顺序（高→低优先级）：

1. 显式环境变量 `MUSIC_TUI_IMAGE=kitty|sixel|halfblocks`（最高优先，测试注入用）
2. 环境提示：`KITTY_WINDOW_ID` 或 `TERM_PROGRAM∈{ghostty,wezterm,rio}` → kitty；
   `TERM` 含 `sixel` → sixel
3. 非交互（无 stdin TTY，测试/CI）→ 恒半块（不发起任何查询）
4. **tmux/screen 内默认回退半块**（外层真 kitty 无法可靠探测；环境变量可强制）
   - 注：占位符方案原理上可穿透 tmux，但探测不可靠，默认保守回退
5. 字体像素尺寸 cellW/H：启动时一次 `CSI 16t` 查询（失败回退 8×16）；
   环境变量 `MUSIC_TUI_CELL_W/H` 可覆盖；协议路径需要

探测结果缓存进包级 var（sync.Once 保护）。

## 集成（ui/home.go）

- `setCover`：解码 → 按探测模式选路径生成渲染缓存 → `coverRenderCache`
- `coverView`/占位框/歌词列宽/鼠标命中/`setSize` 行为**全部不动**（帧固定）
- homeModel 持有解码后的 `image.Image`（每首歌一张，1280×720 RGBA ≈ 3.7MB 可接受；
  协议路径一次 PNG 编码缓存，半块路径一次渲染缓存）

## 测试计划（TDD）

1. **几何纯函数** `scaleFitGeometry`：方形/16:9/竖图/极小(1×1)/超大(4K) → 不超框、比例保持
2. **半块 ScaleFit**：输出网格恒 30×17；16:9 源图图像区居中、上下留白背景空格；
   单色退化全空格、1×1 不 panic、空图 ""；256 色 SGR 断言不变
3. **kitty 序列**：含 `a=t/a=p,q=2`、占位符网格 30×17、行数契约 17 行×30 列
   （ansi.StringWidth 断言）、图像子矩形居中且 ≤30×≤17
4. **sixel 序列**：含 `\x1bPq`、输出恒 17 行
5. **探测**：env 注入（`MUSIC_TUI_IMAGE`）单测；非交互默认半块
6. **集成回归**：setCover 后 coverView 恒 17 行；resize 缓存不变（帧固定，现有测试适配）
7. 现有 coverart/home 测试仅断言尺寸者保持绿（适配 ScaleFit 后尺寸不变量不变）

## 不做（YAGNI）

- iTerm2 inline 协议（用户只提 kitty/sixel；后续可加）
- 动画/多图嵌入（无需求）
- 封面框随窗口动态变化（用户明确保持 30×17）
- 旧缓存刷新重下（已验证缓存已是高清源）
- go-termimg 全景接入（保留其探测理念，渲染自实现以满足行数契约）
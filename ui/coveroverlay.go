package ui

// coveroverlay.go — sixel 封面图像的外带覆盖写出：把 coverrender.Sixel 生成的全帧
// DCS 画到屏幕绝对坐标（\x1b[s 存光标 → CUP 定位 → DCS → \x1b[u 恢复），不与
// bubbletea 的文本网格交互（像素驻留且覆盖文本，布局文本流只放半块色块底座）。
//
// 时序：view() 内同步写出（先于渲染器 flush）——sixel 像素绘制于终端屏幕层，
// 随后的布局文本写在其下方不影响显示；无延迟状态机需求（区别于此前的 kitty 占位符
// 方案：占位符是"格内"文本，会被后续空白覆盖，需要精确的帧间时序）。换歌/回退时
// 在同一坐标重绘一个背景色全帧 DCS 清除旧图像。

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"music-tui/coverrender"
	"music-tui/logger"
)

// overlayOut 覆盖层输出目标（包级可替换，测试捕获用）。真实运行 = os.Stdout。
// 写出与 bubbletea 渲染器的帧 flush（ticker goroutine，60fps）并发写同一 fd——
// overlayMu 保证覆盖层自身的写出原子性；载荷本身已做尺寸压缩（sixel 量化上限
// + RLE，实测 4KB 级），交错窗口极小。DCS 一旦损坏终端整体丢弃，下次 token
// 变化（换歌/resize）重写自愈。
var (
	overlayMu sync.Mutex
	overlayOut io.Writer = os.Stdout
)

type sixelState struct {
	mu     sync.Mutex
	token  string // 已写出位置标识（track|mode|row|col）
	drawn  bool   // 当前画面是否有已写出的六像素（切歌/回退时需先清）
	posRow int    // 上次写出的屏幕行（0 基，清除用）
	posCol int
	gen     int  // 每一次 token 变更/clear 递增，防幽灵重画
	pending bool // 是否待重画（foot 网格驻留擦除后延迟重画）
}

// blankCoverGrid 生成 w 列×h 行的纯空格网格（无任何 SGR/文本内容）——sixel 图像
// 模式的布局底座：图像覆盖在空白上，避免文字/色块底图干扰显示。
func blankCoverGrid(w, h int) string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", w)
	}
	return strings.Join(lines, "\n")
}

// coverScreenPos 计算封面框 30×17 内容的屏幕绝对坐标（0 基行/列），供六像素定位。
// 布局映射（与 renderMiddleView 完全一致）：
//   - 页面从屏幕第 hdr 行起，页面之下 overlayStatusRows 行状态栏 → 页面高 =
//     height - hdr - overlayStatusRows；中间区 midH = 页面高 - 2（底部进度/按钮行）；
//   - 中间区在页面内垂直居中，封面列 30 宽再在其中垂直居中：
//     vpad = (midH-coverH)/2（与 lipgloss.PlaceVertical 的 Center 顶衬一致）；
//   - 水平：歌词列宽 = width-coverW-4，块宽 = coverW+2+歌词宽，块水平居中 → 封面起点列。
// 窗口过小（中间区放不下封面）或尺寸未初始化时返回 (0,0) 表示不可画。
func coverScreenPos(width, height, hdr int) (row, col int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	midH := height - hdr - overlayStatusRows - 2
	if midH < coverH {
		return 0, 0
	}
	lyricsW := width - coverW - 4
	if lyricsW < 1 {
		lyricsW = 1
	}
	blockW := coverW + 2 + lyricsW
	col = (width - blockW) / 2
	if col < 0 {
		col = 0
	}
	row = hdr + (midH-coverH)/2
	return row, col
}

// writeSixel 在屏幕 (row,col) 写出 payload（全帧 DCS，铺满 30×17 格）。单次写保证
// 序列原子落盘。
func writeSixel(row, col int, payload string) {
	if payload == "" || overlayOut == nil {
		return
	}
	overlayMu.Lock()
	defer overlayMu.Unlock()
	var sb string
	sb = "\x1b[s\x1b[" + fmt.Sprintf("%d;%dH", row+1, col+1) + payload + "\x1b[u"
	_, _ = io.WriteString(overlayOut, sb)
}

// sixelClear 若已绘制六像素则在原位置重绘背景色全帧清除，并复位写出状态。
// 覆盖型终端（konsole/kitty 等，像素驻留）缩小窗口致使封面隐藏时，不清理
// 会残影罩住居中歌词；foot 网格驻留型会被行重写自愈，清除同样无害。
func sixelClear(st *sixelState) {
	if st == nil {
		return
	}
	st.mu.Lock()
	if !st.drawn {
		st.pending = false
		st.mu.Unlock()
		return
	}
	rr, cc := st.posRow, st.posCol
	st.mu.Unlock()
	cellW, cellH := coverrender.FontCellSize()
	clear := coverrender.SixelClear(coverW, coverH, cellW, cellH)
	writeSixel(rr, cc, clear)
	st.mu.Lock()
	st.drawn = false
	st.token = ""
	st.gen++
	st.pending = false
	st.mu.Unlock()
}

// clearSixel 清除已绘制的六像素（在最后一次写出位置重绘背景色全帧）。
func (m homeModel) clearSixel() homeModel {
	st := m.sixelSt
	if st == nil {
		st = &sixelState{}
		m.sixelSt = st
	}
	sixelClear(st)
	m.sixelPayload = ""
	return m
}

// ensureSixel 在 view() 内写出六像覆盖层：素材/位置（token）变化时重绘。
// 布局文本流不包含任何协议字节（DCS 仅经此外带写出）。
// 值接收者但状态落在共享 st 指针上（st.pending/gen 跨 view() 拷贝持久）。
func (m homeModel) ensureSixel() {
	// 封面隐藏（窗口宽 < 60 或 高 < 28）：不渲染封面区，也不外带写出图像。
	// 显示→隐藏过渡须清除已画出的六像素（覆盖型终端像素驻留：不清则旧封面
	// 残影罩住缩放后的居中歌词）；sixelPayload 保留，放大恢复到显示态时重绘。
	if m.coverHidden() {
		sixelClear(m.sixelSt)
		return
	}
	if m.coverMode != 2 || m.sixelPayload == "" || m.state.Track == nil {
		return
	}
	st := m.sixelSt
	if st == nil {
		return
	}
	// 页面起点 = 顶部 3 行（空行+Tab 栏+分隔线），页面之下 1 行状态栏
	row, col := coverScreenPos(m.width, m.height+overlayHdrRows+overlayStatusRows, overlayHdrRows)
	if row == 0 && col == 0 {
		logger.Debug("sixel 跳过: 窗口过小 (w=%d h=%d)", m.width, m.height)
		return // 窗口过小：封面被裁剪，不画
	}
	token := fmt.Sprintf("%s|%d|%d|%d", m.state.Track.ID, m.coverMode, row, col)
	st.mu.Lock()
	sameTokenAndDrawn := token == st.token && st.drawn
	pending := st.pending
	capturedGen := st.gen
	oldToken := st.token
	if sameTokenAndDrawn {
		if pending {
			st.pending = false
			st.mu.Unlock()
			payload := m.sixelPayload
			rr, cc := row, col
			gen := capturedGen
			logger.Info("sixel 重画: 中间区已重建 (row=%d col=%d payload=%d 字节 gen=%d)", rr, cc, len(payload), gen)
			// 捕获 st 指针可见的 gen；隐藏过渡会经 sixelClear 递增 gen/clear drawn，
			// 此处仅靠 gen 与 drawn 即可防幽灵重画，无需捕获已失效的 m 副本。
			go func(gen int, payload string, rr, cc int) {
				time.Sleep(45 * time.Millisecond)
				st.mu.Lock()
				ok := st.gen == gen && st.drawn
				st.mu.Unlock()
				if !ok {
					return
				}
				writeSixel(rr, cc, payload)
			}(gen, payload, rr, cc)
		} else {
			st.mu.Unlock()
		}
		return
	}
	oldTokenCopy := oldToken
	st.mu.Unlock()
	logger.Info("sixel 写出: row=%d col=%d (窗口 w=%d h=%d, 屏幕高=%d) payload=%d 字节 token=%q (旧=%q)",
		row, col, m.width, m.height, m.height+overlayHdrRows+overlayStatusRows, len(m.sixelPayload), token, oldTokenCopy)
	writeSixel(row, col, m.sixelPayload)
	st.mu.Lock()
	st.token = token
	st.drawn = true
	st.gen++
	st.pending = false
	st.posRow, st.posCol = row, col
	st.mu.Unlock()
}

// 布局常量：页面在屏幕中的偏移。
const (
	overlayHdrRows    = 3 // 顶部空行 + Tab 栏 + 分隔线
	overlayStatusRows = 1 // 底部状态栏
)
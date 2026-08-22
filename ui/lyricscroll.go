package ui

// 歌词视口行数常量。第二层防御（lyrics 已毫秒化，此处保证 viewport 同步稳固）：
// 即使上游时间戳已稳定，若视口 H 为偶数，H/2 上下不对称仍会导致居中抖动。
const (
	// lyricMaxLines 歌词视口最大行数：当前行恒居中（上 10 下 10），大窗口不再增大。
	lyricMaxLines = 21
	// lyricPadLines 视口外上下留白行数（窗口较小时保证歌词不贴中间区边缘）。
	lyricPadLines = 2
)

// lyricViewportHeight 歌词视口行数（动态）：min(21, midH−上下留白 2*lyricPadLines)，
// 至少 1 行，且强制为奇数以保证视口中心对称（偶数时视口中心在两行之间，
// 当前行无法精确居中，滚动时会偶发上下一行的抖动）。窗口大时 21 行封顶；
// 窗口小时按留白收缩（例如 midH=24 → 20→19 行，取奇数）。
func lyricViewportHeight(midH int) int {
	h := midH - lyricPadLines*2
	if h > lyricMaxLines {
		h = lyricMaxLines
	}
	if h < 1 {
		h = 1
	}
	// 保证视口中心对称：奇数时 H/2 上下各 (H-1)/2 行对称；偶数时中心在缝隙。
	if h%2 == 0 && h > 1 {
		h--
	}
	return h
}

// lyricScrollOffset 歌词视口滚动偏移：内容 = H/2 行空白 + N 行歌词 + H/2 行空白，
// 歌词行 idx 在内容中的行号 = H/2 + idx，显示在视口行 = H/2 + idx − YOffset。
// 令其恒等于视口中央行 H/2 → YOffset = idx，clamp 到 [0, N−1]。
// 防抖第二层防御：即使 lyrics 毫秒化后 LineAt 亚毫秒稳定，此处仍保证 YOffset 与
// viewport 几何（H 奇数、content = N+H-1、maxYOffset = N-1）无缝衔接，无富余抖动。
// 等价于参考公式（视口顶部行号，可为负）top = clamp(idx−H/2, −H/2, N−1−H/2)：
//
//	开头（idx=0）→ top=−H/2，首行在视口中央，上方整片空白；
//	中间 → top=idx−H/2，当前行恒居中；
//	结尾（idx=N−1）→ top=N−1−H/2，末行停在视口中央，下方可空白。
//
// 偏移与视口高 H 无关：padding 前导 H/2 行恰好抵消 H/2 的居中需求，故不依赖 H。
// viewport 侧 maxYOffset：H 恒为奇数（见 lyricViewportHeight），content 行数
// = N + 2·(H/2) = N + H − 1（奇数时 H/2 向下取整），maxYOffset = (N+H−1) − H
// = N−1 恰好，无富余；故返回 N−1 恒合法且不越界。
func lyricScrollOffset(idx, n int) int {
	if n <= 0 {
		return 0
	}
	off := idx
	if off < 0 {
		off = 0
	}
	if off > n-1 {
		off = n - 1
	}
	return off
}

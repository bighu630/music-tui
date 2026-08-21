package coverrender

import (
	"os"
	"strings"
	"sync"

	"music-tui/logger"
)

// Mode 封面渲染方式。
type Mode int

const (
	// ModeHalf 半块自绘回退（任何终端可用，纯 256 色 SGR，布局恒定）。
	ModeHalf Mode = iota
	// ModeKitty kitty 图形协议（APC 传输 + Unicode 占位符网格 + 放置；内联于布局流）。
	ModeKitty
	// ModeSixel sixel 协议（全帧画布 DCS；由集成层外带绝对定位写出）。
	ModeSixel
)

func (m Mode) String() string {
	switch m {
	case ModeKitty:
		return "kitty"
	case ModeSixel:
		return "sixel"
	default:
		return "halfblocks"
	}
}

// stdinIsTTY 可注入（测试模拟交互终端用），默认探测 os.Stdin 是否 TTY。
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

var (
	modeOnce sync.Once
	modeVal  Mode
)

// DetectMode 探测当前终端适用的封面渲染方式（进程级缓存，env 在测试中注入后须
// ResetModeCacheForTests）。判定顺序（高→低）：
//
//  1. 显式环境变量 MUSIC_TUI_COVER（kitty / sixel / halfblocks，大小写不敏感，
//     其他值忽略继续）：测试注入与用户强制手段。
//  2. 非交互（stdin 非 TTY，测试/CI/管道）→ ModeHalf：探测不了能力，不冒险。
//  3. tmux/screen 内 → ModeHalf：图形协议无法可靠穿透（无 passthrough 桥接时
//     外层不识别协议会把载荷当字面文本打印）。需要时用 MUSIC_TUI_COVER 显式强制。
//  4. 启动期能力查询结果（QueryCapability，真实终端应答）：kitty → ModeKitty。
//     sixel 不从此处启用（见下注）。
//  5. 环境提示：KITTY_WINDOW_ID 非空 / TERM_PROGRAM ∈ {ghostty,wezterm,rio} /
//     TERM 含 "kitty" → ModeKitty。
//  6. 默认 ModeHalf（半导体像素风）。
//
// 注：sixel 仅在 MUSIC_TUI_COVER=sixel 显式强制时使用——foot 等网格驻留型终端在
// 图像区域写入任何字符即擦除（歌词行重写 → 闪没），自动启用经验不可靠；需要
// sixel 的用户在确认自己的终端可持久（覆盖型，如 konsole）后以 env 开启。
func DetectMode() Mode {
	modeOnce.Do(func() { modeVal = computeMode() })
	return modeVal
}

func computeMode() Mode {
	// 1. 显式 env 覆盖（最高优先，非交互/隧道内也生效）
	if v := strings.ToLower(os.Getenv("MUSIC_TUI_COVER")); v != "" {
		switch v {
		case "kitty":
			return ModeKitty
		case "sixel":
			return ModeSixel
		case "halfblocks":
			return ModeHalf
		}
	}
	// 2. 非交互：探测不了能力，回退
	if !stdinIsTTY() {
		return ModeHalf
	}
	// 3. tmux 内：**优先看外层是否确实是 kitty**（KITTY_WINDOW_ID 只有 kitty 会设，
	// 透传进 pane 即铁证；tmux 3.5+ 会把 pane 的 kitty APC 原样中继给外层）。
	// 早期实现把 tmux → half 放在 KITTY_WINDOW_ID 检查之前，导致哪怕外层是 kitty
	// 也永远回退半块（回归：日志实测 tmux 内 KITTY_WINDOW_ID=1 仍 halfblocks）。
	// 收紧：KITTY_WINDOW_ID 可被终端模拟器配置跨会话泄漏（如 foot 继承 kitty 的
	// 环境），需经能力校验或 TERM 线索才信任，否则视为泄漏忽略。
	if os.Getenv("TMUX") != "" {
		if v := os.Getenv("KITTY_WINDOW_ID"); v != "" {
			if isTrustedKittyEnv() {
				return ModeKitty // 经能力查询或 TERM 线索确证 kitty，允许中继
			}
			logger.Debug("KITTY_WINDOW_ID 存在但未通过 kitty 能力校验，忽略（疑似跨终端泄漏） TERM=%q TERM_PROGRAM=%q lastQueryRaw=%q (tmux)", os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), lastQueryRaw)
			// 泄漏：不直接返回，继续走 kittyThroughTMUX 校验或回退
		}
		if kittyThroughTMUX {
			return ModeKitty // client_termname 含 kitty（无 KITTY_WINDOW_ID 场景）
		}
	}
	if os.Getenv("TMUX") != "" || strings.Contains(strings.ToLower(os.Getenv("TERM")), "screen") {
		return ModeHalf
	}
	// 4. 启动期能力查询结果（真实终端应答，优先于环境提示）
	//    kitty：内联占位符协议稳定可用。
	//    sixel：**不自动启用**——foot 等网格驻留型终端在图像区域任何字符写入时
	//    都会擦除图像（foot 源码 sixel.c: sixel_overwrite_by_row），行重写（歌词
	//    切换）即闪没；仅 konsole 等少数覆盖型终端能持久。需显式
	//    MUSIC_TUI_COVER=sixel 强制（覆盖型终端用户自己确认后启用）。
	if capabilityMode == ModeKitty {
		return ModeKitty
	}
	// 5. 环境提示（KITTY_WINDOW_ID 收紧：需能力校验或 TERM 线索，否则视为泄漏）
	if v := os.Getenv("KITTY_WINDOW_ID"); v != "" {
		if isTrustedKittyEnv() {
			return ModeKitty
		}
		logger.Debug("KITTY_WINDOW_ID 存在但未通过 kitty 能力校验，忽略（疑似跨终端泄漏） TERM=%q TERM_PROGRAM=%q lastQueryRaw=%q", os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), lastQueryRaw)
		// 视为泄漏，回落不返回，继续后续探测（lastQueryRaw 为空时保持 halfblocks，需用户显式 MUSIC_TUI_COVER）
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm", "rio":
		return ModeKitty
	}
	t := strings.ToLower(os.Getenv("TERM"))
	if strings.Contains(t, "kitty") {
		return ModeKitty
	}
	// 6. 默认（含 foot/mlterm/sixel 等六边形终端）：像素风（半块自绘）——
	//    sixel 需 MUSIC_TUI_COVER=sixel 显式强制
	return ModeHalf
}

// isTrustedKittyEnv 判断 KITTY_WINDOW_ID 是否可信：需经能力查询确证或 TERM/TERM_PROGRAM 线索确证，
// 避免 foot 等终端泄漏的 KITTY_WINDOW_ID 误触发 kitty 模式。
func isTrustedKittyEnv() bool {
	if capabilityMode == ModeKitty {
		return true
	}
	// Gi 兜底：kitty 独有响应前缀，ENODATA 亦含 Gi，避免载荷微差回落 halfblocks
	// （修复前 f=32+AAAA 仅 3 字节 <4 触发 ENODATA，参考 go-termimg f=24 使 3 字节满足）
	// ENOSUPPORT 表示不支持 kitty 图形协议，不视为可信
	if !strings.Contains(lastQueryRaw, "ENOSUPPORT") && (strings.Contains(lastQueryRaw, "OK") || strings.Contains(lastQueryRaw, "Gi=") || strings.Contains(lastQueryRaw, "_Gi")) {
		return true
	}
	t := strings.ToLower(os.Getenv("TERM"))
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	return strings.Contains(t, "kitty") || tp == "kitty" || tp == "ghostty" || tp == "wezterm" || tp == "rio"
}

// kittyThroughTMUX 是否已确证“外层 kitty + tmux 中继”的 kitty（仅当 main 查得
// 外层终端为 kitty 且 tmux 3.5+ 会中继 APC 时置位——防止 foot 等外层在 tmux
// 内收到 kitty APC 变乱码）。
var kittyThroughTMUX bool

// SetTMUXKittyRelay 确证“外层 kitty + tmux 中继”，允许在 tmux 内启用 kitty。
func SetTMUXKittyRelay(ok bool) {
	kittyThroughTMUX = ok
}

// ResetModeCacheForTests 清空进程级探测缓存（测试改 env 后调用）。
func ResetModeCacheForTests() {
	modeOnce = sync.Once{}
	capabilityMode = ModeHalf
	kittyThroughTMUX = false
	lastQueryRaw = ""
}
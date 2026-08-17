package coverrender

import (
	"os"
	"strings"
	"sync"
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
//  4. 环境提示：
//     - KITTY_WINDOW_ID 非空 → ModeKitty（kitty 终端恒设置）
//     - TERM_PROGRAM ∈ {ghostty, wezterm, rio} → ModeKitty
//     - TERM 含 "kitty" → ModeKitty
//     - TERM_PROGRAM ∈ {foot, mlterm} 或 TERM 含 "sixel" → ModeSixel
//  5. 默认 ModeHalf（保守）。
//
// 注意：仅以环境提示判定（不做终端往返查询）——TUI 运行期向终端发查询有抢占输入/
// 挂起风险；依赖强提示（KITTY_WINDOW_ID/TERM_PROGRAM）已足够区分主流终端。
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
	// 3. tmux/screen 内：保守回退
	if os.Getenv("TMUX") != "" || strings.Contains(strings.ToLower(os.Getenv("TERM")), "screen") {
		return ModeHalf
	}
	// 4. 环境提示
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return ModeKitty
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm", "rio":
		return ModeKitty
	case "mlterm":
		return ModeSixel
	}
	t := strings.ToLower(os.Getenv("TERM"))
	if strings.Contains(t, "kitty") {
		return ModeKitty
	}
	// foot 不设 TERM_PROGRAM，仅设 TERM=foot（原生支持 sixel）
	if strings.Contains(t, "foot") || strings.Contains(t, "sixel") {
		return ModeSixel
	}
	// 5. 默认
	return ModeHalf
}

// ResetModeCacheForTests 清空进程级探测缓存（测试改 env 后调用）。
func ResetModeCacheForTests() {
	modeOnce = sync.Once{}
}
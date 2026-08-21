// Package lyrics 提供 LRC 歌词解析与 lrclib API 客户端。
package lyrics

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LyricLine 一行歌词：Time 为该行开始显示的秒数，Text 为歌词文本。
type LyricLine struct {
	Time float64
	Text string
}

// Lyrics 歌词集合：Lines 按时间升序；Source 为来源标记（LyricsSourceAI
// = AI 增强路径，空 = 确定性匹配），仅作展示提示，不影响内容。
// 注意：本项目只接受带时间轴的同步歌词（用户要求：无时间轴的纯文本
// 歌词没有使用价值），不存在 Plain 形态。
type Lyrics struct {
	Lines  []LyricLine
	Source string
}

// timeToMs 将秒转为毫秒整数（四舍五入），用于消除二进制浮点亚毫秒误差。
func timeToMs(t float64) int {
	return int(math.Round(t * 1000))
}

// msToTime 将毫秒整数转回秒（float64），保证值为精确毫秒。
func msToTime(ms int) float64 {
	return float64(ms) / 1000.0
}

// Shift 把所有行的起始时间戳整体平移 delta 秒（负增量可产生 <0 的时间戳，
// 此处 clamp 到 0）。整体平移保持升序——clamp 产生的重复 0 时间戳可接受：
// LineAt 对重复时间戳值取最后一条（upper_bound 语义），二分查找不依赖严格递增。
// 空 Lines / nil 接收者安全。
// 实现按毫秒整数进行，避免浮点累积误差。
func (l *Lyrics) Shift(delta float64) {
	if l == nil {
		return
	}
	deltaMs := int(math.Round(delta * 1000))
	for i := range l.Lines {
		ms := timeToMs(l.Lines[i].Time) + deltaMs
		if ms < 0 {
			ms = 0
		}
		l.Lines[i].Time = msToTime(ms)
	}
}

// LineAt 返回"时间戳 ≤ pos 的最后一行"（upper_bound 语义）的下标与文本；
// 时间戳重复时取最后一条；pos 早于第一行或歌词为空时返回 (-1, "")。
// 二分查找，O(log n)。
// 为消除亚毫秒浮点抖动，比较时按毫秒整数进行（Round 到 ms）。
func (l *Lyrics) LineAt(pos float64) (int, string) {
	if len(l.Lines) == 0 {
		return -1, ""
	}
	posMs := timeToMs(pos)
	lo, hi := 0, len(l.Lines)
	for lo < hi {
		mid := (lo + hi) / 2
		if timeToMs(l.Lines[mid].Time) <= posMs {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx := lo - 1
	if idx < 0 {
		return -1, ""
	}
	return idx, l.Lines[idx].Text
}

// timeTagRe 匹配 [mm:ss]、[mm:ss.x]、[mm:ss.xx]、[mm:ss.xxx]
// （小数分隔符支持 . 与 : 两种写法）。
var timeTagRe = regexp.MustCompile(`^(\d+):(\d{1,2})(?:[.:](\d{1,3}))?$`)

// parseTimeTag 解析单个时间标签（不含方括号），返回秒数与是否合法。
// 解析以整数毫秒为基准：totalMs = mm*60*1000 + ss*1000 + fracMs，
// 其中 fracMs 根据小数位数补齐到毫秒（1位→*100，2位→*10，3位→*1），
// 再返回 float64(totalMs)/1000，保证 2 位与 3 位同值二进制一致。
func parseTimeTag(tag string) (float64, bool) {
	m := timeTagRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	mm, err := strconv.Atoi(m[1])
	if err != nil || mm > math.MaxInt64/60 {
		// Atoi 失败（超长数字溢出）或 mm*60 会溢出 int64 时视为非法，
		// 避免负时间行破坏 LineAt 的二分查找。
		return 0, false
	}
	ss, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, false
	}
	if ss > 59 {
		return 0, false
	}
	fracMs := 0
	if m[3] != "" {
		fracVal, _ := strconv.Atoi(m[3])
		switch len(m[3]) {
		case 1:
			fracMs = fracVal * 100
		case 2:
			fracMs = fracVal * 10
		case 3:
			fracMs = fracVal
		}
	}
	// 整数毫秒基准计算：优先用 int64 精确计算，避免 float 误差；
	// mm 极大时 totalMs 可能溢出 int64，此时退化为 float64 计算
	//（精度损失无害，结果仍是巨大正数，排序靠后）。
	// 先检测 mm*60*1000 是否溢出。
	const msPerMin = int64(60 * 1000)
	const msPerSec = int64(1000)
	if int64(mm) > (math.MaxInt64-int64(fracMs)-int64(ss)*msPerSec)/msPerMin {
		// 溢出风险：用 float64 兜底
		return float64(mm)*60 + float64(ss) + float64(fracMs)/1000.0, true
	}
	totalMs := int64(mm)*msPerMin + int64(ss)*msPerSec + int64(fracMs)
	return float64(totalMs) / 1000.0, true
}

// extractTimestamps 剥离行首连续的时间标签，返回剩余文本与时间列表。
// 行首出现非时间标签（如 [ti:xxx] 元数据）时视为元数据行，返回 ok=false。
func extractTimestamps(line string) (text string, times []float64, ok bool) {
	rest := line
	for strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			return "", nil, false
		}
		tag := rest[1:end]
		t, isTime := parseTimeTag(tag)
		if !isTime {
			return "", nil, false
		}
		times = append(times, t)
		rest = rest[end+1:]
	}
	if len(times) == 0 {
		return "", nil, false
	}
	return strings.TrimSpace(rest), times, true
}

// ParseLRC 解析 LRC 文本为 *Lyrics；时间相同的行保留且相对顺序稳定。
// 元数据标签（[ti:]/[ar:]/[al:] 等）与无时间戳的行被忽略。
func ParseLRC(data []byte) (*Lyrics, error) {
	var lines []LyricLine
	for _, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		text, times, ok := extractTimestamps(raw)
		if !ok {
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		for _, t := range times {
			lines = append(lines, LyricLine{Time: t, Text: text})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Time < lines[j].Time
	})
	return &Lyrics{Lines: lines}, nil
}

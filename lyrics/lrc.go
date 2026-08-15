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

// Lyrics 歌词集合：Lines 按时间升序；Plain 为无时间戳的纯文本兜底；
// Source 为来源标记（LyricsSourceAI = AI 增强路径，空 = 确定性匹配），
// 仅作展示提示，不影响内容。
type Lyrics struct {
	Lines  []LyricLine
	Plain  string
	Source string
}

// LineAt 返回"时间戳 ≤ pos 的最后一行"（upper_bound 语义）的下标与文本；
// 时间戳重复时取最后一条；pos 早于第一行或歌词为空时返回 (-1, "")。
// 二分查找，O(log n)。
func (l *Lyrics) LineAt(pos float64) (int, string) {
	if len(l.Lines) == 0 {
		return -1, ""
	}
	lo, hi := 0, len(l.Lines)
	for lo < hi {
		mid := (lo + hi) / 2
		if l.Lines[mid].Time <= pos {
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
	frac := 0.0
	if m[3] != "" {
		switch len(m[3]) {
		case 1:
			frac = float64(m[3][0]-'0') / 10
		case 2:
			frac = (float64(m[3][0]-'0')*10 + float64(m[3][1]-'0')) / 100
		case 3:
			frac = (float64(m[3][0]-'0')*100 + float64(m[3][1]-'0')*10 + float64(m[3][2]-'0')) / 1000
		}
	}
	// 用 float64 计算彻底消除 int 溢出：mm 接近 MaxInt64/60 时
	// mm*60+ss 可能环绕为负，float64 范围大不可能溢出（超大 mm 的
	// 精度损失无害，结果仍是巨大正数，排序靠后）。
	return float64(mm)*60 + float64(ss) + frac, true
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
		for _, t := range times {
			lines = append(lines, LyricLine{Time: t, Text: text})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Time < lines[j].Time
	})
	return &Lyrics{Lines: lines}, nil
}

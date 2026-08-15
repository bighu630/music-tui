package lyrics

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// lrcCache 是拉取到的歌词本地缓存（AI 增强路径专用）：
// 文件名 = 清洗后的 "title-artist"，同步歌词存 .lrc（纯 LRC 文本），
// 纯文本兜底存 .txt，命中直接读文件、不发 lrclib 请求。
type lrcCache struct {
	dir string
}

// newLRCCache 创建歌词缓存目录。
func newLRCCache(dir string) (*lrcCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建歌词缓存目录: %w", err)
	}
	return &lrcCache{dir: dir}, nil
}

// Get 按 title/artist 查询：优先同步歌词（.lrc），退回纯文本（.txt）。
func (c *lrcCache) Get(title, artist string) (*Lyrics, bool) {
	base := c.baseName(title, artist)
	if data, err := os.ReadFile(filepath.Join(c.dir, base+".lrc")); err == nil {
		if ly, err := ParseLRC(data); err == nil && len(ly.Lines) > 0 {
			return ly, true
		}
	}
	if data, err := os.ReadFile(filepath.Join(c.dir, base+".txt")); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			return &Lyrics{Plain: text}, true
		}
	}
	return nil, false
}

// Put 缓存歌词：同步歌词序列化为 LRC 文本（毫秒精度，ParseLRC 可
// 无损回读），纯文本按原文存储；两者皆空不写文件。
func (c *lrcCache) Put(title, artist string, ly *Lyrics) {
	if ly == nil {
		return
	}
	base := c.baseName(title, artist)
	switch {
	case len(ly.Lines) > 0:
		var sb strings.Builder
		for _, ln := range ly.Lines {
			fmt.Fprintf(&sb, "[%02d:%02d.%03d]%s\n",
				int(ln.Time)/60, int(ln.Time)%60, millis(ln.Time), ln.Text)
		}
		_ = writeFileIfChanged(filepath.Join(c.dir, base+".lrc"), sb.String())
	case ly.Plain != "":
		_ = writeFileIfChanged(filepath.Join(c.dir, base+".txt"), ly.Plain)
	}
}

// millis 取秒的小数部分毫秒（0-999），round 消除浮点噪声。
func millis(t float64) int {
	return int(math.Round((t - math.Floor(t)) * 1000))
}

// writeFileIfChanged 仅在内容变化时写文件（幂等重复缓存不触发 IO）。
func writeFileIfChanged(path, content string) error {
	if data, err := os.ReadFile(path); err == nil && string(data) == content {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// unsafeNameRe 文件名不安全字符（跨平台路径分隔符/通配符等）。
var unsafeNameRe = regexp.MustCompile(`[\\/:*?"<>|]`)

// maxNameBytes 文件名主干最大字节数：留出后缀（.lrc/.txt）空间，
// 保证含后缀全长 < 255 字节（主流文件系统上限）。
const maxNameBytes = 200

// baseName 生成缓存文件名主干：清洗 + 按字节截断（不切断多字节
// 字符）；结果为空时回落 "unknown"。
func (c *lrcCache) baseName(title, artist string) string {
	name := unsafeNameRe.ReplaceAllString(strings.TrimSpace(title)+"-"+strings.TrimSpace(artist), "-")
	budget := maxNameBytes
	var sb strings.Builder
	for _, r := range name {
		if sb.Len()+len(string(r)) > budget {
			break
		}
		sb.WriteRune(r)
	}
	name = strings.TrimSpace(sb.String())
	if name == "" || name == "-" {
		name = "unknown"
	}
	return name
}

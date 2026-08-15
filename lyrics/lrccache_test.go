package lyrics

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLRCCacheRoundtripSynced 同步歌词存取后内容一致（毫秒级误差内）。
func TestLRCCacheRoundtripSynced(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	ly := &Lyrics{Lines: []LyricLine{
		{Time: 12.345, Text: "故事的小黄花"},
		{Time: 17.0, Text: "从出生那年就飘着"},
	}}
	c.Put("晴天", "周杰伦", ly)

	got, ok := c.Get("晴天", "周杰伦")
	if !ok {
		t.Fatal("Get 未命中")
	}
	if len(got.Lines) != 2 {
		t.Fatalf("Lines = %d, want 2", len(got.Lines))
	}
	for i := range ly.Lines {
		if got.Lines[i].Text != ly.Lines[i].Text {
			t.Errorf("Lines[%d].Text = %q, want %q", i, got.Lines[i].Text, ly.Lines[i].Text)
		}
		if d := math.Abs(got.Lines[i].Time - ly.Lines[i].Time); d > 0.001 {
			t.Errorf("Lines[%d].Time = %v, want %v", i, got.Lines[i].Time, ly.Lines[i].Time)
		}
	}
}

// TestLRCCacheSyncOnly 只缓存带时间轴的同步歌词（无 .txt 形态）。

// TestLRCCacheMiss 未知名不命中。
func TestLRCCacheMiss(t *testing.T) {
	c, err := newLRCCache(t.TempDir())
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	if _, ok := c.Get("不存在的歌", "某人"); ok {
		t.Error("未知名竟然命中")
	}
}

// TestLRCCacheSanitize 文件名清洗：路径分隔符/通配符等不可落入文件名。
func TestLRCCacheSanitize(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	ly := &Lyrics{Lines: []LyricLine{{Time: 1, Text: "x"}}}
	c.Put("a/b\\c:d*e?f\"g<h>i|j", "artist", ly)

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("文件数 = %d, want 1（不安全的字符必须被清洗）", len(entries))
	}
	name := entries[0].Name()
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		t.Errorf("文件名仍含不安全字符: %q", name)
	}
	if _, ok := c.Get("a/b\\c:d*e?f\"g<h>i|j", "artist"); !ok {
		t.Error("清洗后的文件名无法命中")
	}
}

// TestLRCCacheLongTitle 超长标题截断文件名（防文件系统名长度限制）。
func TestLRCCacheLongTitle(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	long := strings.Repeat("晴", 500)
	c.Put(long, "艺术家", &Lyrics{Lines: []LyricLine{{Time: 1, Text: "x"}}})
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("文件数 = %d, want 1", len(entries))
	}
	if len(entries[0].Name()) > 255 {
		t.Errorf("文件名过长: %d 字节", len(entries[0].Name()))
	}
	if _, ok := c.Get(long, "艺术家"); !ok {
		t.Error("截断文件名无法命中")
	}
}

// TestLRCCacheSyncOnly 缓存只产出 .lrc 文件；无时间轴的 Lyrics 不写盘；
// Get 不识别 .txt（sync-only 语义，纯文本歌词整体不采用）。
func TestLRCCacheSyncOnly(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	c.Put("晴天", "周杰伦", &Lyrics{Lines: []LyricLine{{Time: 1, Text: "synced"}}})
	c.Put("无时间轴", "某人", &Lyrics{})
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("文件数 = %d, want 1（只缓存同步歌词）", len(entries))
	}
	if entries[0].Name() != "晴天-周杰伦.lrc" {
		t.Errorf("文件名 = %q, want 晴天-周杰伦.lrc", entries[0].Name())
	}
	// 手工放置 .txt 也不得命中（sync-only）
	if err := os.WriteFile(filepath.Join(c.dir, "手工.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("手工", ""); ok {
		t.Error(".txt 不应被当作歌词缓存命中")
	}
}

// TestLRCCacheEmptyNotCached 空歌词不写文件。
func TestLRCCacheEmptyNotCached(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	c.Put("晴天", "周杰伦", &Lyrics{})
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("空歌词不应缓存: %v", entries)
	}
}

// TestLRCCacheFileFormat 缓存文件内容为纯 LRC 文本（可被 ParseLRC 直接解析）。
func TestLRCCacheFileFormat(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	c.Put("晴天", "周杰伦", &Lyrics{Lines: []LyricLine{{Time: 65.5, Text: "刮风这天"}}})
	data, err := os.ReadFile(filepath.Join(dir, "晴天-周杰伦.lrc"))
	if err != nil {
		t.Fatalf("读取缓存文件: %v", err)
	}
	if !strings.Contains(string(data), "刮风这天") {
		t.Errorf("缓存内容 = %q", data)
	}
	if ly, err := ParseLRC(data); err != nil || len(ly.Lines) != 1 {
		t.Errorf("缓存文件无法被 ParseLRC 解析: %v %+v", err, ly)
	}
}

// TestLRCCacheSanitizeControlChars 控制字符（换行等）不得落入文件名。
func TestLRCCacheSanitizeControlChars(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	ly := &Lyrics{Lines: []LyricLine{{Time: 1, Text: "x"}}}
	c.Put("晴天\n恶意", "艺人\r\t", ly)
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("文件数 = %d, want 1", len(entries))
	}
	if strings.ContainsAny(entries[0].Name(), "\n\r\t\x00") {
		t.Errorf("文件名含控制字符: %q", entries[0].Name())
	}
	// 前导/尾随点与空格（Windows 隐藏文件/不可见后缀风险）
	c.Put(".hidden", "artist ", ly)
	for _, e := range mustReadDir(t, c.dir) {
		name := e.Name()
		base := strings.TrimSuffix(name, ".lrc")
		if base == ".hidden-artist" {
			t.Errorf("文件名以点开头: %q", name)
		}
		if strings.HasSuffix(base, " ") {
			t.Errorf("文件名以空格结尾: %q", name)
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

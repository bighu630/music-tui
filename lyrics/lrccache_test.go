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

// TestLRCCacheRoundtripPlain 纯文本歌词存取。
func TestLRCCacheRoundtripPlain(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	c.Put("晴天", "周杰伦", &Lyrics{Plain: "故事的小黄花\n从出生那年就飘着"})

	got, ok := c.Get("晴天", "周杰伦")
	if !ok {
		t.Fatal("Get 未命中")
	}
	if got.Plain != "故事的小黄花\n从出生那年就飘着" {
		t.Errorf("Plain = %q", got.Plain)
	}
	if len(got.Lines) != 0 {
		t.Errorf("plain 缓存不应产出 Lines: %+v", got.Lines)
	}
}

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

// TestLRCCacheSyncPlainNoCollision 同一 title-artist 的同步与纯文本
// 歌词分文件存储，互不覆盖。
func TestLRCCacheSyncPlainNoCollision(t *testing.T) {
	dir := t.TempDir()
	c, err := newLRCCache(dir)
	if err != nil {
		t.Fatalf("newLRCCache: %v", err)
	}
	c.Put("晴天", "周杰伦", &Lyrics{Lines: []LyricLine{{Time: 1, Text: "synced"}}})
	c.Put("晴天", "周杰伦", &Lyrics{Plain: "plain"})
	got, ok := c.Get("晴天", "周杰伦")
	if !ok || len(got.Lines) != 1 || got.Lines[0].Text != "synced" {
		t.Errorf("synced 被 plain 覆盖: %+v %v", got, ok)
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("文件数 = %d, want 2", len(entries))
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

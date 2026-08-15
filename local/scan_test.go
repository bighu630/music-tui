package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-tui/model"
)

// 构造临时目录树并返回其绝对路径。
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"a.mp3",
		"b.FLAC",         // 大写扩展名
		"note.txt",       // 非音频
		"cover.jpg",      // 非音频
		"sub/c.ogg",      // 子目录
		"sub/deep/d.m4a", // 嵌套子目录
		"sub2/e.wav",     // 另一子目录
		"z.opus",
	}
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestIsSupportedExt(t *testing.T) {
	if !IsSupportedExt("song.mp3") || !IsSupportedExt("song.FLAC") || !IsSupportedExt("song.OGG") {
		t.Error("支持的扩展名应大小写不敏感")
	}
	if IsSupportedExt("song.txt") || IsSupportedExt("song") || IsSupportedExt("") {
		t.Error("不支持的扩展名应返回 false")
	}
}

func TestSupportedExts(t *testing.T) {
	want := []string{".mp3", ".flac", ".m4a", ".wav", ".ogg", ".opus", ".aac"}
	if len(SupportedExts) != len(want) {
		t.Fatalf("SupportedExts = %v，期望 %v", SupportedExts, want)
	}
	for i, w := range want {
		if SupportedExts[i] != w {
			t.Errorf("SupportedExts[%d] = %q，期望 %q", i, SupportedExts[i], w)
		}
	}
}

func TestScanDir(t *testing.T) {
	root := buildTree(t)
	tracks, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan(%q) 意外错误: %v", root, err)
	}

	// 非音频文件被过滤：8 个文件中去掉 note.txt / cover.jpg 剩 6 个
	if len(tracks) != 6 {
		t.Fatalf("Scan(%q) 返回 %d 首，期望 6 首", root, len(tracks))
	}

	// 按完整路径字符串排序且稳定
	var got []string
	for _, tr := range tracks {
		if tr.Source != model.SourceLocal {
			t.Errorf("Track %q Source = %q，期望 %q", tr.ID, tr.Source, model.SourceLocal)
		}
		got = append(got, tr.ID)
	}
	want := []string{
		filepath.Join(root, "a.mp3"),
		filepath.Join(root, "b.FLAC"),
		filepath.Join(root, "sub", "c.ogg"),
		filepath.Join(root, "sub", "deep", "d.m4a"),
		filepath.Join(root, "sub2", "e.wav"),
		filepath.Join(root, "z.opus"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("扫描顺序 = %v，期望按完整路径排序 %v", got, want)
	}
}

func TestScanSingleFile(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "周杰伦 - 晴天.mp3")
	if err := os.WriteFile(mp3, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracks, err := Scan(mp3)
	if err != nil {
		t.Fatalf("Scan(%q) 意外错误: %v", mp3, err)
	}
	if len(tracks) != 1 {
		t.Fatalf("Scan(%q) 返回 %d 首，期望 1 首", mp3, len(tracks))
	}
	abs, _ := filepath.Abs(mp3)
	if tracks[0].ID != filepath.Clean(abs) {
		t.Errorf("ID = %q，期望 %q", tracks[0].ID, abs)
	}
	if tracks[0].Title != "晴天" || tracks[0].Artist != "周杰伦" {
		t.Errorf("Title/Artist = %q/%q，期望 晴天/周杰伦", tracks[0].Title, tracks[0].Artist)
	}
}

func TestScanUnsupportedFile(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(txt); err == nil || !strings.Contains(err.Error(), "不支持的音频格式") {
		t.Errorf("Scan(不支持的音频文件) 应报「不支持的音频格式」，实际: %v", err)
	}
}

func TestScanNoAudioInDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(dir); err == nil || !strings.Contains(err.Error(), "目录中没有找到支持的音频文件") {
		t.Errorf("Scan(无音频目录) 应报「目录中没有找到支持的音频文件」，实际: %v", err)
	}
}

func TestScanNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such")
	if _, err := Scan(missing); err == nil || !strings.Contains(err.Error(), "路径不存在") {
		t.Errorf("Scan(不存在路径) 应报「路径不存在」，实际: %v", err)
	}
}

// 目录中的符号链接（悬空链接、指向目录的 .mp3 链接）不得毁掉整个扫描：
// 非常规文件一律跳过，只有真正常规文件入库。
func TestScanSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 悬空符号链接：目标不存在
	if err := os.Symlink(filepath.Join(root, "missing.mp3"), filepath.Join(root, "dangling.mp3")); err != nil {
		t.Skipf("无法创建符号链接（平台限制）: %v", err)
	}
	// 指向目录的 .mp3 链接
	sub := filepath.Join(root, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, filepath.Join(root, "dirlink.mp3")); err != nil {
		t.Skipf("无法创建符号链接（平台限制）: %v", err)
	}

	tracks, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan(%q) 不应因符号链接失败: %v", root, err)
	}
	if len(tracks) != 1 {
		t.Fatalf("Scan(%q) 返回 %d 首，期望仅 1 首常规文件", root, len(tracks))
	}
	if tracks[0].ID != filepath.Join(root, "real.mp3") {
		t.Errorf("ID = %q，期望 %q", tracks[0].ID, filepath.Join(root, "real.mp3"))
	}
}

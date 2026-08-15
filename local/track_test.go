package local

import (
	"os"
	"path/filepath"
	"testing"

	"music-tui/model"
)

func TestFromPath(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "周杰伦 - 晴天.mp3")
	if err := os.WriteFile(mp3, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := FromPath(mp3)
	if err != nil {
		t.Fatalf("FromPath(%q) 意外错误: %v", mp3, err)
	}

	abs, err := filepath.Abs(mp3)
	if err != nil {
		t.Fatal(err)
	}
	abs = filepath.Clean(abs)

	// ID/URL 为规范化绝对路径且相等
	if tr.ID != abs {
		t.Errorf("ID = %q，期望 %q（绝对路径）", tr.ID, abs)
	}
	if tr.URL != abs {
		t.Errorf("URL = %q，期望 %q（绝对路径）", tr.URL, abs)
	}
	if tr.ID != tr.URL {
		t.Errorf("ID(%q) 与 URL(%q) 应相等", tr.ID, tr.URL)
	}

	// 来源标识
	if tr.Source != model.SourceLocal {
		t.Errorf("Source = %q，期望 %q", tr.Source, model.SourceLocal)
	}

	// 文件名解析
	if tr.Title != "晴天" {
		t.Errorf("Title = %q，期望 %q", tr.Title, "晴天")
	}
	if tr.Artist != "周杰伦" {
		t.Errorf("Artist = %q，期望 %q", tr.Artist, "周杰伦")
	}

	// 不读标签：Duration 恒为 0、无封面
	if tr.Duration != 0 {
		t.Errorf("Duration = %v，期望 0", tr.Duration)
	}
	if tr.CoverURL != "" {
		t.Errorf("CoverURL = %q，期望空串", tr.CoverURL)
	}
}

func TestFromPathErrors(t *testing.T) {
	dir := t.TempDir()

	// 目录输入报错
	if _, err := FromPath(dir); err == nil {
		t.Errorf("FromPath(%q) 传入目录应报错", dir)
	}

	// 不存在的路径报错
	missing := filepath.Join(dir, "no-such.mp3")
	if _, err := FromPath(missing); err == nil {
		t.Errorf("FromPath(%q) 不存在的路径应报错", missing)
	}
}

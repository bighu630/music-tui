package local

import (
	"math"
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

	// 无标签内容（"x" 既无标签也无有效时长）：回退文件名解析、Duration 0、无封面
	if tr.Duration != 0 {
		t.Errorf("Duration = %v，期望 0（无标签文件）", tr.Duration)
	}
	if tr.CoverURL != "" {
		t.Errorf("CoverURL = %q，期望空串", tr.CoverURL)
	}
}

// 标签优先：Title/Artist 标签值优先，缺失字段回退文件名解析；
// Duration 读取文件内时长（mp3 帧数 → 秒）。
func TestFromPathTagPriority(t *testing.T) {
	dir := t.TempDir()

	// 1) 完整标签：Title/Artist 全部用标签值（与文件名解析结果不同）
	tagged := filepath.Join(dir, "文件名歌手 - 文件名歌名.mp3")
	writeID3v2MP3(t, tagged, "标签歌名", "标签歌手", 500)
	tr, err := FromPath(tagged)
	if err != nil {
		t.Fatalf("FromPath(%q) 意外错误: %v", tagged, err)
	}
	if tr.Title != "标签歌名" {
		t.Errorf("Title = %q，期望标签值 %q", tr.Title, "标签歌名")
	}
	if tr.Artist != "标签歌手" {
		t.Errorf("Artist = %q，期望标签值 %q", tr.Artist, "标签歌手")
	}
	// 时长：500 帧 MPEG1 Layer3 44100Hz
	if want := 500.0 * 1152 / 44100; math.Abs(tr.Duration-want) > 0.01 {
		t.Errorf("Duration = %v，期望 ≈ %v（CBR 帧数）", tr.Duration, want)
	}

	// 2) 标签只有 Title（无 TPE1）：Artist 回退文件名解析
	onlyTitle := filepath.Join(dir, "周杰伦 - 晴天.mp3")
	writeID3v2MP3(t, onlyTitle, "七里香", "", 10)
	tr2, err := FromPath(onlyTitle)
	if err != nil {
		t.Fatalf("FromPath(%q) 意外错误: %v", onlyTitle, err)
	}
	if tr2.Title != "七里香" {
		t.Errorf("Title = %q，期望标签值 %q", tr2.Title, "七里香")
	}
	if tr2.Artist != "周杰伦" {
		t.Errorf("Artist = %q，期望回退文件名 %q", tr2.Artist, "周杰伦")
	}

	// 3) 无标签：Title/Artist 全部回退文件名解析，Duration 仍读帧数
	plain := filepath.Join(dir, "朴树 - 平凡之路.mp3")
	writeID3v2MP3(t, plain, "", "", 10)
	tr3, err := FromPath(plain)
	if err != nil {
		t.Fatalf("FromPath(%q) 意外错误: %v", plain, err)
	}
	if tr3.Title != "平凡之路" || tr3.Artist != "朴树" {
		t.Errorf("Title/Artist = %q/%q，期望回退文件名 平凡之路/朴树", tr3.Title, tr3.Artist)
	}
	if want := 10.0 * 1152 / 44100; math.Abs(tr3.Duration-want) > 0.01 {
		t.Errorf("Duration = %v，期望 ≈ %v", tr3.Duration, want)
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

package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// HTML 错误页（填充到 ≥MinAudioSize，非音频魔数）→ false：
// yt-dlp 被代理/中间页劫持时产物是 HTML，内容校验应拒绝。
func TestIsAudioFileRejectsHtml(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.webm")
	data := append([]byte("<!DOCTYPE html><html>err</html>"), make([]byte, 2048)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := isAudioFile(path)
	if err != nil {
		t.Fatalf("isAudioFile: %v", err)
	}
	if ok {
		t.Error("HTML 内容 = true, want false")
	}
}

// 合法 WebM（EBML 魔数 + 零填充 ≥ MinAudioSize）→ true。
func TestIsAudioFileAcceptsWebM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.webm")
	data := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 2044)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := isAudioFile(path)
	if err != nil {
		t.Fatalf("isAudioFile: %v", err)
	}
	if !ok {
		t.Error("合法 WebM = false, want true")
	}
}

// 0 字节 → false（空产物/占位残留）。
func TestIsAudioFileRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.webm")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := isAudioFile(path)
	if err != nil {
		t.Fatalf("isAudioFile: %v", err)
	}
	if ok {
		t.Error("0 字节 = true, want false")
	}
}

// 魔数合法但小于 MinAudioSize（下载截断）→ false。
func TestIsAudioFileRejectsTruncatedBelowMinSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.webm")
	if err := os.WriteFile(path, []byte{0x1A, 0x45, 0xDF, 0xA3}, 0o644); err != nil { // 4 字节合法魔数
		t.Fatal(err)
	}
	ok, err := isAudioFile(path)
	if err != nil {
		t.Fatalf("isAudioFile: %v", err)
	}
	if ok {
		t.Error("小于 MinAudioSize 的合法魔数 = true, want false")
	}
}

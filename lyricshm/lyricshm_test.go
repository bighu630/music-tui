package lyricshm

import (
	"os"
	"path/filepath"
	"testing"
)

// 注:enabled 的平台分支(runtime.GOOS != linux)无法单测模拟,靠代码审查
// + 交叉编译验证(见 Task 4)。目录缺失分支可测。

func readFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(got)
}

func TestWriteLineWritesTextWithNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics")
	w := New(path)
	w.WriteLine("第一句歌词")
	if got := readFile(t, path); got != "第一句歌词\n" {
		t.Errorf("内容 = %q,期望 %q", got, "第一句歌词\n")
	}
}

func TestWriteLineOverwritesPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics")
	w := New(path)
	w.WriteLine("旧行")
	w.WriteLine("新行")
	if got := readFile(t, path); got != "新行\n" {
		t.Errorf("内容 = %q,期望 %q", got, "新行\n")
	}
}

func TestWriteLineSkipsBlankKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics")
	w := New(path)
	w.WriteLine("keep")
	w.WriteLine("   ")
	w.WriteLine("")
	w.WriteLine("\t")
	if got := readFile(t, path); got != "keep\n" {
		t.Errorf("空白行不应覆盖,内容 = %q,期望 %q", got, "keep\n")
	}
}

func TestNewEmptyPathUsesDefault(t *testing.T) {
	w := New("")
	if w.path != DefaultPath {
		t.Errorf("path = %q,期望默认 %q", w.path, DefaultPath)
	}
}

func TestNewDisabledWhenDirMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no-such-dir", "lyrics")
	w := New(p)
	if w.enabled {
		t.Error("目录不存在时应禁用")
	}
	w.WriteLine("x") // 不应 panic / 不应创建文件
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("禁用 writer 不应创建文件,Stat err = %v", err)
	}
}

func TestDisabledWriterIsNoop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lyrics")
	w := &Writer{path: p, enabled: false}
	w.WriteLine("x")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("禁用 writer 不应创建文件,Stat err = %v", err)
	}
}

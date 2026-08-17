package lyricshm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 写入逻辑相关测试直接构造 enabled Writer，不经过 New 的平台分支
// （非 Linux 上 New 返回禁用 Writer，WriteLine 为 no-op），保证任意
// 平台都能验证写入/覆盖/空白跳过逻辑；平台禁用分支由
// TestNewDisabledOnNonLinux 在非 Linux 平台显式断言，Linux 上由
// TestNewDisabledWhenDirMissing 覆盖目录缺失分支。

// newEnabledWriter 构造一个绕过平台检查的启用 Writer（仅供测试）。
func newEnabledWriter(path string) *Writer {
	return NewForTest(path)
}

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
	w := newEnabledWriter(path)
	w.WriteLine("第一句歌词")
	if got := readFile(t, path); got != "第一句歌词\n" {
		t.Errorf("内容 = %q,期望 %q", got, "第一句歌词\n")
	}
}

func TestWriteLineOverwritesPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics")
	w := newEnabledWriter(path)
	w.WriteLine("旧行")
	w.WriteLine("新行")
	if got := readFile(t, path); got != "新行\n" {
		t.Errorf("内容 = %q,期望 %q", got, "新行\n")
	}
}

func TestWriteLineSkipsBlankKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics")
	w := newEnabledWriter(path)
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

// TestNewDisabledOnNonLinux 断言非 Linux 平台 New 返回禁用 Writer（歌词
// 文件写入为 Linux 专属特性，macOS/Windows 上必须静默降级）。
func TestNewDisabledOnNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux 平台为启用分支，改用目录缺失用例验证")
	}
	p := filepath.Join(t.TempDir(), "lyrics")
	w := New(p) // 目录存在但非 Linux → 应禁用
	if w.enabled {
		t.Errorf("非 Linux 平台(%s) New 应返回禁用 Writer", runtime.GOOS)
	}
	w.WriteLine("x") // no-op，不应创建文件
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("非 Linux 禁用 writer 不应创建文件,Stat err = %v", err)
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

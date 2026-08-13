package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	if version != "0.1.0" {
		t.Fatalf("version = %q, want %q", version, "0.1.0")
	}
}

func TestRequireToolFound(t *testing.T) {
	dir := t.TempDir()
	name := "mpv"
	if runtime.GOOS == "windows" {
		name = "mpv.exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := requireTool("mpv")
	if err != nil {
		t.Fatalf("requireTool: %v", err)
	}
	if got != bin {
		t.Errorf("path = %q, want %q", got, bin)
	}
}

func TestRequireToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := requireTool("mpv")
	if err == nil {
		t.Fatal("缺少 mpv 时应报错")
	}
	if !strings.Contains(err.Error(), "安装") {
		t.Errorf("错误信息应包含安装提示: %v", err)
	}
}

func TestInstallHint(t *testing.T) {
	hint := installHint("mpv")
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(hint, "brew") {
			t.Errorf("macOS 提示应包含 brew: %q", hint)
		}
	case "windows":
		if !strings.Contains(hint, "winget") {
			t.Errorf("Windows 提示应包含 winget: %q", hint)
		}
	default:
		if !strings.Contains(hint, "apt") {
			t.Errorf("Linux 提示应包含 apt: %q", hint)
		}
	}
}

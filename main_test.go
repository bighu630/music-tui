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

func TestLoadSessionMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store, err := loadSession(path)
	if err != nil {
		t.Fatalf("会话文件不存在时应返回空 store: %v", err)
	}
	if store.State() != nil {
		t.Error("无会话文件时 State 应为 nil")
	}
}

func TestLoadSessionCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := loadSession(path)
	if err != nil {
		t.Fatalf("损坏会话应降级重建而非报错: %v", err)
	}
	if store.State() != nil {
		t.Error("重建后应无会话状态")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "session.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("应生成 1 个损坏备份文件, got %d: %v", len(matches), matches)
	}
}

func TestLoadHistoryMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	store, err := loadHistory(path)
	if err != nil {
		t.Fatalf("历史文件不存在时应返回空 store: %v", err)
	}
	if store == nil {
		t.Fatal("store 不应为 nil")
	}
}

func TestLoadHistoryCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := loadHistory(path)
	if err != nil {
		t.Fatalf("损坏历史应降级重建而非报错: %v", err)
	}
	if store == nil {
		t.Fatal("store 不应为 nil")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "history.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("应生成 1 个损坏备份文件, got %d: %v", len(matches), matches)
	}
}

func TestLoadHistoryCorruptBackupFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 POSIX 目录权限语义")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限, 无法模拟备份失败")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if _, err := loadHistory(path); err == nil {
		t.Fatal("备份失败时应返回错误而非降级")
	}
}

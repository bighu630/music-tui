package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"music-tui/cache"
	"music-tui/config"
	"music-tui/lyrics"
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

// requireYtdlp 空配置 → 与 requireTool 一致走 PATH 查找：PATH 中放假
// yt-dlp 脚本即返回该路径。
func TestRequireYtdlpEmptyUsesPATH(t *testing.T) {
	dir := t.TempDir()
	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name = "yt-dlp.exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := requireYtdlp("")
	if err != nil {
		t.Fatalf("requireYtdlp(\"\"): %v", err)
	}
	if got != bin {
		t.Errorf("path = %q, want %q", got, bin)
	}
}

// requireYtdlp 自定义路径：配置的路径存在且可执行 → 返回该路径。
func TestRequireYtdlpCustomPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "custom-yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := requireYtdlp(bin)
	if err != nil {
		t.Fatalf("requireYtdlp: %v", err)
	}
	if got != bin {
		t.Errorf("path = %q, want %q", got, bin)
	}
}

// requireYtdlp 自定义路径不存在 → 报错含 "ytdlp.path" 与配置值；且即使 PATH
// 中存在真实 yt-dlp 也不回落（配置了路径就只检测该路径）。
func TestRequireYtdlpCustomPathMissingNoFallback(t *testing.T) {
	dir := t.TempDir()
	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name = "yt-dlp.exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	missing := filepath.Join(dir, "no-such-yt-dlp")
	_, err := requireYtdlp(missing)
	if err == nil {
		t.Fatal("配置路径不存在时应报错")
	}
	if !strings.Contains(err.Error(), "ytdlp.path") {
		t.Errorf("错误信息应包含 ytdlp.path: %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("错误信息应包含配置值: %v", err)
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

func TestLoadConfigMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("配置文件不存在时应生成默认配置: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg 不应为 nil")
	}
	if !cfg.Cache.Enabled {
		t.Error("默认配置应开启缓存")
	}
	if cfg.Cache.MaxEntries != config.DefaultMaxEntries {
		t.Errorf("MaxEntries = %d, want %d", cfg.Cache.MaxEntries, config.DefaultMaxEntries)
	}
	if cfg.Cache.Dir == "" {
		t.Error("默认缓存目录不应为空")
	}
}

func TestLoadConfigCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("损坏配置应降级重建而非报错: %v", err)
	}
	if cfg == nil || !cfg.Cache.Enabled {
		t.Error("重建后应为默认配置（缓存开启）")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "config.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("应生成 1 个损坏备份文件, got %d: %v", len(matches), matches)
	}
}

func TestLoadYTMMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ytm.json")
	store, err := loadYTM(path)
	if err != nil {
		t.Fatalf("ytm 文件不存在时应返回空 store: %v", err)
	}
	if store == nil {
		t.Fatal("store 不应为 nil")
	}
}

func TestLoadYTMCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ytm.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := loadYTM(path)
	if err != nil {
		t.Fatalf("损坏 ytm 配置应降级重建而非报错: %v", err)
	}
	if store == nil {
		t.Fatal("store 不应为 nil")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "ytm.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("应生成 1 个损坏备份文件, got %d: %v", len(matches), matches)
	}
}

// 缓存索引文件损坏：备份 .corrupt-* 后重试重建，返回启用态缓存。
func TestLoadCacheCorruptIndexBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	idx := filepath.Join(dir, "index.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idx, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	cm := loadCache(cache.Options{Enabled: true, MaxEntries: 100, Dir: dir}, "/nonexistent/yt-dlp", "", "", nil)
	if cm == nil {
		t.Fatal("loadCache 不应返回 nil")
	}
	if !cm.Enabled() {
		t.Error("索引备份重建后应为启用态缓存")
	}
	matches, err := filepath.Glob(idx + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("应生成 1 个索引损坏备份, got %d: %v", len(matches), matches)
	}
}

// 缓存目录不可创建（Dir 指向普通文件）：绝不阻止启动，降级为禁用态缓存。
func TestLoadCacheFailsGracefullyDisabled(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "notadir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cm := loadCache(cache.Options{Enabled: true, MaxEntries: 100, Dir: notDir}, "/nonexistent/yt-dlp", "", "", nil)
	if cm == nil {
		t.Fatal("loadCache 不应返回 nil")
	}
	if cm.Enabled() {
		t.Error("初始化失败应降级为禁用态缓存")
	}
	// 禁用态缓存全 no-op（不 panic）
	if _, ok := cm.Lookup("t1"); ok {
		t.Error("禁用态 Lookup 应恒 miss")
	}
	if err := cm.Remove("t1"); err != nil {
		t.Errorf("禁用态 Remove 应 no-op: %v", err)
	}
}

// TestDefaultModelConsistency config 与 lyrics 的默认模型常量必须一致
// （config 回落默认值与 OpenAI 客户端兜底值漂移即配置被静默改写）。
func TestDefaultModelConsistency(t *testing.T) {
	if config.DefaultOpenAIModel != lyrics.DefaultAIModel {
		t.Errorf("config.DefaultOpenAIModel=%q != lyrics.DefaultAIModel=%q",
			config.DefaultOpenAIModel, lyrics.DefaultAIModel)
	}
}

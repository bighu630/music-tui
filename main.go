// music-tui 是一个基于 YouTube 的终端音乐播放器。
// 依赖检测 → 启动 mpv → 组装服务 → 启动 TUI → 退出清理。
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"music-tui/cover"
	"music-tui/history"
	"music-tui/lyrics"
	"music-tui/mpris"
	"music-tui/player"
	"music-tui/playlists"
	"music-tui/search"
	"music-tui/session"
	"music-tui/ui"
)

// version 是应用版本号，展示于 User-Agent 与错误信息中。
const version = "0.1.0"

// userAgent 遵循 lrclib 要求的 "应用名 版本 (主页)" 格式。
const userAgent = "music-tui " + version + " (https://github.com/example/music-tui)"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "music-tui:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 运行时依赖检测（缺失即报错退出，附平台安装命令）
	mpvPath, err := requireTool("mpv")
	if err != nil {
		return err
	}
	ytdlpPath, err := requireTool("yt-dlp")
	if err != nil {
		return err
	}

	// 2. 数据目录准备
	cfgRoot, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("获取用户配置目录失败: %w", err)
	}
	hist, err := loadHistory(filepath.Join(cfgRoot, "music-tui", "history.json"))
	if err != nil {
		return fmt.Errorf("加载历史记录失败: %w", err)
	}
	sess, err := loadSession(filepath.Join(cfgRoot, "music-tui", "session.json"))
	if err != nil {
		return fmt.Errorf("加载会话失败: %w", err)
	}
	pls, err := loadPlaylists(filepath.Join(cfgRoot, "music-tui", "playlists.json"))
	if err != nil {
		return fmt.Errorf("加载播放列表失败: %w", err)
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("获取用户缓存目录失败: %w", err)
	}
	covers, err := cover.NewFetcher(filepath.Join(cacheRoot, "music-tui", "covers"))
	if err != nil {
		return fmt.Errorf("初始化封面缓存失败: %w", err)
	}

	// 3. 启动 mpv（defer 保证退出时清理进程与 socket）
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("music-tui-%d.sock", os.Getpid()))
	mpv := player.NewMpvPlayer(mpvPath, sockPath)
	if err := mpv.Start(); err != nil {
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}
	defer mpv.Close()

	// 3.5 MPRIS 服务（仅 Linux 有效；非 Linux 为 no-op 桩）。
	// 连接/注册失败仅警告，绝不影响播放器主功能。
	mprisSrv := mpris.NewServer(mpv)
	if err := mprisSrv.Start(); err != nil {
		log.Printf("MPRIS 服务不可用（不影响播放器）: %v", err)
	} else {
		defer mprisSrv.Close()
	}

	// 4. 组装服务并启动 TUI（退出后 run 返回，defer 清理 mpv）
	model := ui.NewModel(
		mpv,
		search.NewYouTubeAdapter(ytdlpPath),
		lyrics.NewClient(userAgent),
		covers,
		hist,
		sess,
		pls,
		mprisSrv.SetTrack,
	)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI 运行失败: %w", err)
	}
	return nil
}

// loadHistory 加载历史记录；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动。备份失败或重建失败则返回错误。
func loadHistory(path string) (*history.Store, error) {
	store, err := history.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：历史文件损坏，已备份至 %s 并重建\n", backup)
	store, retryErr := history.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// loadPlaylists 加载播放列表文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动（与 loadHistory 同款降级）。
func loadPlaylists(path string) (*playlists.Store, error) {
	store, err := playlists.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：播放列表文件损坏，已备份至 %s 并重建\n", backup)
	store, retryErr := playlists.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// loadSession 加载会话文件；文件损坏（崩溃/断电截断）时备份后重建，
// 避免缓存文件阻止应用启动（与 loadHistory 同款降级）。
func loadSession(path string) (*session.Store, error) {
	store, err := session.NewStore(path)
	if err == nil {
		return store, nil
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if berr := os.Rename(path, backup); berr != nil {
		return nil, err // 备份失败（如权限问题），按原样返回错误
	}
	fmt.Fprintf(os.Stderr, "music-tui: 警告：会话文件损坏，已备份至 %s 并重建\n", backup)
	store, retryErr := session.NewStore(path)
	if retryErr != nil {
		return nil, retryErr
	}
	return store, nil
}

// requireTool 在 PATH 中查找依赖；缺失时返回带平台安装命令的错误。
func requireTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("缺少依赖 %s，请先安装：\n%s", name, installHint(name))
	}
	return path, nil
}

// installHint 返回当前平台安装 mpv/yt-dlp 的命令提示。
func installHint(tool string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install " + tool
	case "windows":
		if tool == "mpv" {
			return "winget install mpv"
		}
		return "pip install yt-dlp"
	default: // linux 等
		return "sudo apt install " + tool + "（dnf: sudo dnf install " + tool + "；pacman: sudo pacman -S " + tool + "）"
	}
}

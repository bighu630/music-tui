package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// maxStderrTail 是错误分支拼入错误消息的 stderr 诊断文本最大长度（与 search 包同款）。
const maxStderrTail = 512

// tail 返回 s 末尾最多 max 字节；用于截取错误诊断文本。
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// findDestFiles 返回缓存目录内匹配 destBase 前缀（<basename>.）的文件列表，
// 按文件名排序（os.ReadDir 保证按文件名排序，输出确定性）；含 .part 临时文件，
// 调用方按需过滤。目录缺失/读取失败返回空。
//
// 不用 filepath.Glob：缓存目录路径含 glob 元字符（用户配置的 m.dir 如 "cache[x]"）
// 时 glob 会静默不匹配或报 ErrBadPattern，导致产物发现/清理失效（下载落盘却不
// 注册、残留不清理）。
func findDestFiles(destBase string) []string {
	dir := filepath.Dir(destBase)
	prefix := filepath.Base(destBase) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files
}

// realDownload 用 yt-dlp 直接把音频下载到 destBase+".%(ext)s" 模板落盘：
// exec `yt-dlp --no-playlist --no-warnings -f bestaudio -o <destBase>.%(ext)s <url>`。
// 成功返回最终文件 basename（register 用）；失败清理 destBase.* 残留（含 .part）后返回错误。
//
// 背景：YouTube 对音频直链做概率性 403 风控（同一 CDN/同一客户端，换 URL 换结果），
// 而每次运行 yt-dlp = 重新提取 = 新 URL。因此下载失败的重试交给上层整进程重跑
// （download 循环），每次重跑重新提取新 URL，天然绕开 403——单次尝试内不重试。
func realDownload(ctx context.Context, ytdlpPath, url, destBase string) (string, error) {
	cmd := exec.CommandContext(ctx, ytdlpPath,
		"--no-playlist", "--no-warnings", "-f", "bestaudio",
		"-o", destBase+".%(ext)s", url)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := tail(stderr.String(), maxStderrTail)
		if msg == "" {
			msg = "<无输出>"
		}
		cleanDestBase(destBase)
		return "", fmt.Errorf("yt-dlp 下载: %w（stderr: %s）", err, msg)
	}
	// yt-dlp 成功落盘 <destBase>.<ext>：前缀匹配排除 .part 临时文件取最终产物。
	// 理论唯一；多匹配 = 陈旧残留，按 ModTime 取最新，避免陈旧文件胜出。
	var final string
	var finalMod time.Time
	for _, m := range findDestFiles(destBase) {
		if strings.HasSuffix(m, ".part") {
			continue // yt-dlp 自己的临时文件，非最终产物
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.ModTime().After(finalMod) {
			final, finalMod = m, fi.ModTime()
		}
	}
	if final == "" {
		cleanDestBase(destBase)
		return "", fmt.Errorf("yt-dlp 未产出文件")
	}
	fi, err := os.Stat(final)
	if err != nil || fi.Size() == 0 {
		cleanDestBase(destBase)
		return "", fmt.Errorf("下载产物无效（缺失或 0 字节）")
	}
	return filepath.Base(final), nil
}

// cleanDestBase 删除 destBase.* 残留（含 .part），失败忽略（New 启动清理兜底）。
func cleanDestBase(destBase string) {
	for _, m := range findDestFiles(destBase) {
		os.Remove(m)
	}
}

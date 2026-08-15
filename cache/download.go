package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// yt-dlp 成功落盘 <destBase>.<ext>：glob 排除 .part 临时文件取最终产物（理论唯一）
	matches, err := filepath.Glob(destBase + ".*")
	if err != nil {
		return "", fmt.Errorf("glob 下载产物: %w", err)
	}
	var final string
	for _, m := range matches {
		if filepath.Ext(m) == ".part" {
			continue // yt-dlp 自己的临时文件，非最终产物
		}
		final = m
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
	if matches, err := filepath.Glob(destBase + ".*"); err == nil {
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// userAgent 是下载请求的 User-Agent（裸 Go 客户端默认无 UA，YouTube 直链易 403）。
const userAgent = "music-tui/0.1.0"

// maxStderrTail 是错误分支拼入错误消息的 stderr 诊断文本最大长度（与 search 包同款）。
const maxStderrTail = 512

// tail 返回 s 末尾最多 max 字节；用于截取错误诊断文本。
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// extractFunc 从页面 URL 提取音频直链与扩展名（测试可注入 stub）。
type extractFunc func(ctx context.Context, url string) (streamURL, ext string, err error)

// realExtract 用 yt-dlp 提取直链：--print 输出一行 "URL EXT"。
func realExtract(ctx context.Context, ytdlpPath, url string) (streamURL, ext string, err error) {
	cmd := exec.CommandContext(ctx, ytdlpPath,
		"--no-playlist", "--no-warnings", "-f", "bestaudio",
		"--print", "%(url)s %(ext)s", url)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := tail(stderr.String(), maxStderrTail)
		if msg == "" {
			msg = "<无输出>"
		}
		return "", "", fmt.Errorf("yt-dlp 提取直链: %w（stderr: %s）", err, msg)
	}
	line := string(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i] // 只取第一行
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("yt-dlp 输出为空")
	}
	streamURL = fields[0]
	if len(fields) >= 2 && safeExt(fields[1]) {
		ext = fields[1]
	}
	return streamURL, ext, nil
}

// safeExt 校验扩展名：仅 1-8 位字母数字。
func safeExt(s string) bool {
	if len(s) < 1 || len(s) > 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// downloadFile 下载 url 到 dest：先写 dest+".part" 再 rename，0 字节视为错误；
// 失败（含 5xx）等 DownloadRetryBackoff 后重试 1 次（总 2 次尝试）。
func downloadFile(ctx context.Context, client *http.Client, url, dest string) (int64, error) {
	n, err := attemptDownload(ctx, client, url, dest)
	if err == nil {
		return n, nil
	}
	select {
	case <-time.After(DownloadRetryBackoff):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return attemptDownload(ctx, client, url, dest)
}

// attemptDownload 单次下载尝试。
func attemptDownload(ctx context.Context, client *http.Client, url, dest string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("构造下载请求: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("发起下载: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	part := dest + ".part"
	f, err := os.Create(part) // Create 自带截断覆盖
	if err != nil {
		return 0, fmt.Errorf("创建临时文件: %w", err)
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(part)
		return 0, fmt.Errorf("写入临时文件: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(part)
		return 0, fmt.Errorf("关闭临时文件: %w", closeErr)
	}
	if n == 0 {
		os.Remove(part)
		return 0, fmt.Errorf("下载内容为空")
	}
	if err := os.Rename(part, dest); err != nil {
		return 0, fmt.Errorf("移动临时文件: %w", err)
	}
	return n, nil
}

package player

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ytdlpConfName 是临时配置文件模板（os.TempDir() 下，按 PID 区分实例）。
// 固定路径 + 覆盖写：startProcess 每次重新生成（重连场景幂等），Close 删除。
const ytdlpConfName = "music-tui-ytdlp-%d.conf"

// userYtdlpConfPaths 返回 yt-dlp 用户默认配置候选路径（存在即按序合并）：
// $XDG_CONFIG_HOME/yt-dlp/config、~/.config/yt-dlp/config、darwin 平台加
// ~/Library/Application Support/yt-dlp/config、/etc/yt-dlp.conf。
// 与 yt-dlp 官方配置搜索顺序对齐：用户默认配置（如 cookies 基础配置）必须保留，
// 临时配置只在其后追加 add-header 行。
func userYtdlpConfPaths() []string {
	var paths []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "yt-dlp", "config"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "yt-dlp", "config"))
		if runtime.GOOS == "darwin" {
			paths = append(paths, filepath.Join(home, "Library", "Application Support", "yt-dlp", "config"))
		}
	}
	paths = append(paths, "/etc/yt-dlp.conf")
	return paths
}

// quoteShlexArg 按 POSIX shlex 规则转义配置行参数：参数含空格/双引号/反斜杠/#
// 时用双引号包裹，并转义内部 \ 与 "（yt-dlp 配置解析用 shlex.split(posix=True)）。
func quoteShlexArg(arg string) string {
	if !strings.ContainsAny(arg, " \"\\#") {
		return arg
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(arg) + `"`
}

// buildYtdlpConf 生成 yt-dlp 临时配置文件（os.TempDir()/music-tui-ytdlp-<pid>.conf，
// 0600，覆盖写）：用户默认配置原文合并在前（存在即按序合并），按键排序的
// add-header 行在后——行格式 "add-header Name:Value"（冒号后无空格），值
// TrimSpace 后拼接、空值跳过、含空格/引号/反斜杠/# 时引号包裹转义。
// 返回文件路径；写入失败返回错误（调用方降级：跳过 config-location，
// 不影响 mpv 启动——取流功能降级不崩溃）。
func buildYtdlpConf(headers map[string]string) (string, error) {
	var sb strings.Builder
	for _, path := range userYtdlpConfPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 不存在/不可读：跳过
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			continue // 空配置文件无内容可合并
		}
		sb.WriteString(strings.TrimRight(string(data), "\n"))
		sb.WriteByte('\n')
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 按键排序：配置内容确定性（map 迭代无序）
	for _, k := range keys {
		v := strings.TrimSpace(headers[k])
		if v == "" {
			continue // 值空跳过
		}
		sb.WriteString("add-header " + quoteShlexArg(k+":"+v) + "\n")
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf(ytdlpConfName, os.Getpid()))
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return "", fmt.Errorf("写 yt-dlp 临时配置: %w", err)
	}
	return path, nil
}

// quoteMpvOptionValue 按 mpv 选项值语法包裹字符串：值含逗号或双引号时用
// 双引号包裹并转义内部双引号（如 --ytdl-raw-options=cookiefile="a,b"），
// 否则原样返回。
func quoteMpvOptionValue(v string) string {
	if !strings.ContainsAny(v, `,"`) {
		return v
	}
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}

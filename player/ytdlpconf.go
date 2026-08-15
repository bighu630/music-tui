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
// 注意：这些路径（含未在候选列表的 ~/.yt-dlp/config、config.txt 等）现代
// yt-dlp 原生仍会加载，本合并只是冗余兜底——重复加载无害（同键 add-header
// 后胜、靠后的临时配置行优先级正确），且保证用户默认配置（如 cookies 基础
// 配置）保留，临时配置只在其后追加 add-header 行。
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
// 或换行（\n\r）时用双引号包裹，并转义内部 \ 与 "（yt-dlp 配置解析用
// shlex.split(posix=True)；\n\r 防御：值含换行会拆散配置行结构，属无效
// HTTP header 场景）。
func quoteShlexArg(arg string) string {
	if !strings.ContainsAny(arg, " \"\\#\n\r") {
		return arg
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(arg) + `"`
}

// isConfigLocationsLine 判断配置行是否为 config-location(s) 选项（忽略前导
// 短横线与缩进，匹配行首 token）。这类行必须从合并副本中过滤：yt-dlp 把
// 配置内嵌套的 --config-locations 相对路径解析为相对"该配置文件所在目录"，
// 而合并副本位于 os.TempDir()——相对路径必然不存在 → parser.error 致命退出
// → mpv 取流全挂。
func isConfigLocationsLine(line string) bool {
	tok := strings.TrimLeft(strings.TrimSpace(line), "-")
	if i := strings.IndexAny(tok, " \t="); i >= 0 {
		tok = tok[:i]
	}
	return tok == "config-locations" || tok == "config-location"
}

// buildYtdlpConf 生成 yt-dlp 临时配置文件（os.TempDir()/music-tui-ytdlp-<pid>.conf，
// 0600，覆盖写）：用户默认配置原文合并在前（paths 为空时取默认
// userYtdlpConfPaths()，存在即按序合并，过滤 config-location(s) 行），按键
// 排序的 --add-header 行在后——行格式 "--add-header Name:Value"（冒号后无
// 空格），值 TrimSpace 后拼接、空值跳过、含空格/引号/反斜杠/#/换行时引号
// 包裹转义。paths 可注入：测试传固定列表，不依赖本机 /etc/yt-dlp.conf 等。
// 返回文件路径；写入失败返回错误（调用方降级：跳过 config-locations，
// 不影响 mpv 启动——取流功能降级不崩溃）。
func buildYtdlpConf(headers map[string]string, paths ...string) (string, error) {
	var sb strings.Builder
	if len(paths) == 0 {
		paths = userYtdlpConfPaths()
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 不存在/不可读：跳过
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			continue // 空配置文件无内容可合并
		}
		// 逐行过滤 config-location(s)：嵌套相对路径在 /tmp 合并副本下必然
		// 失效（见 isConfigLocationsLine），不过滤 → yt-dlp 致命退出。
		var kept []string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if isConfigLocationsLine(line) {
				continue
			}
			kept = append(kept, line)
		}
		sb.WriteString(strings.Join(kept, "\n"))
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
		sb.WriteString("--add-header " + quoteShlexArg(k+":"+v) + "\n")
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf(ytdlpConfName, os.Getpid()))
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return "", fmt.Errorf("写 yt-dlp 临时配置: %w", err)
	}
	return path, nil
}

// quoteMpvOptionValue 按 mpv 列表选项值语法包裹字符串：值含逗号时用双引号
// 包裹（如 --ytdl-raw-options=cookies="/tmp/a,b"），否则原样返回。mpv
// m_option.c 的 read_subparam 用 bstrcspn 找下一个字面 `"`、引号内不支持
// 反斜杠转义——mpv 列表值无法表示字面 `"`（含 `,`+`"` 的路径不可用），
// 故不做转义（转义会让引号区提前终止 → mpv 启动失败）。
func quoteMpvOptionValue(v string) string {
	if !strings.Contains(v, ",") {
		return v
	}
	return `"` + v + `"`
}

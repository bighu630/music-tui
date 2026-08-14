package cache

import "strings"

// maxSafeNameLen 是 SafeName 输出的最大字节数：超长 ID 截断，避免
// 文件名超过文件系统限制（ENAMETOOLONG）导致下载静默失败。
const maxSafeNameLen = 64

// SafeName 把任意 ID 安全化为磁盘文件名：保留 [A-Za-z0-9._-]，
// 其余字符（含 Unicode 非 ASCII）转为 '_'；超长截断到 64 字节；
// 结果为纯点串（""、"."、".."、截断后的长点串）时返回 "unknown"
// （".." 会逃逸缓存目录；纯点串在部分平台被剔除尾部点而失效）。
func SafeName(id string) string {
	out := make([]byte, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			r = '_'
		}
		out = append(out, byte(r)) // 非 ASCII rune 已在 default 分支转为 '_'（若直接截断 byte(r) 会漏网）
	}
	if len(out) > maxSafeNameLen {
		out = out[:maxSafeNameLen] // 截断可能在 '_' 序列中，截断后再判空/判纯点
	}
	if len(out) == 0 || strings.Trim(string(out), ".") == "" {
		return "unknown"
	}
	return string(out)
}

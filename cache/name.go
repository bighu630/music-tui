package cache

// SafeName 把任意 ID 安全化为磁盘文件名：保留 [A-Za-z0-9._-]，
// 其余字符（含 Unicode 非 ASCII）转为 '_'；结果为空时返回 "unknown"。
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
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}

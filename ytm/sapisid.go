package ytm

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// ytmOrigin 是 SAPISIDHASH 签名与 InnerTube 请求的固定 origin。
const ytmOrigin = "https://music.youtube.com"

// cookiePair 是 Cookie header 中的一个 "name=value" 项（保序）。
type cookiePair struct {
	Name  string
	Value string
}

// parseCookieHeader 把 "name=value; name2=value2; ..." 解析为保序的键值对；
// 无 "=" 的段、空名称跳过；名称与值均去首尾空格。
func parseCookieHeader(header string) []cookiePair {
	var out []cookiePair
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out = append(out, cookiePair{Name: name, Value: strings.TrimSpace(value)})
	}
	return out
}

// findCookiePair 按名称（大小写不敏感）查找 cookie 对。
func findCookiePair(pairs []cookiePair, name string) (string, bool) {
	for _, p := range pairs {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}
	return "", false
}

// extractSAPISID 从 Cookie header 提取 SAPISID 值：优先 __Secure-3PAPISID，
// 其次 SAPISID（ytmusicapi 用 3PAPISID、yutemal 用 SAPISID，两者均兼容）。
func extractSAPISID(header string) (string, error) {
	pairs := parseCookieHeader(header)
	if v, ok := findCookiePair(pairs, "__Secure-3PAPISID"); ok {
		return v, nil
	}
	if v, ok := findCookiePair(pairs, "SAPISID"); ok {
		return v, nil
	}
	return "", fmt.Errorf("Cookie 中未找到 SAPISID（__Secure-3PAPISID / SAPISID）")
}

// sapisidHash 生成 SAPISIDHASH 值：
// {unix_ts}_{hex(sha1(ts + " " + sapisid + " " + "https://music.youtube.com"))}
func sapisidHash(sapisid string, ts int64) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", ts, sapisid, ytmOrigin)))
	return strconv.FormatInt(ts, 10) + "_" + hex.EncodeToString(h[:])
}

// Package ytm 实现 YouTube Music 同步核心：cookie 登录配置、浏览器 cookie
// 导出解密（Chrome v10/v11、macOS）、InnerTube browse 客户端与同步编排。
package ytm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	// linuxKeyIterations 是 Linux Chrome v10 固定密钥的 PBKDF2 迭代次数。
	linuxKeyIterations = 1
	// macKeyIterations 是 macOS Chrome keychain 密钥的 PBKDF2 迭代次数。
	macKeyIterations = 1003
	// chromeSalt 是 Chrome cookie 密钥派生的固定 salt。
	chromeSalt = "saltysalt"
	// chromeKeyLen 是派生密钥长度（AES-128）。
	chromeKeyLen = 16
	// sha256PrefixLen 是 meta_version>=24 时密文明文前的 SHA256 前缀长度。
	sha256PrefixLen = 32
	// v10Prefix 是 Linux/macOS Chrome cookie 值的版本前缀。
	v10Prefix = "v10"
	// v11Prefix 是 Linux keyring 加密 cookie 的版本前缀。
	v11Prefix = "v11"
)

// pbkdf2SHA1 是 PBKDF2-HMAC-SHA1 的手写实现（Chrome 密钥派生专用，
// 迭代次数参数化：Linux=1、macOS=1003）。对照 RFC 6070 类向量单测。
func pbkdf2SHA1(password, salt []byte, iterations, keyLen int) []byte {
	if iterations <= 0 {
		return nil
	}
	hLen := sha1.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	mac := func(data []byte) []byte {
		h := hmac.New(sha1.New, password)
		h.Write(data)
		return h.Sum(nil)
	}
	u := make([]byte, hLen)
	var dk []byte
	for block := 1; block <= numBlocks; block++ {
		// U1 = PRF(password, salt || INT_32_BE(block))
		saltBlock := make([]byte, 0, len(salt)+4)
		saltBlock = append(saltBlock, salt...)
		saltBlock = append(saltBlock, byte(block>>24), byte(block>>16), byte(block>>8), byte(block))
		u = mac(saltBlock)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			u = mac(u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// deriveChromeKey 按 Chrome 规范派生 cookie 解密密钥：
// PBKDF2-HMAC-SHA1(password, salt="saltysalt", iterations, keylen=16)。
func deriveChromeKey(password []byte, iterations int) []byte {
	return pbkdf2SHA1(password, []byte(chromeSalt), iterations, chromeKeyLen)
}

// pkcs7Unpad 校验并移除 PKCS7 填充；填充非法返回错误。
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("密文长度不是块大小整数倍")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, errors.New("非法 PKCS7 填充")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("非法 PKCS7 填充")
		}
	}
	return data[:len(data)-padLen], nil
}

// aesCBCDecrypt 用 AES-128-CBC（IV=16 个空格字节）解密密文并去 PKCS7 填充。
func aesCBCDecrypt(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("密文长度非法")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES 解密器失败: %w", err)
	}
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return pkcs7Unpad(plain)
}

// decryptV10 解密一条 v10 Chrome cookie：
// 值 = "v10" + AES-128-CBC(key, IV=16 空格, PKCS7 填充明文)。
// metaVersion>=24 时明文前 32 字节为 SHA256 前缀，须剥离（参考
// yt_dlp.cookies._decrypt_aes_cbc_multi）。密钥错误/数据损坏返回错误。
func decryptV10(encryptedValue, key []byte, metaVersion int) (string, error) {
	if len(encryptedValue) < len(v10Prefix) || string(encryptedValue[:len(v10Prefix)]) != v10Prefix {
		return "", errors.New("不是 v10 格式的 cookie 值")
	}
	plain, err := aesCBCDecrypt(encryptedValue[len(v10Prefix):], key)
	if err != nil {
		return "", err
	}
	if metaVersion >= 24 {
		if len(plain) < sha256PrefixLen {
			return "", errors.New("解密结果短于 SHA256 前缀")
		}
		plain = plain[sha256PrefixLen:]
	}
	if !utf8.Valid(plain) {
		return "", errors.New("解密结果不是合法 UTF-8")
	}
	return string(plain), nil
}

// decryptV10Multi 依次用多个密钥尝试解密 v10 值，返回第一个通过
// 填充校验 + UTF-8 校验的结果（Chrome 空密码降级策略）。
func decryptV10Multi(encryptedValue []byte, keys [][]byte, metaVersion int) (string, error) {
	var lastErr error
	for _, key := range keys {
		if len(key) == 0 {
			continue
		}
		got, err := decryptV10(encryptedValue, key, metaVersion)
		if err == nil {
			return got, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("无可用密钥")
	}
	return "", lastErr
}

package ytm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// 已知向量（Python hashlib.pbkdf2_hmac 计算）：
//
//	pbkdf2_hmac('sha1', pw, b'saltysalt', iter, len)
var pbkdf2Vectors = []struct {
	password   string
	iterations int
	keyLen     int
	wantHex    string
}{
	{"peanuts", 1, 16, "fd621fe5a2b402539dfa147ca9272778"},    // Linux v10
	{"", 1, 16, "d0d0ec9c7d77d43ac54187fa4818d17f"},           // Linux empty key
	{"peanuts", 1003, 16, "d9a09d499b4e1b7461f28e67972c6dbd"}, // macOS
	{"", 1003, 16, "1cbee826d6938327ae9043f63bfc26d7"},
	{"mac-password-123", 1003, 16, "867678d96449d1f228bd761d69177b7a"},
	// 多块输出（keyLen > 20 触发第二块）与多迭代
	{"peanuts", 2, 32, "f9b0ba6d9215488382150631d10b83549b41dd6008a09559a4c818ca7a491c6e"},
}

func TestPBKDF2SHA1KnownVectors(t *testing.T) {
	for _, v := range pbkdf2Vectors {
		got := pbkdf2SHA1([]byte(v.password), []byte("saltysalt"), v.iterations, v.keyLen)
		if hex.EncodeToString(got) != v.wantHex {
			t.Errorf("pbkdf2SHA1(%q, iter=%d, len=%d) = %x, want %s",
				v.password, v.iterations, v.keyLen, got, v.wantHex)
		}
	}
}

func TestDeriveChromeKey(t *testing.T) {
	if got := deriveChromeKey([]byte("peanuts"), linuxKeyIterations); hex.EncodeToString(got) != "fd621fe5a2b402539dfa147ca9272778" {
		t.Errorf("Linux v10 key = %x", got)
	}
	if got := deriveChromeKey(nil, macKeyIterations); hex.EncodeToString(got) != "1cbee826d6938327ae9043f63bfc26d7" {
		t.Errorf("macOS empty key = %x", got)
	}
	if len(deriveChromeKey([]byte("peanuts"), linuxKeyIterations)) != 16 {
		t.Error("key 长度应为 16")
	}
}

// encryptV10ForTest 用 Go 标准库构造 v10 密文（PKCS7 填充 + AES-128-CBC，IV=16 空格）。
// meta24 时在明文前加 sha256 前缀（Chrome meta_version>=24 行为）。
func encryptV10ForTest(t *testing.T, key, value []byte, meta24 bool) []byte {
	t.Helper()
	if meta24 {
		sum := sha256.Sum256(value)
		value = append(sum[:], value...)
	}
	pad := aes.BlockSize - len(value)%aes.BlockSize
	padded := make([]byte, len(value)+pad)
	copy(padded, value)
	for i := len(value); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return append([]byte("v10"), ct...)
}

// 固定向量：peanuts key 加密 "hello world"（Python cryptography 生成），
// 防止实现与参考实现一起漂移。
func TestDecryptV10FixedVector(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	enc := "763130d3d37762103c1b281780f0c0bbb5d9e5" // "v10"+密文（"hello world"）
	raw, err := hex.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptV10(raw, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("decryptV10 = %q, want %q", got, "hello world")
	}
}

func TestDecryptV10Roundtrip(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	value := "my-cookie-value-123"
	enc := encryptV10ForTest(t, key, []byte(value), false)
	got, err := decryptV10(enc, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Errorf("解密结果 = %q, want %q", got, value)
	}
}

func TestDecryptV10StripsSHA256PrefixWhenMeta24(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	value := "my-secret-cookie-value"
	enc := encryptV10ForTest(t, key, []byte(value), true)

	// meta_version=24：剥离 32 字节前缀
	got, err := decryptV10(enc, key, 24)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Errorf("meta24 解密结果 = %q, want %q", got, value)
	}

	// meta_version=10（不剥离前缀）时，二进制 SHA256 前缀使整串不是合法 UTF-8，
	// 与 yt-dlp 行为一致（decode 失败 → 该 cookie 解密失败）。
	if _, err := decryptV10(enc, key, 10); err == nil {
		t.Error("meta10 处理带前缀密文应解密失败（前缀非 UTF-8）")
	}
}

func TestDecryptV10EmptyKeyFallback(t *testing.T) {
	peanutsKey := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	emptyKey := deriveChromeKey(nil, linuxKeyIterations)
	value := "empty-key-value"
	enc := encryptV10ForTest(t, emptyKey, []byte(value), false)

	// 单独用 peanuts key 失败
	if _, err := decryptV10(enc, peanutsKey, 10); err == nil {
		t.Error("peanuts key 不应解出 empty key 密文")
	}
	// Multi：先错后对
	got, err := decryptV10Multi(enc, [][]byte{peanutsKey, emptyKey}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Errorf("empty key fallback 结果 = %q, want %q", got, value)
	}
}

func TestDecryptV10BadDataNoPanic(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	cases := [][]byte{
		nil,
		[]byte("v10"),
		[]byte("v10abc"),                         // 密文长度非法
		[]byte("xxx" + string(make([]byte, 16))), // 未知版本前缀
		[]byte("v10" + string(make([]byte, 16))), // 全零密文（填充校验失败）
	}
	for _, c := range cases {
		if _, err := decryptV10(c, key, 10); err == nil {
			t.Errorf("坏数据 %x 应返回错误", c)
		}
	}
}

func TestDecryptV10PKCS7Validation(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	enc := encryptV10ForTest(t, key, []byte("padding-check"), false)
	// 篡改最后一个填充字节 → PKCS7 校验失败
	enc[len(enc)-1] ^= 0xff
	if _, err := decryptV10(enc, key, 10); err == nil {
		t.Error("篡改填充后应报错")
	}
}

func TestDecryptV10RejectsWrongKey(t *testing.T) {
	key := deriveChromeKey([]byte("peanuts"), linuxKeyIterations)
	enc := encryptV10ForTest(t, key, []byte("hello world"), false)
	wrong := deriveChromeKey([]byte("peanuts2"), linuxKeyIterations)
	if _, err := decryptV10(enc, wrong, 10); err == nil {
		t.Error("错误密钥应解密失败")
	}
}

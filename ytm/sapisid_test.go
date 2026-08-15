package ytm

import (
	"regexp"
	"strings"
	"testing"
)

func TestExtractSAPISIDPrefers3PAPISID(t *testing.T) {
	header := "SID=aaa; SAPISID=fallback-value; __Secure-3PAPISID=preferred-value; HSID=bbb"
	got, err := extractSAPISID(header)
	if err != nil {
		t.Fatal(err)
	}
	if got != "preferred-value" {
		t.Errorf("应优先 __Secure-3PAPISID, got %q", got)
	}
}

func TestExtractSAPISIDFallsBackToSAPISID(t *testing.T) {
	header := "SID=aaa; SAPISID=only-value; HSID=bbb"
	got, err := extractSAPISID(header)
	if err != nil {
		t.Fatal(err)
	}
	if got != "only-value" {
		t.Errorf("got %q, want %q", got, "only-value")
	}
}

func TestExtractSAPISIDCaseInsensitive(t *testing.T) {
	header := "__secure-3papisid=mixed-case-value; sid=zzz"
	got, err := extractSAPISID(header)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mixed-case-value" {
		t.Errorf("名称应大小写不敏感, got %q", got)
	}
}

func TestExtractSAPISIDMissing(t *testing.T) {
	for _, header := range []string{
		"",
		"SID=aaa; HSID=bbb",
		"SID=aaa;;",
		"  ;  ",
	} {
		if _, err := extractSAPISID(header); err == nil {
			t.Errorf("header %q 缺少 SAPISID 应报错", header)
		}
	}
}

func TestExtractSAPISIDValueWithEquals(t *testing.T) {
	header := "SAPISID=val=ue; SID=x"
	got, err := extractSAPISID(header)
	if err != nil {
		t.Fatal(err)
	}
	if got != "val=ue" {
		t.Errorf("值内含 = 应保留, got %q", got)
	}
}

func TestSAPISIDHashDeterministic(t *testing.T) {
	// 固定 ts 期望值（python hashlib.sha1(f"{ts} {sapisid} https://music.youtube.com")）
	got := sapisidHash("SAPISID_TEST_VALUE_123", 1500000000)
	want := "1500000000_b3cc2964344fac905571e0c5d70ae1eec2691d7d"
	if got != want {
		t.Errorf("sapisidHash = %q, want %q", got, want)
	}
}

func TestSAPISIDHashTimestampFormat(t *testing.T) {
	// 无符号十进制 unix 秒 + 40 位 hex
	re := regexp.MustCompile(`^\d+_[0-9a-f]{40}$`)
	if !re.MatchString(sapisidHash("v", 1234567)) {
		t.Errorf("格式不符: %q", sapisidHash("v", 1234567))
	}
}

func TestSAPISIDHashChangesWithOriginValue(t *testing.T) {
	h1 := sapisidHash("abc", 100)
	h2 := sapisidHash("abd", 100)
	if strings.HasSuffix(h1, strings.SplitN(h2, "_", 2)[1]) {
		t.Error("不同 sapisid 不应产生相同哈希")
	}
}

func TestParseCookieHeaderOrderPreserved(t *testing.T) {
	pairs := parseCookieHeader("SID=1;  HSID =2 ;SAPISID=3")
	if len(pairs) != 3 {
		t.Fatalf("len = %d, want 3", len(pairs))
	}
	if pairs[0].Name != "SID" || pairs[0].Value != "1" {
		t.Errorf("pairs[0] = %+v", pairs[0])
	}
	if pairs[1].Name != "HSID" || pairs[1].Value != "2" {
		t.Errorf("pairs[1] = %+v", pairs[1])
	}
	if pairs[2].Value != "3" {
		t.Errorf("pairs[2] = %+v", pairs[2])
	}
}

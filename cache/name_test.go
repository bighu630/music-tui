package cache

import (
	"strings"
	"testing"
)

func TestSafeNameKeepsYouTubeID(t *testing.T) {
	cases := []string{
		"dQw4w9WgXcQ",
		"abc-123_XYZ.45",
		"a_b-c.0",
	}
	for _, id := range cases {
		if got := SafeName(id); got != id {
			t.Errorf("SafeName(%q) = %q, want %q", id, got, id)
		}
	}
}

func TestSafeNameReplacesUnsafeChars(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a/b c", "a_b_c"},
		{"a:b*c?d", "a_b_c_d"},
		{"hello world!", "hello_world_"},
		{"a\tb", "a_b"},
	}
	for _, c := range cases {
		if got := SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSafeNameUnicodeAllUnderscores(t *testing.T) {
	// 中文 2 个 rune，各转一个 '_'
	if got := SafeName("中文"); got != "__" {
		t.Errorf("SafeName(中文) = %q, want %q", got, "__")
	}
}

func TestSafeNameEmptyReturnsUnknown(t *testing.T) {
	if got := SafeName(""); got != "unknown" {
		t.Errorf("SafeName(\"\") = %q, want %q", got, "unknown")
	}
}

func TestSafeNameDotAndDotDotUnknown(t *testing.T) {
	for _, id := range []string{".", ".."} {
		if got := SafeName(id); got != "unknown" {
			t.Errorf("SafeName(%q) = %q, want %q", id, got, "unknown")
		}
	}
}

func TestSafeNameLongTruncatesTo64(t *testing.T) {
	got := SafeName(strings.Repeat("a", 200))
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
	if got != strings.Repeat("a", 64) {
		t.Errorf("SafeName(long) = %q, want 64 个 a", got)
	}
}

func TestSafeNameLongDotSeqUnknown(t *testing.T) {
	// 超长纯点串截断后仍是纯点串（Windows 会因尾部点被剔除而失效），按非法处理
	if got := SafeName(".." + strings.Repeat(".", 100)); got != "unknown" {
		t.Errorf("SafeName(..+100点) = %q, want %q", got, "unknown")
	}
}

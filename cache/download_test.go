package cache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadFileSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello audio data")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "song.m4a")
	client := &http.Client{}
	n, err := downloadFile(context.Background(), client, srv.URL, dest)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if n != int64(len("hello audio data")) {
		t.Errorf("n = %d, want %d", n, len("hello audio data"))
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "hello audio data" {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part residue: %v", err)
	}
}

func TestDownloadFile404(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	DownloadRetryBackoff = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "song.m4a")
	if _, err := downloadFile(context.Background(), &http.Client{}, srv.URL, dest); err == nil {
		t.Fatal("downloadFile 404 = nil error, want error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest exists after 404: %v", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part exists after 404: %v", err)
	}
}

func TestDownloadFileRetrySucceeds(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	DownloadRetryBackoff = 0

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, "retried ok")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "song.m4a")
	n, err := downloadFile(context.Background(), &http.Client{}, srv.URL, dest)
	if err != nil {
		t.Fatalf("downloadFile after retry: %v", err)
	}
	if n != int64(len("retried ok")) {
		t.Errorf("n = %d, want %d", n, len("retried ok"))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "retried ok" {
		t.Errorf("content = %q, want retried ok", data)
	}
}

func TestDownloadFileRetryGivesUp(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	DownloadRetryBackoff = 0

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "song.m4a")
	if _, err := downloadFile(context.Background(), &http.Client{}, srv.URL, dest); err == nil {
		t.Fatal("downloadFile 500x2 = nil error, want error")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2 (retry once)", got)
	}
}

func TestDownloadFileEmptyBody(t *testing.T) {
	defer func(old time.Duration) { DownloadRetryBackoff = old }(DownloadRetryBackoff)
	DownloadRetryBackoff = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 空体
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "song.m4a")
	if _, err := downloadFile(context.Background(), &http.Client{}, srv.URL, dest); err == nil {
		t.Fatal("downloadFile empty body = nil error, want error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest exists after empty body: %v", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part exists after empty body: %v", err)
	}
}

func writeFakeYtDlp(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestRealExtractFakeScript(t *testing.T) {
	script := writeFakeYtDlp(t, `echo "http://stream.example/a.m4a m4a"`)
	streamURL, ext, err := realExtract(context.Background(), script, "https://youtube.com/watch?v=abc")
	if err != nil {
		t.Fatalf("realExtract: %v", err)
	}
	if streamURL != "http://stream.example/a.m4a" {
		t.Errorf("streamURL = %q", streamURL)
	}
	if ext != "m4a" {
		t.Errorf("ext = %q, want m4a", ext)
	}
}

func TestRealExtractSingleFieldNoExt(t *testing.T) {
	script := writeFakeYtDlp(t, `echo "http://stream.example/a"`)
	streamURL, ext, err := realExtract(context.Background(), script, "https://youtube.com/watch?v=abc")
	if err != nil {
		t.Fatalf("realExtract: %v", err)
	}
	if streamURL != "http://stream.example/a" {
		t.Errorf("streamURL = %q", streamURL)
	}
	if ext != "" {
		t.Errorf("ext = %q, want empty", ext)
	}
}

func TestRealExtractEmptyOutput(t *testing.T) {
	script := writeFakeYtDlp(t, `echo ""`)
	if _, _, err := realExtract(context.Background(), script, "https://youtube.com/watch?v=abc"); err == nil {
		t.Fatal("realExtract empty output = nil error, want error")
	}
}

func TestRealExtractNonZeroExit(t *testing.T) {
	script := writeFakeYtDlp(t, `echo "oops" >&2; exit 1`)
	if _, _, err := realExtract(context.Background(), script, "https://youtube.com/watch?v=abc"); err == nil {
		t.Fatal("realExtract exit 1 = nil error, want error")
	}
}

func TestSafeExt(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"m4a", true},
		{"M4A", true},
		{"webm", true},
		{"mp3", true},
		{"12345678", true},
		{"a1B2", true},
		{"", false},
		{"a/b", false},
		{"abcdefghi", false}, // 9 字符超限
		{"a-b", false},
		{"a b", false},
	}
	for _, c := range cases {
		if got := safeExt(c.in); got != c.want {
			t.Errorf("safeExt(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

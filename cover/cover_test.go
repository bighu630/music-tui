package cover

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-tui/model"
)

// pngBytes 生成一张 8x8 的有效 PNG（image.Decode 校验需要真实图片）。
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchMaxresSuccess(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/maxresdefault.jpg") {
			_, _ = w.Write(pngBytes(t))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	got, err := f.Fetch(context.Background(), tr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got == "" {
		t.Fatal("返回路径为空")
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("缓存文件缺失: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("请求数 = %d (%v)，maxres 成功时不应降级", len(paths), paths)
	}
}

func TestFetchDegradesThroughChain(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/maxresdefault.jpg"),
			strings.HasSuffix(r.URL.Path, "/sddefault.jpg"):
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write(pngBytes(t))
		}
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := "/vi/abc/maxresdefault.jpg,/vi/abc/sddefault.jpg,/vi/abc/hqdefault.jpg"
	if got := strings.Join(paths, ","); got != want {
		t.Errorf("降级顺序 = %s, want %s", got, want)
	}
}

func TestFetchAllFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err == nil {
		t.Error("降级链全失败应报错")
	}
}

func TestFetchCachesToDisk(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(pngBytes(t))
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1（第二次应命中磁盘缓存）", calls)
	}
}

func TestFetchRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/x/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err == nil {
		t.Error("非图片内容应视为下载失败")
	}
}

func TestFetchEmptyCoverURL(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(context.Background(), model.Track{ID: "abc"}); err == nil {
		t.Error("CoverURL 为空应报错")
	}
}

func TestThumbURLReplacesLastSegment(t *testing.T) {
	got, err := thumbURL("https://i.ytimg.com/vi/abc/maxresdefault.jpg", "hqdefault")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://i.ytimg.com/vi/abc/hqdefault.jpg" {
		t.Errorf("got %q", got)
	}
}

func TestThumbURLKeepsQuery(t *testing.T) {
	got, err := thumbURL("https://i.ytimg.com/vi/abc/maxresdefault.jpg?sqp=x&rs=y", "sddefault")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://i.ytimg.com/vi/abc/sddefault.jpg?sqp=x&rs=y"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewFetcherCreatesCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "covers")
	f, err := NewFetcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("缓存目录未创建: %v", err)
	}
	_ = f
}

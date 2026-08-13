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
	"sync"
	"testing"

	"music-tui/model"
)

// pngBytes 生成一张 8x8 的有效 PNG（image.Decode 校验需要真实图片）。
// 返回 error 而非内部 t.Fatal：可能被 httptest 的 handler goroutine 调用，
// FailNow 不允许在非测试 goroutine 中执行。
func pngBytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writePNG 向响应写入一张有效 PNG；生成失败时 t.Errorf 上报并返回。
func writePNG(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	data, err := pngBytes()
	if err != nil {
		t.Errorf("生成 PNG: %v", err)
		return
	}
	_, _ = w.Write(data)
}

func TestFetchMaxresSuccess(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/maxresdefault.jpg") {
			writePNG(t, w)
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
			writePNG(t, w)
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

func TestFetchServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err == nil {
		t.Error("500 应视为下载失败")
	}
}

func TestFetchCachesToDisk(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writePNG(t, w)
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

func TestFetchRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxCoverBytes+1))
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", CoverURL: server.URL + "/x/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err == nil {
		t.Error("超过大小上限应视为下载失败")
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

func TestFetchEmptyID(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(context.Background(), model.Track{CoverURL: "https://example.com/x.jpg"}); err == nil {
		t.Error("ID 为空应报错")
	}
}

func TestFetchCacheHitWithoutCoverURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePNG(t, w)
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", Source: "youtube", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}
	if _, err := f.Fetch(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	// 缓存按 ID+Source 键控，命中不依赖 CoverURL。
	tr.CoverURL = ""
	if _, err := f.Fetch(context.Background(), tr); err != nil {
		t.Fatalf("缓存命中应成功: %v", err)
	}
}

func TestFetchConcurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePNG(t, w)
	}))
	defer server.Close()

	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := model.Track{ID: "abc", Source: "youtube", CoverURL: server.URL + "/vi/abc/maxresdefault.jpg"}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.Fetch(context.Background(), tr)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	// 缓存文件必须存在且是完整有效图片（未被并发写坏）。
	dest := filepath.Join(f.dir, "youtube-abc.jpg")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取缓存: %v", err)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("缓存文件不是有效图片: %v", err)
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

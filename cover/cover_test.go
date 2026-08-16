package cover

import (
	"bytes"
	"context"
	"encoding/binary"
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

// ---- 本地内嵌封面（fixture 构造）----

func be32(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}

func syncsafe(n int) []byte {
	return []byte{
		byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F),
	}
}

// writeMP3WithAPIC 写一个带 ID3v2.3 APIC 帧（image/png）的最小 mp3 fixture。
// 与 local 包测试的 writeID3v2MP3WithAPIC 各自独立构造（cover 是独立包，
// 不借道 local 的未导出 helper）；dhowden/tag 只需文件以 "ID3" 开头即可
// 解析，无需真实音频帧。
func writeMP3WithAPIC(t *testing.T, path string, picData []byte) {
	t.Helper()
	content := []byte{0x00} // encoding：latin-1
	content = append(content, "image/png"...)
	content = append(content, 0x00) // MIME 结束
	content = append(content, 0x03) // picture type：front cover
	content = append(content, 0x00) // 空描述（latin-1 以 $00 结束）
	content = append(content, picData...)
	frame := append([]byte("APIC"), be32(uint32(len(content)))...)
	frame = append(frame, 0x00, 0x00) // frame flags
	frame = append(frame, content...)

	var b []byte
	b = append(b, "ID3"...)
	b = append(b, 0x03, 0x00, 0x00) // v2.3，revision 0，无 flags
	b = append(b, syncsafe(len(frame))...)
	b = append(b, frame...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFetchLocalCover 本地歌曲：从标签提取内嵌封面写入缓存（路径可读、内容
// 与内嵌一致、image.Decode 可解）；二次 Fetch 命中磁盘缓存（删除源文件后
// 仍成功且返回同一 dest，证明未重读源文件）。
func TestFetchLocalCover(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pic, err := pngBytes()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cover.mp3")
	writeMP3WithAPIC(t, p, pic)
	tr := model.Track{ID: p, URL: p, Source: model.SourceLocal}

	got, err := f.Fetch(context.Background(), tr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("读取缓存: %v", err)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("缓存文件不是有效图片: %v", err)
	}
	if !bytes.Equal(data, pic) {
		t.Errorf("缓存内容与内嵌封面不一致")
	}

	// 二次 Fetch：删除源文件后仍命中磁盘缓存（dest 由 ID 确定性计算，仅
	// 比较返回值无法区分命中与重提取；删源后若缓存逻辑被移除，二次 Fetch
	// 会因源文件缺失失败——此断言才能捕获回归）。
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	again, err := f.Fetch(context.Background(), tr)
	if err != nil {
		t.Fatalf("二次 Fetch（源文件已删除）: %v", err)
	}
	if again != got {
		t.Errorf("二次 Fetch 返回 %q，期望命中缓存 %q", again, got)
	}
}

// TestFetchLocalCoverNoPicture 本地文件无内嵌封面/读取失败 → 报错
// （调用方显示占位框，与 YouTube 无封面一致）。
func TestFetchLocalCoverNoPicture(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// 无 APIC 的 mp3（ID3v2.3 空标签 + padding 补齐）→ "内嵌封面" 错误
	noPic := filepath.Join(t.TempDir(), "nopic.mp3")
	var b []byte
	b = append(b, "ID3"...)
	b = append(b, 0x03, 0x00, 0x00)
	b = append(b, syncsafe(0)...)
	b = append(b, make([]byte, 32)...) // padding（tag.ReadFrom 需读足 11 字节探测格式）
	if err := os.WriteFile(noPic, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(context.Background(), model.Track{ID: noPic, URL: noPic, Source: model.SourceLocal}); err == nil || !strings.Contains(err.Error(), "内嵌封面") {
		t.Errorf("Fetch(无 APIC) err = %v，期望含 \"内嵌封面\"", err)
	}

	// 文件不存在 → "本地文件不存在" 错误（os.IsNotExist 特判，避免暴露底层
	// 打开文件错误）
	missing := filepath.Join(t.TempDir(), "no-such.mp3")
	if _, err := f.Fetch(context.Background(), model.Track{ID: missing, URL: missing, Source: model.SourceLocal}); err == nil || !strings.Contains(err.Error(), "本地文件不存在") {
		t.Errorf("Fetch(文件不存在) err = %v，期望含 \"本地文件不存在\"", err)
	}
}

// TestFetchLocalCoverOversized APIC 图片数据超过 maxCoverBytes（16 MiB）
// → 报错（防解压炸弹，与 download 的大小上限一致）。
func TestFetchLocalCoverOversized(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "big.mp3")
	writeMP3WithAPIC(t, p, bytes.Repeat([]byte{1}, 16<<20+1)) // maxCoverBytes+1
	tr := model.Track{ID: p, URL: p, Source: model.SourceLocal}
	if _, err := f.Fetch(context.Background(), tr); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Errorf("Fetch(超大封面) err = %v，期望含 \"超过\"", err)
	}
}

// TestFetchLocalCoverBadImage APIC 图片数据非图片 → 报错
// （image.Decode 校验不通过，与 download 的非图片语义一致）。
func TestFetchLocalCoverBadImage(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "bad.mp3")
	writeMP3WithAPIC(t, p, []byte("this is not an image"))
	tr := model.Track{ID: p, URL: p, Source: model.SourceLocal}
	if _, err := f.Fetch(context.Background(), tr); err == nil || !strings.Contains(err.Error(), "不是有效图片") {
		t.Errorf("Fetch(坏图) err = %v，期望含 \"不是有效图片\"", err)
	}
}

// TestFetchLocalCoverCacheName 本地 ID（绝对路径，含 /）转义为缓存文件名：
// dest 在缓存目录内（无子目录创建）、文件可读。
func TestFetchLocalCoverCacheName(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pic, err := pngBytes()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "sub", "dir", "song.mp3")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMP3WithAPIC(t, p, pic)
	tr := model.Track{ID: p, URL: p, Source: model.SourceLocal}

	got, err := f.Fetch(context.Background(), tr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if filepath.Dir(got) != f.dir {
		t.Errorf("dest 目录 = %q，期望缓存目录 %q（ID 含 / 必须转义）", filepath.Dir(got), f.dir)
	}
	// 缓存目录下不得创建子目录（路径分隔符已替换为 _）
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("缓存目录下创建了子目录 %q（应转义为文件名）", e.Name())
		}
	}
	if _, err := os.ReadFile(got); err != nil {
		t.Errorf("缓存文件不可读: %v", err)
	}
}

// TestCacheFileNameEscapesPath 路径分隔符/非法字符转义为 '_'，YouTube ID 不受影响。
func TestCacheFileNameEscapesPath(t *testing.T) {
	cases := []struct {
		source, id, want string
	}{
		{"youtube", "abc", "youtube-abc"},                         // YouTube ID（[A-Za-z0-9_-]）不受影响
		{"local", `/a/b/c.mp3`, "local-_a_b_c.mp3"},               // 路径分隔符 → _
		{"local", `C:\music\song.mp3`, "local-C__music_song.mp3"}, // Windows 分隔符/冒号 → _
		{"local", "a b?.mp3", "local-a_b_.mp3"},                   // 空格与非法字符 → _
		{"local", "中文.mp3", "local-__.mp3"},                       // Unicode 非 ASCII → _
	}
	for _, c := range cases {
		if got := cacheFileName(c.source, c.id); got != c.want {
			t.Errorf("cacheFileName(%q, %q) = %q, want %q", c.source, c.id, got, c.want)
		}
	}
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

package cover

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
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

// TestCachedPath 只查缓存不下载：缓存文件存在返回 (绝对路径, true) 且文件名
// 含正确来源前缀（local- / youtube-）；不存在返回 ("", false)；空 ID 返回
// false；本地源与 YouTube 源同名 ID 互不串（不同缓存文件名）；无缓存且无
// CoverURL 的曲目直接 false——不触发任何下载/联网（CachedPath 仅 os.Stat）。
func TestCachedPath(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 预置缓存文件（命名与 cacheFileName 一致）：本地（ID 为绝对路径，含空格
	// 与分隔符，须转义）与 YouTube（video id 无需转义）。
	localID := filepath.Join(t.TempDir(), "a b", "song.mp3")
	ytID := "dQw4w9WgXcQ"
	localDest := filepath.Join(f.dir, cacheFileName(model.SourceLocal, localID)+".jpg")
	ytDest := filepath.Join(f.dir, cacheFileName("youtube", ytID)+".jpg")
	if err := os.WriteFile(localDest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ytDest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// (a) 缓存存在 → (绝对路径, true)，且路径含正确来源前缀
	localTrack := model.Track{ID: localID, Source: model.SourceLocal, CoverURL: ""}
	p, ok := f.CachedPath(localTrack)
	if !ok || p != localDest {
		t.Errorf("本地缓存命中: got (%q, %v), want (%q, true)", p, ok, localDest)
	}
	if !strings.Contains(filepath.Base(p), "local-") {
		t.Errorf("本地缓存文件名应含 local- 前缀: %q", filepath.Base(p))
	}
	ytTrack := model.Track{ID: ytID, Source: "youtube", CoverURL: "http://x/y.jpg"}
	p, ok = f.CachedPath(ytTrack)
	if !ok || p != ytDest {
		t.Errorf("YouTube 缓存命中: got (%q, %v), want (%q, true)", p, ok, ytDest)
	}
	if !strings.HasPrefix(filepath.Base(p), "youtube-") {
		t.Errorf("YouTube 缓存文件名应含 youtube- 前缀: %q", filepath.Base(p))
	}

	// (b) 不存在 → ("", false)
	if p, ok := f.CachedPath(model.Track{ID: "no-such-id", Source: "youtube"}); ok || p != "" {
		t.Errorf("未命中应返回 (\"\", false): got (%q, %v)", p, ok)
	}

	// (c) 空 ID → false
	if _, ok := f.CachedPath(model.Track{ID: "", Source: model.SourceLocal}); ok {
		t.Error("空 ID 不应命中缓存")
	}

	// (d) 本地源与 YouTube 源同名 ID 互不串：只写 local- 文件，youtube 源不命中
	shared := "same-id"
	localShared := filepath.Join(f.dir, cacheFileName(model.SourceLocal, shared)+".jpg")
	if err := os.WriteFile(localShared, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.CachedPath(model.Track{ID: shared, Source: "youtube"}); ok {
		t.Error("同名 ID 的 YouTube 源不应命中 local- 缓存文件")
	}
	if _, ok := f.CachedPath(model.Track{ID: shared, Source: model.SourceLocal}); !ok {
		t.Error("同名 ID 的本地源应命中 local- 缓存文件")
	}

	// (e) 无缓存且无 CoverURL → false（CachedPath 不触发下载，无网络路径）
	if _, ok := f.CachedPath(model.Track{ID: "no-download", Source: "youtube", CoverURL: ""}); ok {
		t.Error("无缓存且无 CoverURL 应返回 false（不触发下载）")
	}

	// (f) 同名目录（非文件）→ false：os.Stat 命中但 IsDir 拒绝，不产生坏 artUrl
	dirDest := filepath.Join(f.dir, cacheFileName("youtube", "is-dir")+".jpg")
	if err := os.MkdirAll(dirDest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.CachedPath(model.Track{ID: "is-dir", Source: "youtube"}); ok {
		t.Error("同名目录不应命中缓存（非文件）")
	}

	// 包级 CachedPath 与 Fetcher 方法一致（dir 显式传入）
	p, ok = CachedPath(f.dir, localTrack)
	if !ok || p != localDest {
		t.Errorf("包级 CachedPath: got (%q, %v), want (%q, true)", p, ok, localDest)
	}
	if p, ok := CachedPath(f.dir, model.Track{ID: "", Source: "youtube"}); ok || p != "" {
		t.Errorf("包级 CachedPath 空 ID: got (%q, %v), want (\"\", false)", p, ok)
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
		{"youtube", "abc", "youtube-abc"},                         // YouTube ID（[A-Za-z0-9_-]，sanitize 恒等）不受影响
		{"local", `/a/b/c.mp3`, "local-_a_b_c.mp3-78a914f5c93a"}, // 本地路径：转义 + 哈希后缀（单射）
		{"local", `C:\music\song.mp3`, "local-C__music_song.mp3-1e2014c54dc1"},
		{"local", "a b?.mp3", "local-a_b_.mp3-eea52ce19caa"},
		{"local", "中文.mp3", "local-__.mp3-4959cceb308c"}, // Unicode 非 ASCII → _ + 哈希后缀
	}
	for _, c := range cases {
		if got := cacheFileName(c.source, c.id); got != c.want {
			t.Errorf("cacheFileName(%q, %q) = %q, want %q", c.source, c.id, got, c.want)
		}
	}
}

// TestCacheFileNameLongPathCapped 超长本地路径的 sanitize 段截断到上限，
// 总文件名仍远小于 OS 单文件名上限；截断不影响唯一性（哈希覆盖完整 ID），
// 两条仅尾部不同的超长路径仍映射到不同文件名。
func TestCacheFileNameLongPathCapped(t *testing.T) {
	id := "/home/very/long/path/" + strings.Repeat("sub/", 40) + "中文歌曲.mp3"
	got := cacheFileName(model.SourceLocal, id)
	if len(got) > 200 {
		t.Errorf("缓存文件名过长: %d 字节（%q）", len(got), got)
	}
	if !strings.Contains(got, "-") {
		t.Errorf("长路径应带哈希后缀: %q", got)
	}
	// 两条仅在超长尾部不同的路径必须不同名（哈希兜底唯一性）
	id2 := id + "x"
	if got2 := cacheFileName(model.SourceLocal, id2); got2 == got {
		t.Errorf("尾部不同的超长路径撞名:\n %q\n %q", got, got2)
	}
}

// TestCacheFileNameOverlongIdentityID 超长但全为安全字符的 ID（恒等分支本会
// 命中）也必须受限长约束：落哈希分支取截断 + 后缀，不产生超 OS 上限的文件名。
func TestCacheFileNameOverlongIdentityID(t *testing.T) {
	id := strings.Repeat("a", 200) // 全安全字符、恒等、但超长
	got := cacheFileName("youtube", id)
	if len(got) > 200 {
		t.Errorf("缓存文件名过长: %d 字节（%q）", len(got), got)
	}
	if !strings.HasSuffix(got, "-c2a908d98f5d") {
		t.Errorf("超长恒等 ID 应落哈希分支（截断+后缀）: %q", got)
	}
	// 同一前缀仅尾字符不同的超长恒等 ID 仍不同名
	id2 := id[:199] + "b"
	if got2 := cacheFileName("youtube", id2); got2 == got {
		t.Errorf("尾部不同的超长恒等 ID 撞名:\n %q\n %q", got, got2)
	}
}

// TestCacheFileNameDistinctForCollidingPaths 缓存命名必须单射：不同本地文件
// （中文路径/分隔符 vs 下划线等经过转义会撞名的一类）不得映射到同一缓存
// 文件名——否则两首歌共享一个封面缓存文件，先 fetch 的封面串显到另一首，
// 即用户报告的"封面错位"（稳定、部分歌曲受影响）。
func TestCacheFileNameDistinctForCollidingPaths(t *testing.T) {
	pairs := [][2]string{
		{ // 同目录同长相中文歌名（每 非ASCII rune → 一个 '_'，长度相同必撞）
			"/home/ivhu/Music/周杰伦/晴天.mp3",
			"/home/ivhu/Music/周杰伦/稻香.mp3",
		},
		{ // 分隔符 vs 字面下划线同形
			"/a/b.mp3",
			"/a_b.mp3",
		},
		{ // 空格 vs 下划线
			"/a b.mp3",
			"/a_b.mp3",
		},
		{ // 中文歌名 vs 中文歌手名同长相
			"/home/u/Music/克罗地亚狂想曲.mp3",
			"/home/u/Music/天空之城钢琴版.mp3",
		},
	}
	for _, p := range pairs {
		a, b := cacheFileName(model.SourceLocal, p[0]), cacheFileName(model.SourceLocal, p[1])
		if a == b {
			t.Errorf("不同文件撞同一缓存文件名:\n  %q → %s\n  %q → %s\n（封面将串显）", p[0], a, p[1], b)
		}
	}
}

// pngColor 生成一张 8x8 纯色 PNG（以不同颜色区分不同歌曲的内嵌封面）。
func pngColor(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFetchLocalCoverDistinctPerTrack 症状级端到端复现：同一目录下两张
// 中文歌名的 mp3（转义后长度相同会撞 key），内嵌不同封面。Fetch 两首必须
// 返回不同的缓存文件、且各自内容与自己的内嵌封面一致（不得把另一首的
// 封面当成自己的）。修复前两首共享同一缓存文件 → 后 fetch 的一首拿到
// 先 fetch 的那首的封面（用户报告的错位）。
func TestFetchLocalCoverDistinctPerTrack(t *testing.T) {
	f, err := NewFetcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	picA := pngColor(t, 255, 0, 0) // 红色封面
	picB := pngColor(t, 0, 0, 255) // 蓝色封面
	pA := filepath.Join(dir, "晴天.mp3")
	pB := filepath.Join(dir, "稻香.mp3")
	writeMP3WithAPIC(t, pA, picA)
	writeMP3WithAPIC(t, pB, picB)
	trA := model.Track{ID: pA, URL: pA, Source: model.SourceLocal}
	trB := model.Track{ID: pB, URL: pB, Source: model.SourceLocal}

	destA, err := f.Fetch(context.Background(), trA)
	if err != nil {
		t.Fatalf("Fetch(A): %v", err)
	}
	destB, err := f.Fetch(context.Background(), trB)
	if err != nil {
		t.Fatalf("Fetch(B): %v", err)
	}
	if destA == destB {
		t.Fatalf("两首不同歌曲命中同一缓存文件 %q——封面将串显", destA)
	}
	dataA, err := os.ReadFile(destA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(destB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataA, picA) {
		t.Errorf("歌曲 A 缓存内容应为其自己的内嵌封面（红），但内容不匹配")
	}
	if !bytes.Equal(dataB, picB) {
		t.Errorf("歌曲 B 缓存内容应为其自己的内嵌封面（蓝），但内容不匹配")
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

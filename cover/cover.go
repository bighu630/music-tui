// Package cover 负责封面图下载、404 降级与磁盘缓存。
package cover

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // 注册解码格式
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"music-tui/local"
	"music-tui/model"
)

// thumbnailSizes 是 YouTube 缩略图降级链：从大到小依次尝试。
var thumbnailSizes = []string{"maxresdefault", "sddefault", "hqdefault", "mqdefault"}

// maxCoverBytes 限制单张封面下载大小，防止解压炸弹。
const maxCoverBytes = 16 << 20 // 16 MiB

// Fetcher 下载并缓存封面。缓存文件位于 <cacheDir>/<Source>-<ID>.jpg。
type Fetcher struct {
	client *http.Client
	dir    string
}

// NewFetcher 创建 Fetcher 并确保缓存目录存在。
func NewFetcher(cacheDir string) (*Fetcher, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建封面缓存目录: %w", err)
	}
	return &Fetcher{
		client: &http.Client{Timeout: 15 * time.Second},
		dir:    cacheDir,
	}, nil
}

// CachedPath 只查缓存不下载：track 的缓存封面文件存在时返回其绝对路径与 true。
// 本地歌曲（SourceLocal，ID 为绝对路径）与 YouTube（ID 为 video id）共用同一
// 缓存命名（local-*.jpg / youtube-*.jpg），此处按 cacheFileName 计算路径后
// os.Stat 判断存在性。ID 为空返回 false。不触发任何网络请求或标签读取。
func CachedPath(dir string, track model.Track) (string, bool) {
	if track.ID == "" {
		return "", false
	}
	dest := filepath.Join(dir, cacheFileName(track.Source, track.ID)+".jpg")
	if info, err := os.Stat(dest); err == nil && !info.IsDir() {
		return dest, true
	}
	return "", false
}

// CachedPath 只查缓存不下载（同包级 CachedPath，dir 用 f.dir）。
func (f *Fetcher) CachedPath(track model.Track) (string, bool) {
	return CachedPath(f.dir, track)
}

// Fetch 返回封面本地路径：磁盘缓存命中则直接返回；
// 否则本地歌曲从文件标签提取内嵌封面（ID3v2 APIC / FLAC PICTURE / MP4 covr），
// 其余来源沿降级链（maxresdefault→sddefault→hqdefault→mqdefault）下载；
// 校验图片有效性后原子写入缓存。全部失败返回错误（调用方显示占位框）。
func (f *Fetcher) Fetch(ctx context.Context, track model.Track) (string, error) {
	if track.ID == "" {
		return "", errors.New("track ID 为空")
	}
	dest := filepath.Join(f.dir, cacheFileName(track.Source, track.ID)+".jpg")
	// 缓存按 ID+Source 键控，命中时无需 CoverURL、无需重新提取。
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	// 本地歌曲无 CoverURL：从文件标签提取内嵌封面。
	if track.Source == model.SourceLocal {
		return f.fetchLocalCover(ctx, track, dest)
	}
	if track.CoverURL == "" {
		return "", errors.New("封面 URL 为空")
	}
	var errs []error
	for _, size := range thumbnailSizes {
		u, err := thumbURL(track.CoverURL, size)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", size, err))
			continue
		}
		if err := f.download(ctx, u, dest); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", size, err))
			continue
		}
		return dest, nil
	}
	return "", errors.Join(errs...)
}

// thumbURL 将封面 URL 的最后一段路径替换为指定缩略图尺寸，保留 query 参数。
func thumbURL(base, size string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if strings.Trim(u.Path, "/") == "" {
		return "", errors.New("封面 URL 无路径")
	}
	segs := strings.Split(u.Path, "/")
	segs[len(segs)-1] = size + ".jpg"
	u.Path = strings.Join(segs, "/")
	return u.String(), nil
}

// maxCacheNameLen 是 cacheFileName 中 sanitize 段的最大字节数：超长地址路径
// 截断，避免与哈希后缀合计超操作系统单文件名上限（255 字节）。截断不影响
// 唯一性——哈希后缀覆盖完整 ID（见 cacheFileName）。
const maxCacheNameLen = 120

// cacheFileName 把 (source, id) 安全化为缓存文件名（不含扩展名）。
//
// 本地曲目的 ID 是文件绝对路径（含 /、\ 等分隔符、空格与非 ASCII 字符），
// 直接拼接会让 dest 变成子目录路径导致写入失败甚至逃逸缓存目录。此处按
// cache.SafeName 的思路保留 [A-Za-z0-9._-]、其余字符（含路径分隔符、Windows
// 非法字符、Unicode 非 ASCII）统一替换为 '_'；但 cover 不依赖 cache 包，独立
// 实现。
//
// 单射性（封面错位根因）：转义本身并非单射——非 ASCII rune 折叠为 '_'（
// 等长中文名如 a/晴天.mp3 与 a/稻香.mp3 撞名）、路径分隔符/空格与字面 '_'
// 同形（/a/b.mp3 与 /a_b.mp3 撞名）。若不加区分，两首不同歌曲会共享同一
// 缓存文件，先 fetch 的封面被另一首命中复用 → 封面串显/错位（稳定、仅
// 受影响路径撞 key 的部分歌曲）。故：
//   - ID 本身全为安全字符（sanitize 是恒等映射，如 YouTube video id）时，
//     不同 ID 天然映射到不同文件名，直接用原名（youtube 缓存零变化）；
//   - 否则（本地绝对路径必含 '/'）追加完整 ID 的 sha256 前 12 hex（48 bit）
//     后缀恢复单射性：不同 ID 必得不同文件名，哈希碰撞概率可忽略。哈希
//     覆盖完整 ID，sanitize 段被截断也不影响唯一性。
//
// 文件名长度受操作系统上限约束：sanitize 段截断到 maxCacheNameLen，加后缀
// 仍远小于 255。
func cacheFileName(source, id string) string {
	out := make([]byte, 0, len(source)+1+len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			r = '_'
		}
		out = append(out, byte(r)) // 非 ASCII rune 已在 default 分支转 '_'（直接截断 byte(r) 会漏网）
	}
	base := string(out)
	// 恒等且长度未超上限：直接原名（YouTube 等纯安全字符 ID）。超长恒等 ID
	// 也落入哈希分支（截断 + 哈希兜底唯一性与长度），避免 ENAMETOOLONG。
	if len(base) <= maxCacheNameLen && base == id {
		return source + "-" + base
	}
	if len(base) > maxCacheNameLen {
		base = base[:maxCacheNameLen]
	}
	sum := sha256.Sum256([]byte(id))
	return source + "-" + base + "-" + hex.EncodeToString(sum[:])[:12]
}

// fetchLocalCover 从本地音频文件标签提取内嵌封面（local.Picture），校验
// 图片有效性后原子写入缓存。无内嵌封面/提取失败返回错误（调用方显示占位
// 框，与 YouTube 无封面一致）。
func (f *Fetcher) fetchLocalCover(ctx context.Context, track model.Track, dest string) (string, error) {
	data, err := local.Picture(track.URL)
	if err != nil {
		if errors.Is(err, local.ErrNoPicture) {
			return "", errors.New("本地文件无内嵌封面")
		}
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("本地文件不存在")
		}
		return "", fmt.Errorf("读取封面失败: %v", err)
	}
	if len(data) >= maxCoverBytes {
		return "", fmt.Errorf("封面超过大小上限 %d 字节", maxCoverBytes)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("封面不是有效图片: %w", err)
	}
	if err := f.saveCache(data, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// download 下载并校验图片，随后 saveCache 原子写入 dest。
func (f *Fetcher) download(ctx context.Context, u, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes))
	if err != nil {
		return err
	}
	if len(data) >= maxCoverBytes {
		return fmt.Errorf("封面 %s 超过大小上限 %d 字节", u, maxCoverBytes)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("封面不是有效图片: %w", err)
	}
	return f.saveCache(data, dest)
}

// saveCache 把封面数据原子写入 dest：先写唯一临时文件再重命名。
// 并发安全：每次调用使用独立的 CreateTemp 临时文件（并发下不会互相覆盖或
// ENOENT）；Rename 失败但 dest 已存在时视为成功（并发下对方已完成写入）。
// 由 download 与 fetchLocalCover 共用。
func (f *Fetcher) saveCache(data []byte, dest string) error {
	tmp, err := os.CreateTemp(f.dir, "*.tmp")
	if err != nil {
		return err
	}
	// 失败时清理临时文件；成功 rename 后原路径已不存在，Remove 的 ENOENT 忽略。
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil // 并发下对方已写入 dest
		}
		return err
	}
	return nil
}

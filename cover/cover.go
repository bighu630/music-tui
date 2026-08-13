// Package cover 负责封面图下载、404 降级与磁盘缓存。
package cover

import (
	"bytes"
	"context"
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

// Fetch 返回封面本地路径：磁盘缓存命中则直接返回；
// 否则沿降级链（maxresdefault→sddefault→hqdefault→mqdefault）下载，
// 校验图片有效性后原子写入缓存。全部失败返回聚合错误（调用方显示占位框）。
func (f *Fetcher) Fetch(ctx context.Context, track model.Track) (string, error) {
	if track.ID == "" {
		return "", errors.New("track ID 为空")
	}
	dest := filepath.Join(f.dir, track.Source+"-"+track.ID+".jpg")
	// 缓存按 ID+Source 键控，命中时无需 CoverURL。
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
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

// download 下载并校验图片，写入唯一临时文件后原子重命名到 dest。
// 并发安全：每次调用使用独立的 CreateTemp 临时文件（并发下不会互相覆盖或
// ENOENT）；Rename 失败但 dest 已存在时视为成功（并发下对方已完成写入）。
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

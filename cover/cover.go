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

// Fetcher 下载并缓存封面。缓存文件位于 <cacheDir>/<videoID>.jpg。
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
// 校验图片有效性后原子写入缓存。全部失败返回错误（调用方显示占位框）。
func (f *Fetcher) Fetch(ctx context.Context, track model.Track) (string, error) {
	if track.CoverURL == "" {
		return "", errors.New("封面 URL 为空")
	}
	dest := filepath.Join(f.dir, track.ID+".jpg")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	for _, size := range thumbnailSizes {
		u, err := thumbURL(track.CoverURL, size)
		if err != nil {
			continue
		}
		if err := f.download(ctx, u, dest); err == nil {
			return dest, nil
		}
	}
	return "", errors.New("封面降级链全部失败")
}

// thumbURL 将封面 URL 的最后一段路径替换为指定缩略图尺寸，保留 query 参数。
func thumbURL(base, size string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	segs := strings.Split(u.Path, "/")
	if len(segs) == 0 {
		return "", errors.New("封面 URL 无路径")
	}
	segs[len(segs)-1] = size + ".jpg"
	u.Path = strings.Join(segs, "/")
	return u.String(), nil
}

// download 下载并校验图片，写入临时文件后原子重命名到 dest。
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("封面不是有效图片: %w", err)
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

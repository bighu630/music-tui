package local

import (
	"fmt"
	"os"
	"path/filepath"

	"music-tui/model"
)

// FromPath 将本地文件路径映射为 model.Track。
//
// 规范化绝对路径（filepath.Abs + filepath.Clean）同时作为 ID 与 URL
// （可直接交给 mpv 播放）；Title/Artist 由文件名解析；不读取音频标签，
// 因此 Duration 恒为 0、CoverURL 为空。path 为目录或不存在时返回错误。
func FromPath(path string) (model.Track, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return model.Track{}, err
	}
	abs = filepath.Clean(abs)

	info, err := os.Stat(abs)
	if err != nil {
		return model.Track{}, fmt.Errorf("无法访问文件: %s: %w", abs, err)
	}
	if info.IsDir() {
		return model.Track{}, fmt.Errorf("%s 是目录，不是音频文件", abs)
	}

	title, artist := ParseFilename(filepath.Base(abs))
	return model.Track{
		ID:       abs, // 来源内唯一 ID：绝对路径
		Title:    title,
		Artist:   artist,
		Duration: 0, // 不读标签
		URL:      abs,
		Source:   model.SourceLocal,
		CoverURL: "",
	}, nil
}

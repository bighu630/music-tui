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
// （可直接交给 mpv 播放）；Title/Artist 标签优先（ID3v2/ID3v1/MP4/FLAC/OGG
// 等，字段缺失时回退文件名解析）；Duration 读取音频文件内时长
// （mp3/flac/m4a/wav，解析失败为 0）；CoverURL 为空。path 为目录或
// 不存在时返回错误。
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
	// 标签优先：非空字段覆盖文件名解析结果；缺失字段（空串）回退文件名
	if t, a, ok := readTags(abs); ok {
		if t != "" {
			title = t
		}
		if a != "" {
			artist = a
		}
	}
	// 时长：解析失败为 0（不阻断扫描）
	duration := readDuration(abs)
	if duration < 0 {
		duration = 0
	}

	return model.Track{
		ID:       abs, // 来源内唯一 ID：绝对路径
		Title:    title,
		Artist:   artist,
		Duration: duration,
		URL:      abs,
		Source:   model.SourceLocal,
		CoverURL: "",
	}, nil
}

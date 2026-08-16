package local

// 内嵌封面提取：dhowden/tag 的 Picture() 覆盖 ID3v2 APIC（mp3）、
// FLAC PICTURE（flac）、MP4 covr（m4a/mp4）等格式。

import (
	"errors"
	"os"

	"github.com/dhowden/tag"
)

// ErrNoPicture 表示文件没有内嵌封面（Picture 返回）。
var ErrNoPicture = errors.New("文件无内嵌封面")

// Picture 读取音频文件的内嵌封面（ID3v2 APIC / FLAC PICTURE / MP4 covr），
// 返回图片原始字节（JPEG/PNG 等，格式由标签内 MIME 决定）。
//
// 无内嵌封面返回 ErrNoPicture；文件打开/标签解析失败返回对应错误。
// 刻意与 readTags 各自独立打开文件（不共享解析）：readTags 服务于扫描时
// 的 Title/Artist 回退（失败静默），Picture 服务于封面提取（调用方需要
// 区分"无封面"与"读取失败"）——两函数调用时机与失败语义不同，合并只会
// 互相拖累。
func Picture(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil, err
	}
	pic := m.Picture()
	if pic == nil {
		return nil, ErrNoPicture
	}
	return pic.Data, nil
}

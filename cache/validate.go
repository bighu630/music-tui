package cache

import (
	"io"
	"os"
)

// MinAudioSize 是判定缓存文件为有效音频内容的最小字节数：小于该值的文件
// 视为截断/占位残留（0 字节、下载半截），即使魔数正确也拒绝入库/命中。
// 包级可调变量（测试可临时调小）。
var MinAudioSize = 1024

// isAudioFile 校验 path 内容是否为可识别的音频容器：读头部 32 字节，
// 要求 size >= MinAudioSize 且魔数命中任一已知音频格式（EBML/WebM、Ogg、
// FLAC、MP3、MP4/M4A、WAV、AIFF、AAC ADTS）。HTML 错误页（yt-dlp 被
// 代理/中间页劫持的产物）、截断文件等返回 false；文件缺失/读取失败返回错误。
func isAudioFile(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.Size() < int64(MinAudioSize) {
		return false, nil // 过小：截断/占位内容，非完整音频
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	hdr := make([]byte, 32)
	n, err := io.ReadFull(f, hdr)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	return hasAudioMagic(hdr[:n]), nil
}

// hasAudioMagic 判断头部字节是否命中已知音频容器魔数。len(h) 通常为 32；
// 长度守卫是防御性的（MinAudioSize 被测试调小时短头部也安全）。
func hasAudioMagic(h []byte) bool {
	if len(h) < 4 {
		return false
	}
	switch {
	case h[0] == 0x1A && h[1] == 0x45 && h[2] == 0xDF && h[3] == 0xA3: // EBML/WebM
		return true
	case string(h[:4]) == "OggS": // Ogg（Vorbis/Opus）
		return true
	case string(h[:4]) == "fLaC": // FLAC
		return true
	case string(h[:3]) == "ID3": // MP3（ID3 标签头）
		return true
	case h[0] == 0xFF && h[1]&0xE0 == 0xE0: // MP3 帧同步
		return true
	case len(h) >= 8 && string(h[4:8]) == "ftyp": // MP4/M4A（ISO BMFF）
		return true
	case len(h) >= 12 && string(h[:4]) == "RIFF" && string(h[8:12]) == "WAVE": // WAV
		return true
	case len(h) >= 12 && string(h[:4]) == "FORM" && string(h[8:12]) == "AIFF": // AIFF
		return true
	case h[0] == 0xFF && h[1]&0xF6 == 0xF0: // AAC ADTS
		return true
	}
	return false
}

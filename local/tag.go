package local

// 音频标签读取：Title/Artist 交给 dhowden/tag（ID3v2/ID3v1/MP4/FLAC/OGG 等），
// 时长自写解析器（dhowden/tag 无 Duration 接口）：
//   - mp3：跳过 ID3v2 后解析首帧头，Xing/Info 快路径 → CBR 判断 → VBR 逐帧扫描兜底；
//   - flac：STREAMINFO（sample rate + total samples）；
//   - m4a/mp4：moov/mvhd（timescale + duration）；
//   - wav：fmt 的 byteRate + data chunk 大小；
//   - ogg/opus/aac 等：无标准可解析时长 → 0。
//
// 任何解析失败一律返回 0（静默，绝不阻断扫描）。

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
)

// readTags 读取音频文件的 Title/Artist 标签。
//
// 读取失败（不含标签、格式无法识别、文件不存在等）或 Title/Artist 均为空
// 时返回 ok=false，调用方回退到文件名解析。返回的字段已 TrimSpace；
// 某个字段为空串表示该字段缺失（调用方只回退缺失字段）。
func readTags(path string) (title, artist string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return "", "", false
	}
	title = strings.TrimSpace(m.Title())
	artist = strings.TrimSpace(m.Artist())
	if title == "" && artist == "" {
		return "", "", false
	}
	return title, artist, true
}

// readDuration 读取音频文件时长（秒）。不支持或解析失败返回 0。
func readDuration(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return mp3Duration(f, size)
	case ".flac":
		return flacDuration(f, size)
	case ".m4a", ".mp4":
		return m4aDuration(f, size)
	case ".wav":
		return wavDuration(f, size)
	}
	return 0 // ogg/opus/aac 及其他：无标准可解析时长
}

func readUint32BE(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func readUint64BE(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

// id3v2TagSize 解析 ID3v2 头部的 4 字节 syncsafe 大小（不含 10 字节头本身）。
func id3v2TagSize(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

// parseMPEGHeader 解析 MPEG 音频帧头（4 字节）。
//
// 返回帧长（字节）、每帧样本数、比特率（bps）、采样率（Hz）；
// 同步字/版本/层/bitrate index/samplerate index 任一无效（含保留值、
// free format）→ ok=false。
func parseMPEGHeader(b []byte) (frameLen, samples, bitrate, samplerate int, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return 0, 0, 0, 0, false
	}
	version := (b[1] >> 3) & 0x03 // 0=MPEG2.5 2=MPEG2 3=MPEG1（1=保留）
	layer := (b[1] >> 1) & 0x03   // 1=Layer3 2=Layer2 3=Layer1（0=保留）
	if version == 1 || layer == 0 {
		return 0, 0, 0, 0, false
	}
	bi := int(b[2] >> 4)      // bitrate index（0=free、15=无效）
	si := int(b[2]>>2) & 0x03 // samplerate index（3=保留）
	if bi == 0 || bi == 15 || si == 3 {
		return 0, 0, 0, 0, false
	}
	padding := int(b[2]>>1) & 0x01

	// 比特率表（kbps，index 0=free、15=无效，与上述校验对应）
	var bitrates [16]int
	switch layer {
	case 3: // Layer1
		bitrates = [...]int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}
	case 2: // Layer2
		bitrates = [...]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0}
	default: // Layer3
		bitrates = [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	}
	bitrate = bitrates[bi] * 1000

	// 采样率表（Hz）
	var srs [3]int
	switch version {
	case 3: // MPEG1
		srs = [...]int{44100, 48000, 32000}
	case 2: // MPEG2
		srs = [...]int{22050, 24000, 16000}
	default: // MPEG2.5
		srs = [...]int{11025, 12000, 8000}
	}
	samplerate = srs[si]

	switch layer {
	case 1: // Layer3：MPEG1=1152，MPEG2/2.5=576
		samples = 1152
		if version != 3 {
			samples = 576
		}
	case 2: // Layer2：一律 1152
		samples = 1152
	default: // Layer1：384
		samples = 384
	}

	switch layer {
	case 3: // Layer1：帧长 = (12*bitrate/samplerate + padding) * 4
		frameLen = (12*bitrate/samplerate + padding) * 4
	default: // Layer2/3：帧长 = 144*bitrate/samplerate + padding
		frameLen = 144*bitrate/samplerate + padding
	}
	return frameLen, samples, bitrate, samplerate, frameLen >= 4
}

// mp3Duration 解析 MP3 时长：跳过 ID3v2 → 首帧头 → Xing/Info 快路径 →
// CBR 判断 → VBR 逐帧扫描兜底。
func mp3Duration(f *os.File, size int64) float64 {
	// 跳过 ID3v2 标签（10 字节头 + syncsafe size；flags 0x10 表示还有 10 字节 footer）
	audioStart := int64(0)
	var hdr [10]byte
	if size >= 10 {
		if _, err := f.ReadAt(hdr[:], 0); err == nil && string(hdr[:3]) == "ID3" {
			audioStart = 10 + int64(id3v2TagSize(hdr[6:10]))
			if hdr[5]&0x10 != 0 {
				audioStart += 10
			}
		}
	}
	if audioStart < 0 || audioStart+4 > size {
		return 0
	}

	// 首帧头
	var fh [4]byte
	if _, err := f.ReadAt(fh[:], audioStart); err != nil {
		return 0
	}
	frameLen, samples, bitrate, samplerate, ok := parseMPEGHeader(fh[:])
	if !ok {
		return 0 // 无有效帧头
	}

	// 快路径：首帧 payload 内的 Xing/Info 头（含帧数）→ 直接算时长。
	// Xing 位于 side info 之后：payload 内偏移 = side info 大小
	// （MPEG1 立体声 32 / 单声道 17；MPEG2/2.5 立体声 17 / 单声道 9）。
	version := (fh[1] >> 3) & 0x03
	sideInfo := 32
	if version != 3 {
		sideInfo = 17
	}
	if fh[3]>>6 == 3 { // 单声道
		sideInfo = 17
		if version != 3 {
			sideInfo = 9
		}
	}
	if payloadLen := frameLen - 4; payloadLen >= sideInfo+12 {
		payload := make([]byte, payloadLen)
		if _, err := f.ReadAt(payload, audioStart+4); err == nil {
			magic := string(payload[sideInfo : sideInfo+4])
			if magic == "Xing" || magic == "Info" {
				if n := readUint32BE(payload[sideInfo+8 : sideInfo+12]); n > 0 {
					return float64(n) * float64(samples) / float64(samplerate)
				}
			}
		}
	}

	// CBR 判断：文件剩余字节对帧长取模容差 < 64 → 视为 CBR，帧数 = 剩余/帧长
	// （容差容忍 ID3v1 之类的小尾巴）。取模判断前抽查中部两帧帧头
	// （比特率/采样率/版本层一致）——防止 VBR 文件总长度恰好与首帧长同余
	// 而被误判；其余情况（含带大尾标签的 CBR）落 VBR 逐帧扫描（精确）。
	remaining := size - audioStart
	spotOK := true
	for k := 1; k <= 2; k++ {
		off := audioStart + int64(k)*int64(frameLen)
		if off+4 > size {
			break
		}
		var h [4]byte
		if _, err := f.ReadAt(h[:], off); err != nil {
			spotOK = false
			break
		}
		_, sp, br, sr, ok := parseMPEGHeader(h[:])
		if !ok || br != bitrate || sr != samplerate || sp != samples {
			spotOK = false
			break
		}
	}
	if spotOK && remaining%int64(frameLen) < 64 {
		return float64(remaining/int64(frameLen)) * float64(samples) / float64(samplerate)
	}

	// VBR 兜底：从首帧起逐帧扫描计数（每帧按帧头算帧长 seek 跳过）；
	// 遇到无效帧头视为音频结束，用已累计帧数。
	frames := int64(0)
	for off := audioStart; off+4 <= size; {
		var h [4]byte
		if _, err := f.ReadAt(h[:], off); err != nil {
			break
		}
		fl, _, _, _, ok := parseMPEGHeader(h[:])
		if !ok || fl < 4 {
			break
		}
		frames++
		off += int64(fl)
	}
	if frames > 0 {
		return float64(frames) * float64(samples) / float64(samplerate)
	}
	return 0
}

// flacDuration 解析 FLAC 时长：STREAMINFO 的 sample rate（20bit，块内字节 10 起）
// 与 total samples（36bit）。
func flacDuration(f *os.File, size int64) float64 {
	var magic [4]byte
	if size < 4 {
		return 0
	}
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "fLaC" {
		return 0
	}

	off := int64(4)
	for off+4 <= size {
		var bh [4]byte
		if _, err := f.ReadAt(bh[:], off); err != nil {
			return 0
		}
		btype := bh[0] & 0x7F
		last := bh[0]&0x80 != 0
		blen := int64(bh[1])<<16 | int64(bh[2])<<8 | int64(bh[3])
		if off+4+blen > size {
			return 0 // 截断
		}
		if btype == 0 { // STREAMINFO
			if blen < 18 {
				return 0
			}
			var si [18]byte
			if _, err := f.ReadAt(si[:], off+4); err != nil {
				return 0
			}
			samplerate := int64(si[10])<<12 | int64(si[11])<<4 | int64(si[12])>>4
			total := uint64(si[13]&0x0F)<<32 | uint64(si[14])<<24 | uint64(si[15])<<16 |
				uint64(si[16])<<8 | uint64(si[17])
			if samplerate > 0 {
				return float64(total) / float64(samplerate)
			}
			return 0
		}
		off += 4 + blen
		if last {
			break
		}
	}
	return 0
}

// wavDuration 解析 WAV 时长：fmt chunk 的 byteRate（offset 8）+ data chunk 大小。
func wavDuration(f *os.File, size int64) float64 {
	var hdr [12]byte
	if size < 12 {
		return 0
	}
	if _, err := f.ReadAt(hdr[:], 0); err != nil ||
		string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0
	}

	var byteRate int64
	off := int64(12)
	for off+8 <= size {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], off); err != nil {
			return 0
		}
		id := string(ch[:4])
		clen := int64(readUint32BE(ch[4:8]))
		if off+8+clen > size {
			return 0 // 截断
		}
		switch {
		case id == "fmt ":
			if clen < 16 {
				return 0
			}
			var fmtData [16]byte
			if _, err := f.ReadAt(fmtData[:], off+8); err != nil {
				return 0
			}
			byteRate = int64(readUint32BE(fmtData[8:12]))
		case id == "data" && byteRate > 0:
			return float64(clen) / float64(byteRate)
		}
		off += 8 + clen
		if clen%2 == 1 {
			off++ // chunk 按 2 字节对齐
		}
	}
	return 0
}

// m4aDuration 解析 M4A/MP4 时长：顶层找 moov → 其内找 mvhd（v0/v1）。
func m4aDuration(f *os.File, size int64) float64 {
	off := int64(0)
	for off+8 <= size {
		var ah [8]byte
		if _, err := f.ReadAt(ah[:], off); err != nil {
			return 0
		}
		sz := int64(readUint32BE(ah[:4]))
		typ := string(ah[4:8])
		payloadStart := off + 8
		var payloadLen int64
		switch {
		case sz == 1: // 64 位扩展大小
			var ext [8]byte
			if _, err := f.ReadAt(ext[:], payloadStart); err != nil {
				return 0
			}
			payloadLen = int64(readUint64BE(ext[:])) - 16
			payloadStart += 8
		case sz == 0: // 到文件尾
			payloadLen = size - payloadStart
		default:
			if sz < 8 {
				return 0
			}
			payloadLen = sz - 8
		}
		if payloadLen < 0 || payloadStart+payloadLen > size {
			return 0 // 截断
		}
		if typ == "moov" {
			return mvhdDuration(f, payloadStart, payloadLen)
		}
		off = payloadStart + payloadLen
	}
	return 0
}

// mvhdDuration 在 moov 内容内按 atom 边界找 mvhd 并解析时长。
func mvhdDuration(f *os.File, start, length int64) float64 {
	end := start + length
	for off := start; off+8 <= end; {
		var ah [8]byte
		if _, err := f.ReadAt(ah[:], off); err != nil {
			return 0
		}
		sz := int64(readUint32BE(ah[:4]))
		typ := string(ah[4:8])
		switch {
		case sz == 1:
			var ext [8]byte
			if _, err := f.ReadAt(ext[:], off+8); err != nil {
				return 0
			}
			sz = int64(readUint64BE(ext[:]))
			if sz < 16 {
				return 0
			}
		case sz == 0:
			sz = end - off
		}
		if sz < 8 || off+sz > end {
			return 0 // 截断
		}
		if typ == "mvhd" {
			return parseMVHD(f, off+8, sz-8)
		}
		off += sz
	}
	return 0
}

// parseMVHD 解析 mvhd 内容（原子头之后）：v0 为 4 字节 timescale + 4 字节
// duration，v1 为 4 字节 timescale + 8 字节 duration。
func parseMVHD(f *os.File, start, length int64) float64 {
	if length < 20 {
		return 0
	}
	var vb [4]byte
	if _, err := f.ReadAt(vb[:], start); err != nil {
		return 0
	}
	var timescale, duration int64
	switch vb[0] {
	case 0: // version 0
		if length < 20 {
			return 0
		}
		var b [8]byte
		if _, err := f.ReadAt(b[:], start+12); err != nil {
			return 0
		}
		timescale = int64(readUint32BE(b[:4]))
		duration = int64(readUint32BE(b[4:8]))
	case 1: // version 1
		if length < 32 {
			return 0
		}
		var b [12]byte
		if _, err := f.ReadAt(b[:], start+20); err != nil {
			return 0
		}
		timescale = int64(readUint32BE(b[:4]))
		duration = int64(readUint64BE(b[4:12]))
	default:
		return 0
	}
	if timescale <= 0 || duration < 0 {
		return 0
	}
	return float64(duration) / float64(timescale)
}

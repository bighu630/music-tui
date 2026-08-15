package local

// 标签/时长读取测试：fixture 全部手工构造二进制（不依赖 ffmpeg），
// 覆盖 readTags（ID3 标签优先语义）与 readDuration（mp3/flac/m4a/wav 解析）。

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ---- 二进制 fixture 构造 ----

func be16(n uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, n)
	return b
}

func be32(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}

// syncsafe 将 n 编码为 4 字节 ID3v2 syncsafe 整数。
func syncsafe(n int) []byte {
	return []byte{
		byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F),
	}
}

// id3TextFrame 构造一个 ID3v2.3 文本帧（encoding 3 = UTF-8，便于中文断言）。
func id3TextFrame(id, text string) []byte {
	content := append([]byte{3}, text...)
	f := append([]byte(id), be32(uint32(len(content)))...)
	f = append(f, 0x00, 0x00) // frame flags
	return append(f, content...)
}

// mpeg1Layer3Bitrate 是 MPEG1 Layer3 比特率表（kbps），index 0 = free。
var mpeg1Layer3Bitrate = [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}

// mp3Frame 构造一帧 MPEG1 Layer3 音频帧（给定 bitrate index，44100Hz 无 padding）。
func mp3Frame(bitrateIndex int) []byte {
	h := []byte{0xFF, 0xFB, byte(bitrateIndex << 4), 0x00}
	frameLen := 144 * mpeg1Layer3Bitrate[bitrateIndex] * 1000 / 44100
	f := make([]byte, frameLen)
	copy(f, h)
	return f
}

// writeID3v2MP3 写一个 ID3v2.3 标签（TIT2/TPE1，UTF-8）+ N 帧 MPEG1 Layer3 音频。
// title/artist 为空时对应帧省略（模拟字段缺失）；两者皆空时不写 ID3 标签。
func writeID3v2MP3(t *testing.T, path, title, artist string, frames int) {
	t.Helper()
	var b []byte
	if title != "" || artist != "" {
		var tagBody []byte
		if title != "" {
			tagBody = append(tagBody, id3TextFrame("TIT2", title)...)
		}
		if artist != "" {
			tagBody = append(tagBody, id3TextFrame("TPE1", artist)...)
		}
		b = append(b, "ID3"...)
		b = append(b, 0x03, 0x00, 0x00) // v2.3，revision 0，无 flags
		b = append(b, syncsafe(len(tagBody))...)
		b = append(b, tagBody...)
	}
	for i := 0; i < frames; i++ {
		b = append(b, mp3Frame(9)...) // 128kbps
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeFLAC 写一个最小 FLAC：fLaC + STREAMINFO（sample rate 44100、双声道 16bit、
// total samples 参数）+ 收尾块。
func writeFLAC(t *testing.T, path string, totalSamples uint64) {
	t.Helper()
	si := make([]byte, 34)
	// 字节 10 起：sample rate 20bit / channels 3bit / bps 5bit / total samples 36bit
	si[10] = byte(44100 >> 12)                     // 0x0A
	si[11] = byte(44100 >> 4 & 0xFF)               // 0xC4
	si[12] = byte(44100&0xF)<<4 | 1<<1 | (16-1)>>4 // sample rate 低 4 位 + ch-1=1 + bps-1 高位
	si[13] = byte(16-1&0xF)<<4 | byte(totalSamples>>32&0xF)
	si[14] = byte(totalSamples >> 24)
	si[15] = byte(totalSamples >> 16)
	si[16] = byte(totalSamples >> 8)
	si[17] = byte(totalSamples)

	var b []byte
	b = append(b, "fLaC"...)
	b = append(b, 0x00, 0x00, 0x00, 0x22) // STREAMINFO（type 0，非 last，length 34）
	b = append(b, si...)
	b = append(b, 0x81, 0x00, 0x00, 0x00) // 收尾块（last + PADDING，length 0）
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeWAV 写一个最小 WAV：PCM 44100Hz 16bit 双声道（byteRate=176400）+ data chunk。
func writeWAV(t *testing.T, path string, dataBytes int) {
	t.Helper()
	var b []byte
	b = append(b, "RIFF"...)
	b = append(b, be32(uint32(36+dataBytes))...)
	b = append(b, "WAVE"...)
	b = append(b, "fmt "...)
	b = append(b, be32(16)...)
	b = append(b, be16(1)...)      // PCM
	b = append(b, be16(2)...)      // 双声道
	b = append(b, be32(44100)...)  // sample rate
	b = append(b, be32(176400)...) // byteRate = 44100*2*2
	b = append(b, be16(4)...)      // blockAlign
	b = append(b, be16(16)...)     // bitsPerSample
	b = append(b, "data"...)
	b = append(b, be32(uint32(dataBytes))...)
	b = append(b, make([]byte, dataBytes)...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeM4A 写一个最小 M4A：ftyp + moov/mvhd v0（timescale 1000、duration=durationSec*1000）。
func writeM4A(t *testing.T, path string, durationSec uint32) {
	t.Helper()
	var b []byte
	// ftyp（24 字节）
	b = append(b, be32(24)...)
	b = append(b, "ftyp"...)
	b = append(b, "M4A "...)
	b = append(b, be32(0)...)
	b = append(b, "M4A "...)
	// moov → mvhd v0
	var mvhdPayload []byte
	mvhdPayload = append(mvhdPayload, 0x00, 0x00, 0x00, 0x00)    // version 0 + flags
	mvhdPayload = append(mvhdPayload, make([]byte, 8)...)        // creation + modification time
	mvhdPayload = append(mvhdPayload, be32(1000)...)             // timescale
	mvhdPayload = append(mvhdPayload, be32(durationSec*1000)...) // duration
	mvhdPayload = append(mvhdPayload, make([]byte, 80)...)       // 其余字段（rate/volume/matrix 等）
	mvhd := append(be32(uint32(8+len(mvhdPayload))), "mvhd"...)
	mvhd = append(mvhd, mvhdPayload...)
	var moov []byte
	moov = append(moov, be32(uint32(8+len(mvhd)))...)
	moov = append(moov, "moov"...)
	moov = append(moov, mvhd...)
	b = append(b, be32(uint32(8+len(moov)))...)
	b = append(b, moov...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- readTags ----

func TestReadTagsID3(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tagged.mp3")
	writeID3v2MP3(t, p, "  晴天  ", " 周杰伦 ", 3)

	title, artist, ok := readTags(p)
	if !ok {
		t.Fatalf("readTags(%q) ok=false，期望读出标签", p)
	}
	if title != "晴天" {
		t.Errorf("Title = %q，期望 %q（TrimSpace 后）", title, "晴天")
	}
	if artist != "周杰伦" {
		t.Errorf("Artist = %q，期望 %q（TrimSpace 后）", artist, "周杰伦")
	}

	// 无标签的纯 MPEG 帧 → ok=false
	plain := filepath.Join(dir, "plain.mp3")
	writeID3v2MP3(t, plain, "", "", 3)
	if _, _, ok := readTags(plain); ok {
		t.Errorf("readTags(无标签纯帧) ok=true，期望 false")
	}

	// 不存在的文件 → ok=false（不报错）
	if _, _, ok := readTags(filepath.Join(dir, "no-such.mp3")); ok {
		t.Errorf("readTags(不存在文件) ok=true，期望 false")
	}
}

func TestReadTagsTitleOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	writeID3v2MP3(t, p, "七里香", "", 3)

	title, artist, ok := readTags(p)
	if !ok {
		t.Fatalf("readTags(%q) ok=false，期望 Title 非空时 ok=true", p)
	}
	if title != "七里香" {
		t.Errorf("Title = %q，期望 %q", title, "七里香")
	}
	if artist != "" {
		t.Errorf("Artist = %q，期望空串（缺失字段由调用方回退）", artist)
	}
}

// ---- readDuration：mp3 ----

func TestReadDurationMP3CBR(t *testing.T) {
	// 带 ID3v2 标签的 CBR 文件：500 帧 128kbps → 500*1152/44100 s
	p := filepath.Join(t.TempDir(), "cbr.mp3")
	writeID3v2MP3(t, p, "晴天", "周杰伦", 500)
	got := readDuration(p)
	want := 500.0 * 1152 / 44100 // ≈ 13.061
	if math.Abs(got-want) > 0.01 {
		t.Errorf("readDuration(带标签 CBR) = %v，期望 ≈ %v", got, want)
	}

	// 无 ID3 的纯帧 CBR → 时长仍正确
	p2 := filepath.Join(t.TempDir(), "plain.mp3")
	writeID3v2MP3(t, p2, "", "", 500)
	got = readDuration(p2)
	if math.Abs(got-want) > 0.01 {
		t.Errorf("readDuration(纯帧 CBR) = %v，期望 ≈ %v", got, want)
	}
}

func TestReadDurationMP3VBR(t *testing.T) {
	// VBR：10 帧不同 bitrate（无 Xing 头）→ 逐帧扫描，时长 = 10*1152/44100
	p := filepath.Join(t.TempDir(), "vbr.mp3")
	var b []byte
	for _, i := range []int{10, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
		b = append(b, mp3Frame(i)...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDuration(p)
	want := 10.0 * 1152 / 44100 // ≈ 0.261
	if math.Abs(got-want) > 0.01 {
		t.Errorf("readDuration(VBR) = %v，期望 ≈ %v", got, want)
	}
}

func TestReadDurationMP3Xing(t *testing.T) {
	// 首帧 payload 内嵌 Xing 头（立体声 offset 36），声明 500 帧 → 快路径
	p := filepath.Join(t.TempDir(), "xing.mp3")
	frame := mp3Frame(9)                                // 417 字节：4 头 + 413 payload
	xing := append([]byte("Xing"), be32(0x00000003)...) // flags: frames+bytes
	xing = append(xing, be32(500)...)
	xing = append(xing, be32(500*417)...)
	copy(frame[36:], xing)
	if err := os.WriteFile(p, frame, 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDuration(p)
	want := 500.0 * 1152 / 44100 // ≈ 13.061
	if math.Abs(got-want) > 0.01 {
		t.Errorf("readDuration(Xing) = %v，期望 ≈ %v", got, want)
	}
}

// ---- readDuration：flac / wav / m4a ----

func TestReadDurationFLAC(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.flac")
	writeFLAC(t, p, 88200)
	got := readDuration(p)
	if math.Abs(got-2.0) > 0.001 {
		t.Errorf("readDuration(FLAC) = %v，期望 2.0", got)
	}
}

func TestReadDurationWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.wav")
	writeWAV(t, p, 882000)
	got := readDuration(p)
	if math.Abs(got-5.0) > 0.001 {
		t.Errorf("readDuration(WAV) = %v，期望 5.0", got)
	}
}

func TestReadDurationM4A(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.m4a")
	writeM4A(t, p, 285)
	got := readDuration(p)
	if math.Abs(got-285.0) > 0.001 {
		t.Errorf("readDuration(M4A) = %v，期望 285.0", got)
	}
}

// ---- readDuration：不支持 / 损坏 ----

func TestReadDurationUnsupportedCorrupt(t *testing.T) {
	dir := t.TempDir()

	// ogg 扩展名：无时长解析 → 0
	ogg := filepath.Join(dir, "a.ogg")
	if err := os.WriteFile(ogg, []byte("OggS\x00\x02"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDuration(ogg); got != 0 {
		t.Errorf("readDuration(ogg) = %v，期望 0", got)
	}

	// 垃圾字节 mp3：无有效帧头 → 0
	junk := filepath.Join(dir, "junk.mp3")
	if err := os.WriteFile(junk, []byte("this is not an mp3 file, just random bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDuration(junk); got != 0 {
		t.Errorf("readDuration(垃圾 mp3) = %v，期望 0", got)
	}

	// 空文件 → 0
	empty := filepath.Join(dir, "empty.flac")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDuration(empty); got != 0 {
		t.Errorf("readDuration(空文件) = %v，期望 0", got)
	}

	// 截断的 flac（只有 fLaC 魔数）→ 0
	trunc := filepath.Join(dir, "trunc.flac")
	if err := os.WriteFile(trunc, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDuration(trunc); got != 0 {
		t.Errorf("readDuration(截断 flac) = %v，期望 0", got)
	}

	// 不存在的文件 → 0
	if got := readDuration(filepath.Join(dir, "no-such.mp3")); got != 0 {
		t.Errorf("readDuration(不存在文件) = %v，期望 0", got)
	}
}

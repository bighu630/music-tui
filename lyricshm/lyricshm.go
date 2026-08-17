// Package lyricshm 把当前歌词行写入共享内存文件(默认 /dev/shm/lyrics),
// 供 OBS 歌词、桌面小部件、脚本实时读取。仅 Linux 启用;目录缺失或写入
// 失败时静默降级(仅日志,不报错、不 panic)。
package lyricshm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"music-tui/logger"
)

// DefaultPath 默认歌词文件路径(Linux tmpfs 内存文件系统)。
const DefaultPath = "/dev/shm/lyrics"

// Writer 把当前歌词行覆盖写入单个文件。
type Writer struct {
	path    string
	enabled bool
}

// New 创建 Writer:path 为空时用 DefaultPath。仅当运行平台为 Linux 且
// 目标文件所在目录存在时启用;否则返回禁用 Writer(调用方无需分支,
// WriteLine 为 no-op)。禁用原因打一条 Info 日志。
func New(path string) *Writer {
	p := path
	if p == "" {
		p = DefaultPath
	}
	w := &Writer{path: p}
	if runtime.GOOS != "linux" {
		logger.Info("lyricshm: 非 Linux 平台(%s),歌词文件写入禁用", runtime.GOOS)
		return w
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		logger.Info("lyricshm: 目录 %s 不可用(%v),歌词文件写入禁用", filepath.Dir(p), err)
		return w
	}
	w.enabled = true
	return w
}

// NewForTest 创建一个跳过平台/目录检查的启用 Writer（故意不绑定平台限制，
// 供测试在多平台构造写入用例。生产代码请用 New——非 Linux 平台歌词文件写入
// 本就应禁用（writeToShm 是 Linux 专属特性）。
func NewForTest(path string) *Writer {
	return &Writer{path: path, enabled: true}
}

// WriteLine 覆盖写入 text 并追加换行。text 为空白串(TrimSpace 为空)时跳过,
// 保留文件现有内容(空行保留上一行歌词)。禁用时 no-op;写入失败仅 Warn 日志。
func (w *Writer) WriteLine(text string) {
	if !w.enabled {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := os.WriteFile(w.path, []byte(text+"\n"), 0o644); err != nil {
		logger.Warn("lyricshm: 写入 %s 失败: %v", w.path, err)
	}
}

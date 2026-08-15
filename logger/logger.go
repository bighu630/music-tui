// Package logger 提供 music-tui 的文件日志：写入 os.TempDir()/music-tui.log，
// 带级别过滤与大小轮转（默认单文件 5MB，超限轮转到 .1，保留最近一份）。
// 进程内全局单例，所有函数并发安全；Init 失败静默降级为 no-op——
// 日志是辅助设施，绝不阻断启动。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level 是日志级别，数值越大越严重。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// 包级可调变量：测试可改 logPath 与 MaxFileSize 验证轮转。
var (
	// logPath 是日志文件路径（默认 os.TempDir()/music-tui.log）。
	logPath = filepath.Join(os.TempDir(), "music-tui.log")
	// MaxFileSize 是单文件大小上限（字节），超限轮转到 .1。
	MaxFileSize = int64(5 * 1024 * 1024)
)

// NormalizeLevel 返回 s 的规范化级别字符串（大小写不敏感）；空/非法回落 "info"。
func NormalizeLevel(s string) string {
	switch strings.ToLower(s) {
	case "debug":
		return "debug"
	case "warn":
		return "warn"
	case "error":
		return "error"
	default:
		return "info"
	}
}

// ParseLevel 解析级别字符串；空/非法回落 LevelInfo。
func ParseLevel(s string) Level {
	switch NormalizeLevel(s) {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

var (
	mu    sync.Mutex
	level = LevelInfo
	file  *os.File
	size  int64
)

// Init 打开/创建日志文件（追加，0600）并设置级别；重复调用重新打开
// （测试替换 logPath 后重新 Init 生效）。失败静默降级：后续日志 no-op。
func Init(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
	open()
}

// SetLevel 运行中调整级别（config 加载后调用）。
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// Debug/Info/Warn/Error 按级别写日志；低于当前级别直接丢弃。
func Debug(format string, args ...any) { logf(LevelDebug, format, args...) }
func Info(format string, args ...any)  { logf(LevelInfo, format, args...) }
func Warn(format string, args ...any)  { logf(LevelWarn, format, args...) }
func Error(format string, args ...any) { logf(LevelError, format, args...) }

// levelName 返回级别显示名。
func levelName(l Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	default:
		return "ERROR"
	}
}

// open 打开日志文件并记录当前大小；失败置 file=nil（调用方持 mu）。
func open() {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		file = nil
		return
	}
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	} else {
		size = 0
	}
	file = f
}

// rotate 轮转：关闭当前文件 → 删旧 .1 → rename 为 .1 → 重新打开（调用方持 mu）。
func rotate() {
	if file != nil {
		file.Close()
		file = nil
	}
	_ = os.Remove(logPath + ".1")
	_ = os.Rename(logPath, logPath+".1")
	open()
}

// logf 写一行日志：级别过滤 → 轮转检查 → 写入。写失败（磁盘满等）关闭
// 文件降级 no-op，避免每次写都失败（调用方持 mu）。
func logf(l Level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if l < level || file == nil {
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), levelName(l), fmt.Sprintf(format, args...))
	if size+int64(len(line)) > MaxFileSize {
		rotate()
		if file == nil {
			return
		}
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		file = nil
		return
	}
	size += int64(len(line))
}

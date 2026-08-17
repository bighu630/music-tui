package local

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"music-tui/model"
)

// SupportedExts 是本地扫描支持的音频扩展名（匹配时大小写不敏感）。
var SupportedExts = []string{".mp3", ".flac", ".m4a", ".wav", ".ogg", ".opus", ".aac"}

// IsSupportedExt 按扩展名（大小写不敏感）判断是否为支持的音频文件。
func IsSupportedExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, s := range SupportedExts {
		if ext == s {
			return true
		}
	}
	return false
}

// Scan 扫描本地路径（文件或目录）得到歌曲列表。
//
//   - path 不存在 → 错误「路径不存在」；
//   - path 是文件：扩展名不支持 → 错误「不支持的音频格式」，支持 → 单曲列表；
//   - path 是目录：递归扫描全部子目录（filepath.WalkDir），只收扩展名匹配的
//     常规文件（d.Type().IsRegular()：悬空符号链接/指向目录的 .mp3 链接等
//     非常规文件一律跳过，避免单个坏链接毁掉整个目录扫描），
//     按完整路径字符串排序（sort.Strings）保证稳定顺序；
//     一个都没有 → 错误「目录中没有找到支持的音频文件」。
//
// 每个文件经 FromPath 映射为 model.Track。
func Scan(path string) ([]model.Track, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("路径不存在: %s", path)
		}
		return nil, err
	}

	// 单文件输入
	if !info.IsDir() {
		if !IsSupportedExt(path) {
			return nil, fmt.Errorf("不支持的音频格式: %s", path)
		}
		tr, err := FromPath(path)
		if err != nil {
			return nil, err
		}
		return []model.Track{tr}, nil
	}

	// 目录：递归收集扩展名匹配的常规文件（非常规文件如符号链接一律跳过：
	// 悬空链接或指向目录的 .mp3 链接会让 FromPath 失败，一个坏链接毁掉
	// 整个目录扫描）
	var paths []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || !IsSupportedExt(p) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描目录失败: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("目录中没有找到支持的音频文件: %s", path)
	}

// 按完整路径排序：Windows 路径分隔符为 '\'（0x5C > '2'=0x32），
// 字典序会令 "sub2\\..." 排在 "sub\\..." 之前，与 Linux 的 '/'(0x2F)
// 层级语义不一致。统一把分隔符视作 '/' 比较（filepath.ToSlash），
// 保证跨平台一致的稳定层级顺序。
	sort.Slice(paths, func(i, j int) bool {
		return filepath.ToSlash(paths[i]) < filepath.ToSlash(paths[j])
	})

	tracks := make([]model.Track, 0, len(paths))
	for _, p := range paths {
		tr, err := FromPath(p)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, tr)
	}
	return tracks, nil
}

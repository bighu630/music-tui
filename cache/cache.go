package cache

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"music-tui/model"
)

// 包级可调变量（测试可临时调小）。
var (
	DownloadTimeout      = 5 * time.Minute  // 单次后台下载总超时
	ExtractTimeout       = 60 * time.Second // yt-dlp 取直链超时
	DownloadRetryBackoff = 2 * time.Second  // 下载失败重试间隔
)

// defaultMaxEntries 与 config.DefaultMaxEntries 保持一致（本地常量避免循环 import）。
const defaultMaxEntries = 100

// IndexFile 是索引文件名（缓存目录内）；main 备份损坏索引时引用同一常量。
const IndexFile = "index.json"

// Manager 音频缓存门面；所有方法并发安全。
type Manager struct {
	mu         sync.Mutex
	enabled    bool
	dir        string
	maxEntries int
	idx        index
	inflight   map[string]bool
	ytdlpPath  string
	client     *http.Client
	extract    extractFunc
}

// New 创建 Manager：MkdirAll → 加载索引（缺失=空，损坏=返回错误）→
// 启动清理（条目文件缺失删条目；超限按 LastPlayed 淘汰最旧并删文件；有变化则持久化）。
// opts 规范化：MaxEntries<1 → 100；Dir=="" → 错误。
func New(opts Options, ytdlpPath string) (*Manager, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("缓存目录为空")
	}
	maxEntries := opts.MaxEntries
	if maxEntries < 1 {
		maxEntries = defaultMaxEntries
	}
	m := &Manager{
		enabled:    opts.Enabled,
		dir:        opts.Dir,
		maxEntries: maxEntries,
		inflight:   map[string]bool{},
		ytdlpPath:  ytdlpPath,
		client:     &http.Client{},
	}
	m.extract = func(ctx context.Context, url string) (string, string, error) {
		return realExtract(ctx, m.ytdlpPath, url)
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建缓存目录: %w", err)
	}
	// 清理上次异常退出（kill -9/断电）残留的 .part 临时文件，避免永久滞留
	if parts, err := filepath.Glob(filepath.Join(opts.Dir, "*.part")); err == nil {
		for _, p := range parts {
			os.Remove(p) // 失败忽略：下次启动再试
		}
	}
	ix, err := load(m.indexPath())
	if err != nil {
		return nil, err
	}
	m.idx = *ix

	// 启动清理：先校验文件名合法性（防被篡改的 index.json 路径穿越删目录外文件），
	// 再对合法条目做缺失文件清理与超限淘汰。
	changed := false
	kept := make([]Entry, 0, len(m.idx.entries))
	for _, e := range m.idx.entries {
		if !validCacheFile(e.File) {
			changed = true // 非法文件名（含路径分隔符/绝对路径/“.”/“..”）→ 删条目
			continue
		}
		if _, err := os.Stat(filepath.Join(m.dir, e.File)); err != nil {
			changed = true // 条目文件缺失 → 删条目
			continue
		}
		kept = append(kept, e)
	}
	m.idx.entries = kept
	for m.idx.len() > m.maxEntries {
		e, _ := m.idx.oldest()
		os.Remove(filepath.Join(m.dir, e.File))
		m.idx.remove(e.ID)
		changed = true
	}
	if changed {
		if err := m.idx.save(m.indexPath()); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Disabled 返回禁用态 Manager（Lookup 恒 miss、CacheAsync/Remove 为 no-op、Register 返回 nil）。
func Disabled() *Manager {
	return &Manager{}
}

// Enabled 返回缓存开关状态。
func (m *Manager) Enabled() bool { return m.enabled }

// indexPath 返回索引文件完整路径（调用方须确保 dir 非空）。
func (m *Manager) indexPath() string { return filepath.Join(m.dir, IndexFile) }

// validCacheFile 校验索引条目文件名只能是指向缓存目录内文件的纯文件名
// （非空、非 "."/".."、无路径分隔符），防止被篡改的 index.json 通过
// 路径穿越删除缓存目录外文件或把目录外路径交给播放器。
func validCacheFile(file string) bool {
	return file != "" && file != "." && file != ".." && filepath.Base(file) == file
}

// Lookup 命中判定：开关开 + 索引有条目 + 文件存在。
// 命中 → 刷新 LastPlayed=now 并持久化；条目在但文件缺失 → 移除条目，返回 miss。
func (m *Manager) Lookup(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || m.dir == "" {
		return "", false
	}
	e, ok := m.idx.get(id)
	if !ok {
		return "", false
	}
	full := filepath.Join(m.dir, e.File)
	if _, err := os.Stat(full); err != nil {
		m.idx.remove(id) // 文件缺失移除条目（不持久化）
		return "", false
	}
	m.idx.upsert(id, time.Now())
	m.idx.save(m.indexPath()) // 刷新持久化，忽略错误
	return full, true
}

// CacheAsync 后台异步下载并注册（不阻塞、立即返回）：
// 开关关/同 ID 在途/条目已存在 → no-op。
func (m *Manager) CacheAsync(track model.Track) {
	m.mu.Lock()
	if !m.enabled || m.dir == "" {
		m.mu.Unlock()
		return
	}
	if m.inflight[track.ID] {
		m.mu.Unlock()
		return
	}
	if _, ok := m.idx.get(track.ID); ok {
		m.mu.Unlock()
		return
	}
	m.inflight[track.ID] = true
	m.mu.Unlock()

	go m.download(track)
}

// download 执行一次后台下载：取直链 → 下载到缓存目录 → 注册进索引。
// 任一步失败仅 log.Printf；结束必清除 inflight 标记。
func (m *Manager) download(track model.Track) {
	defer func() {
		m.mu.Lock()
		delete(m.inflight, track.ID)
		m.mu.Unlock()
	}()
	if !m.enabled || m.dir == "" || m.extract == nil {
		return // Disabled 安全（正常流程 CacheAsync 已拦截，此处为防御）
	}

	ctx, cancel := context.WithTimeout(context.Background(), DownloadTimeout)
	defer cancel()

	extractCtx, cancelExtract := context.WithTimeout(ctx, ExtractTimeout)
	streamURL, ext, err := m.extract(extractCtx, track.URL)
	cancelExtract()
	if err != nil {
		log.Printf("缓存下载失败(%s): %v", track.ID, err)
		return
	}
	dest := filepath.Join(m.dir, SafeName(track.ID))
	if ext != "" {
		dest += "." + ext
	}
	if _, err := downloadFile(ctx, m.client, streamURL, dest); err != nil {
		log.Printf("缓存下载失败(%s): %v", track.ID, err)
		return
	}
	if err := m.register(track.ID, filepath.Base(dest)); err != nil {
		log.Printf("缓存下载失败(%s): %v", track.ID, err)
	}
}

// Register 把已存在的缓存文件注册进索引（下载完成/测试预置用）：
// 刷新 LastPlayed、持久化、超限淘汰最旧（删文件）。不校验文件是否存在（Lookup 会校验）。
func (m *Manager) Register(id string) error {
	return m.register(id, SafeName(id))
}

// register 注册指定文件名的条目（内部：download 完成时文件名含扩展名）。
func (m *Manager) register(id, file string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		return nil // Disabled 安全
	}
	m.idx.upsertFile(id, file, time.Now())
	max := m.maxEntries
	if max < 1 {
		max = defaultMaxEntries
	}
	for m.idx.len() > max {
		e, _ := m.idx.oldest()
		os.Remove(filepath.Join(m.dir, e.File))
		m.idx.remove(e.ID)
	}
	return m.idx.save(m.indexPath())
}

// Remove 删除缓存文件 + 索引条目 + 持久化；不存在返回 nil。
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		return nil // Disabled 安全
	}
	if e, ok := m.idx.get(id); ok {
		os.Remove(filepath.Join(m.dir, e.File))
	}
	if !m.idx.remove(id) {
		return nil
	}
	return m.idx.save(m.indexPath())
}

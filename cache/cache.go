package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"music-tui/logger"
	"music-tui/model"
)

// 包级可调变量（测试可临时调小）。
var (
	DownloadTimeout        = 5 * time.Minute  // 单次后台下载总超时（所有尝试共享）
	DownloadAttemptTimeout = 90 * time.Second // 单次 yt-dlp 下载尝试超时
	DownloadRetryBackoff   = 2 * time.Second  // 下载失败整进程重跑间隔
	MaxDownloadAttempts    = 5                // 下载失败整进程重跑预算（每次重新提取新 URL）
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
	inflight   map[string]chan struct{}
	ytdlpPath  string
	cookieFile string
	headers    map[string]string
}

// New 创建 Manager：MkdirAll → 加载索引（缺失=空，损坏=返回错误）→
// 启动清理（条目文件缺失删条目；内容有效性校验：非音频（HTML/截断）删文件+删条目；
// 超限按 LastPlayed 淘汰最旧并删文件；有变化则持久化）。
// opts 规范化：MaxEntries<1 → 100；Dir=="" → 错误。
// cookieFile/headers 可选：附加到 yt-dlp 下载参数（--cookies/--add-header），
// 均空时不改变既有行为。
func New(opts Options, ytdlpPath string, cookieFile string, headers map[string]string) (*Manager, error) {
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
		inflight:   map[string]chan struct{}{},
		ytdlpPath:  ytdlpPath,
		cookieFile: cookieFile,
		headers:    headers,
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建缓存目录: %w", err)
	}
	// 清理上次异常退出（kill -9/断电）残留的 .part 临时文件，避免永久滞留。
	// 不用 filepath.Glob：缓存目录路径含 glob 元字符（如 "cache[x]"）时匹配会失效。
	if entries, err := os.ReadDir(opts.Dir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".part") {
				logger.Debug("缓存启动清理: 删除 .part 残留 %s", e.Name())
				os.Remove(filepath.Join(opts.Dir, e.Name())) // 失败忽略：下次启动再试
			}
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
			logger.Warn("缓存启动清理: 非法文件名，删条目 %q", e.File)
			changed = true // 非法文件名（含路径分隔符/绝对路径/“.”/“..”）→ 删条目
			continue
		}
		if _, err := os.Stat(filepath.Join(m.dir, e.File)); err != nil {
			logger.Info("缓存启动清理: 条目文件缺失，删条目 %s (%s)", e.ID, e.File)
			changed = true // 条目文件缺失 → 删条目
			continue
		}
		// 内容有效性：条目文件被替换为 HTML/截断等非音频 → 删文件 + 删条目
		//（防损坏文件滞留；changed=true 最后由统一 save 持久化）
		if ok, err := isAudioFile(filepath.Join(m.dir, e.File)); err != nil || !ok {
			logger.Warn("缓存启动清理: 条目非音频(损坏)，删文件+条目 %s (%s)", e.ID, e.File)
			os.Remove(filepath.Join(m.dir, e.File))
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	m.idx.entries = kept
	if m.idx.len() > m.maxEntries {
		m.evictIfOverLimit()
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
		logger.Info("缓存条目文件缺失，移除: %s (%s)", id, full)
		m.idx.remove(id) // 文件缺失移除条目（不持久化）
		return "", false
	}
	// 内容有效性：文件被替换为 HTML/截断等非音频 → 删文件 + 删条目 + miss
	//（UI 自动回退网络取流，损坏文件不滞留）
	if ok, err := isAudioFile(full); err != nil || !ok {
		logger.Warn("缓存校验失败，删除损坏文件: %s (%s)", id, full)
		os.Remove(full)
		m.idx.remove(id)
		return "", false
	}
	logger.Debug("缓存命中: %s (%s)", id, full)
	m.idx.upsert(id, time.Now())
	m.idx.save(m.indexPath()) // 刷新持久化，忽略错误
	return full, true
}

// CacheAsync 后台异步下载并注册（不阻塞、立即返回）：
// 开关关/目录空/同 ID 在途/条目已存在 → no-op，返回 nil（没有下载发生）。
// 否则启动后台下载并返回完成信号 channel：下载彻底结束（成功注册进索引，
// 或预算耗尽失败）时关闭。调用方（如 preload 调度器）用 <-done 串行等待；
// 现有调用方忽略返回值即可（Go 语句调用允许忽略返回值）。
// 同 ID 在途时返回 nil 且不返回在途信号：preload 调度器依赖 done==nil 表示
// no-op 的语义；需要监听在途下载完成信号用 WaitDone。
func (m *Manager) CacheAsync(track model.Track) <-chan struct{} {
	m.mu.Lock()
	if !m.enabled || m.dir == "" {
		m.mu.Unlock()
		return nil
	}
	// 本地曲目不参与缓存下载：文件已在本地，网络缓存/下载无意义（下载只会把
	// 本地路径交给 yt-dlp 无谓失败）。cache 层防御：即使未来调用点误把本地
	// 曲目交给 CacheAsync 也不得启动下载（root 播放链路另有 Source 判断跳过）。
	if track.Source == model.SourceLocal {
		m.mu.Unlock()
		return nil
	}
	if m.inflight[track.ID] != nil {
		m.mu.Unlock()
		return nil
	}
	if _, ok := m.idx.get(track.ID); ok {
		m.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	m.inflight[track.ID] = done // 存入该次下载的完成信号，WaitDone 可取同一 channel
	m.mu.Unlock()

	go m.download(track, done)
	return done
}

// WaitDone 返回该 ID 在途下载的完成信号（与 CacheAsync 首次发起时返回的
// 是同一 channel）：仅监听不启动下载；下载彻底结束（成功注册或失败耗尽）
// 时信号关闭，调用方可 <-done 等待；无在途下载返回 nil。
// 用途：缓存兜底播放——UI 在 mpv URL 播放失败/卡住时，对已由 preload/预热
// 启动、正在在途的下载也能拿到完成信号，下载完成即切本地文件播放。
func (m *Manager) WaitDone(id string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inflight[id]
}

// download 执行一次后台下载：yt-dlp 直接下载到缓存目录（-o 模板落盘）→ 注册进索引。
// YouTube 对音频直链有概率性 403 风控：同一直链重试无意义（换 URL 才换结果），
// 因此失败整进程重跑 = 重新运行 yt-dlp = 重新提取新 URL；预算内最多
// MaxDownloadAttempts 次（每次尝试有 DownloadAttemptTimeout 子超时，总超时
// DownloadTimeout 封顶）。任一步失败仅记日志；结束必清除 inflight 标记，
// 且无论成功失败都关闭 done 完成信号（defer close，含开头的 Disabled 防御
// 分支）——通知 CacheAsync 调用方“下载彻底结束”。
func (m *Manager) download(track model.Track, done chan struct{}) {
	defer func() {
		m.mu.Lock()
		delete(m.inflight, track.ID)
		m.mu.Unlock()
		close(done) // 任何路径（含下方 Disabled 防御分支）都必须关闭完成信号
	}()
	if !m.enabled || m.dir == "" || m.ytdlpPath == "" {
		return // Disabled 安全（正常流程 CacheAsync 已拦截，此处为防御）
	}

	ctx, cancel := context.WithTimeout(context.Background(), DownloadTimeout)
	defer cancel()

	destBase := filepath.Join(m.dir, SafeName(track.ID))
	var lastErr error
	for attempt := 0; attempt < MaxDownloadAttempts; attempt++ {
		logger.Debug("缓存下载开始(%s) 第 %d/%d 次: %s - %s", track.ID, attempt+1, MaxDownloadAttempts, track.Title, track.Artist)
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, DownloadAttemptTimeout)
		file, err := realDownload(attemptCtx, m.ytdlpPath, track.URL, destBase, m.cookieFile, m.headers)
		cancelAttempt()
		if err == nil {
			if rerr := m.register(track.ID, file); rerr != nil {
				logger.Warn("缓存下载失败(%s): %v", track.ID, rerr)
				os.Remove(filepath.Join(m.dir, file)) // 注册失败删除已下载文件，避免孤儿滞留
			} else {
				logger.Info("缓存下载完成(%s): %s", track.ID, file)
			}
			return
		}
		lastErr = err
		if attempt+1 < MaxDownloadAttempts {
			select {
			case <-time.After(DownloadRetryBackoff):
			case <-ctx.Done():
				logger.Warn("缓存下载失败(%s): %v", track.ID, ctx.Err())
				return
			}
		}
	}
	logger.Warn("缓存下载失败(%s): %v", track.ID, lastErr)
}

// Register 把已存在的缓存文件注册进索引（下载完成/测试预置用）：
// 刷新 LastPlayed、持久化、超限淘汰最旧（删文件）。不校验文件是否存在（Lookup 会校验）。
func (m *Manager) Register(id string) error {
	return m.register(id, SafeName(id))
}

// register 注册指定文件名的条目（内部：download 完成时文件名含扩展名）。
// 写入前内容校验：产物被劫持为 HTML/截断等非音频 → 返回错误（download
// 失败分支会删除已下载文件，防孤儿滞留）。
func (m *Manager) register(id, file string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dir == "" {
		return nil // Disabled 安全
	}
	ok, err := isAudioFile(filepath.Join(m.dir, file))
	if err != nil {
		logger.Warn("缓存注册校验失败: %s (%s): %v", id, file, err)
		return fmt.Errorf("校验缓存文件: %w", err)
	}
	if !ok {
		logger.Warn("缓存注册校验失败: %s (%s): 内容非音频", id, file)
		return fmt.Errorf("缓存文件内容非音频（HTML 错误页或截断文件）: %s", file)
	}
	m.idx.upsertFile(id, file, time.Now())
	m.evictIfOverLimit()
	return m.idx.save(m.indexPath())
}

// evictIfOverLimit 超限淘汰最旧条目（删文件）；调用方应持 m.mu（New 构造期无并发除外）。
func (m *Manager) evictIfOverLimit() {
	max := m.maxEntries
	if max < 1 {
		max = defaultMaxEntries
	}
	for m.idx.len() > max {
		e, _ := m.idx.oldest()
		logger.Info("缓存超限淘汰: %s (%s)", e.ID, e.File)
		os.Remove(filepath.Join(m.dir, e.File))
		m.idx.remove(e.ID)
	}
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

package ytm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LoginMethod 是 YTM 登录方式。
type LoginMethod int

const (
	// MethodNone 未配置登录。
	MethodNone LoginMethod = iota
	// MethodBrowser 浏览器自动导出 cookie（懒导出到 cookies 文件）。
	MethodBrowser
	// MethodCookiesFile 用户指定 cookies.txt 文件路径。
	MethodCookiesFile
	// MethodPasted 粘贴 Cookie header 字符串（落盘 ytm-cookies.txt）。
	MethodPasted
)

func (m LoginMethod) String() string {
	switch m {
	case MethodBrowser:
		return "浏览器读取"
	case MethodCookiesFile:
		return "cookies.txt 文件"
	case MethodPasted:
		return "粘贴 Cookie 字符串"
	default:
		return "未登录"
	}
}

// LoginConfig 是 YTM 登录配置。
type LoginConfig struct {
	Method      LoginMethod `json:"method"`
	Browser     string      `json:"browser,omitempty"`      // MethodBrowser: chrome/chromium/brave/edge/vivaldi/opera
	CookiesPath string      `json:"cookies_path,omitempty"` // cookies 文件实际路径
	UpdatedAt   time.Time   `json:"updated_at"`
}

// SyncEntry 记录一次同步映射（远端歌单 → 本地列表）。
type SyncEntry struct {
	PlaylistID string    `json:"playlist_id"`
	ListName   string    `json:"list_name"`
	SyncedAt   time.Time `json:"synced_at"`
	Count      int       `json:"count"`
}

// ErrNoLogin 未配置登录时 CookieHeader/CookieFile 返回的错误。
var ErrNoLogin = errors.New("尚未配置 YouTube Music 登录")

// ytmFile 是 ytm.json 的磁盘结构。
type ytmFile struct {
	Login LoginConfig `json:"login"`
	Syncs []SyncEntry `json:"syncs,omitempty"`
}

// Store 持久化 ytm.json（默认 ~/.config/music-tui/ytm.json）：
// 权限 0600、原子写（.tmp + rename）。文件不存在 = 空；损坏返回错误
// （由 main 备份重建，与 playlists 降级一致）。所有方法并发安全。
type Store struct {
	mu   sync.Mutex
	path string
	data ytmFile
}

// NewStore 加载 ytm.json（不存在视为空；父目录自动创建）。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建 ytm 配置目录: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取 ytm 配置文件: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("解析 ytm 配置文件（损坏）: %w", err)
	}
	if s.data.Syncs == nil {
		s.data.Syncs = []SyncEntry{}
	}
	return s, nil
}

// Login 返回当前登录配置副本。
func (s *Store) Login() LoginConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Login
}

// SetLogin 保存登录配置（自动填充 UpdatedAt）。
func (s *Store) SetLogin(cfg LoginConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.Browser = strings.TrimSpace(cfg.Browser)
	cfg.CookiesPath = strings.TrimSpace(cfg.CookiesPath)
	cfg.UpdatedAt = time.Now()
	s.data.Login = cfg
	return s.saveLocked()
}

// ClearLogin 清除登录配置（保留 SyncEntry 映射）。
func (s *Store) ClearLogin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Login = LoginConfig{}
	return s.saveLocked()
}

// SetPastedLogin 保存粘贴的 Cookie header 文本（MethodPasted）：
// 先落盘 cookies 文件（0600，默认路径 ytm-cookies.txt，与 ytm.json 同目录），
// 再保存配置。Netscape 格式文本原样保留；单行 Cookie header 自动转为
// Netscape 格式（供 yt-dlp 读取）。返回实际 cookies 文件路径。
func (s *Store) SetPastedLogin(header string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.defaultCookiesPath()
	if err := writeFileAtomic(p, []byte(header), 0o600); err != nil {
		return "", fmt.Errorf("写入 cookie 文件失败: %w", err)
	}
	if err := ensurePastedFile(p); err != nil {
		return "", err
	}
	s.data.Login = LoginConfig{Method: MethodPasted, CookiesPath: p, UpdatedAt: time.Now()}
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return p, nil
}

// SyncEntries 返回全部同步映射副本（保持插入顺序）。
func (s *Store) SyncEntries() []SyncEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SyncEntry(nil), s.data.Syncs...)
}

// UpsertSync 按 PlaylistID upsert 同步映射。
func (s *Store) UpsertSync(e SyncEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Syncs {
		if s.data.Syncs[i].PlaylistID == e.PlaylistID {
			s.data.Syncs[i] = e
			return s.saveLocked()
		}
	}
	s.data.Syncs = append(s.data.Syncs, e)
	return s.saveLocked()
}

// RemoveSync 按 PlaylistID 删除同步映射；不存在返回 nil。
func (s *Store) RemoveSync(playlistID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Syncs {
		if s.data.Syncs[i].PlaylistID == playlistID {
			s.data.Syncs = append(s.data.Syncs[:i], s.data.Syncs[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

// FindSync 按 PlaylistID 查找同步映射。
func (s *Store) FindSync(playlistID string) (SyncEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.data.Syncs {
		if e.PlaylistID == playlistID {
			return e, true
		}
	}
	return SyncEntry{}, false
}

// saveLocked 原子写盘（调用方须持锁）。
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 ytm 配置: %w", err)
	}
	if err := writeFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("写入 ytm 配置: %w", err)
	}
	return nil
}

// defaultCookiesPath 是 cookie 落盘默认路径（与 ytm.json 同目录）。
func (s *Store) defaultCookiesPath() string {
	return filepath.Join(filepath.Dir(s.path), "ytm-cookies.txt")
}

// CookieFile 确保当前登录方式对应的 cookies 文件存在并返回路径：
//   - MethodBrowser：懒导出——每次重新导出浏览器 cookie（覆盖式更新 CookiesPath）
//   - MethodCookiesFile：直接返回配置路径（校验可读）
//   - MethodPasted：文件内容为单行 Cookie header 时转成 Netscape 格式
//
// 未登录返回 ErrNoLogin。
func (s *Store) CookieFile() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.data.Login
	switch cfg.Method {
	case MethodNone:
		return "", ErrNoLogin
	case MethodCookiesFile:
		if cfg.CookiesPath == "" {
			return "", errors.New("未设置 cookies.txt 路径")
		}
		if _, err := os.Stat(cfg.CookiesPath); err != nil {
			return "", fmt.Errorf("cookies.txt 不可读: %w", err)
		}
		return cfg.CookiesPath, nil
	case MethodPasted:
		p := cfg.CookiesPath
		if p == "" {
			p = s.defaultCookiesPath()
		}
		if err := ensurePastedFile(p); err != nil {
			return "", err
		}
		return p, nil
	case MethodBrowser:
		if cfg.Browser == "" {
			return "", errors.New("未指定浏览器")
		}
		p := cfg.CookiesPath
		if p == "" {
			p = s.defaultCookiesPath()
		}
		if err := ExportBrowserCookies(cfg.Browser, p); err != nil {
			return "", err
		}
		if s.data.Login.CookiesPath != p {
			s.data.Login.CookiesPath = p
			if err := s.saveLocked(); err != nil {
				return "", err
			}
		}
		return p, nil
	default:
		return "", ErrNoLogin
	}
}

// CookieHeader 从当前登录配置派生 "name=value; ..." Cookie header
// 字符串（供 InnerTube 请求）。浏览器方式下触发懒导出；cookies 文件
// 只取 youtube 域 cookie；未登录返回 ErrNoLogin。
func (s *Store) CookieHeader() (string, error) {
	path, err := s.CookieFile()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 cookie 文件失败: %w", err)
	}
	cookies, err := ParseNetscape(data)
	if err != nil {
		return "", fmt.Errorf("解析 cookie 文件失败: %w", err)
	}
	var parts []string
	for _, c := range cookies {
		if !isYouTubeDomain(c.Domain) || c.Name == "" {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		return "", ErrNoYTCookies
	}
	return strings.Join(parts, "; "), nil
}

// ensurePastedFile 检查粘贴 cookie 文件：已是 Netscape 格式则直接可用；
// 单行 Cookie header 则转为 Netscape 写出（供 yt-dlp 读取）。
func ensurePastedFile(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("读取 cookie 文件失败: %w", err)
	}
	if looksLikeNetscape(data) {
		return nil
	}
	cookies, err := cookiesFromHeader(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	return WriteNetscape(p, cookies)
}

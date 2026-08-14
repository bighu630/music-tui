package ytm

import (
	"context"
	"net/http"
	"time"
)

// BrowserInfo 描述一个支持自动导出 cookie 的浏览器。
type BrowserInfo struct {
	Name  string // 标识（chrome/chromium/brave/edge/vivaldi/opera）
	Label string // 展示名（"Google Chrome"…）
}

// SupportedBrowsers 是浏览器支持矩阵：Linux/macOS 自研解密；
// Windows 不支持（v20 app-bound），UI 提示改用 cookies.txt。
var SupportedBrowsers = []BrowserInfo{
	{Name: "chrome", Label: "Google Chrome"},
	{Name: "chromium", Label: "Chromium"},
	{Name: "brave", Label: "Brave"},
	{Name: "edge", Label: "Microsoft Edge"},
	{Name: "vivaldi", Label: "Vivaldi"},
	{Name: "opera", Label: "Opera"},
}

// Client 是 YTM 同步客户端：登录配置（Store）+ 歌单拉取（Fetcher）+ InnerTube。
type Client struct {
	store      *Store
	fetcher    Fetcher
	httpClient *http.Client
}

// NewClient 创建 YTM 客户端。
func NewClient(store *Store, fetcher Fetcher) *Client {
	return &Client{
		store:      store,
		fetcher:    fetcher,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetHTTPClient 替换默认 HTTP 客户端（UI 测试注入离线 RoundTripper 用；
// 传 nil 恢复默认超时客户端）。仅供测试/特殊场景调用，正常使用默认值即可。
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	c.httpClient = hc
}

// ---- Store 门面（UI 编排层经 Client 读写登录/同步状态，不直接接触 Store） ----

// Login 返回当前登录配置副本。
func (c *Client) Login() LoginConfig { return c.store.Login() }

// SetLogin 保存登录配置（自动填充 UpdatedAt）。
func (c *Client) SetLogin(cfg LoginConfig) error { return c.store.SetLogin(cfg) }

// ClearLogin 清除登录配置（保留 SyncEntry 映射）。
func (c *Client) ClearLogin() error { return c.store.ClearLogin() }

// SetPastedLogin 保存粘贴的 Cookie header 文本（落盘 cookies 文件 + 配置），
// 返回实际 cookies 文件路径。
func (c *Client) SetPastedLogin(header string) (string, error) {
	return c.store.SetPastedLogin(header)
}

// SyncEntries 返回全部同步映射副本（保持插入顺序）。
func (c *Client) SyncEntries() []SyncEntry { return c.store.SyncEntries() }

// VerifyLogin 检查登录态，返回可区分错误：
// 未登录（ErrNotLoggedIn）/失效（ErrSessionInvalid）/网络错误（透传包装）。
// 登录有效返回 nil。
func (c *Client) VerifyLogin(ctx context.Context) error {
	_, err := c.ListPlaylists(ctx)
	return err
}

// ListPlaylists 枚举 YTM 全部歌单（需登录）。
func (c *Client) ListPlaylists(ctx context.Context) ([]RemotePlaylist, error) {
	it, err := c.innerTube()
	if err != nil {
		return nil, err
	}
	resp, err := it.browse(ctx, likedPlaylistsBrowseID)
	if err != nil {
		return nil, err
	}
	playlists := extractPlaylists(resp)
	// 登录判定：logged_in=0 或无歌单条目 → 未登录（契约）
	if len(playlists) == 0 || loggedInParam(resp) == "0" {
		return nil, ErrNotLoggedIn
	}
	return playlists, nil
}

// innerTube 构建带当前 cookie 的 browse 客户端。
func (c *Client) innerTube() (*innerTube, error) {
	header, err := c.store.CookieHeader()
	if err != nil {
		return nil, err
	}
	return newInnerTube(c.httpClient, header), nil
}

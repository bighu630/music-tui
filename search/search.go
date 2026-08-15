// Package search 定义搜索适配器接口与 YouTube 实现。
package search

import (
	"context"

	"music-tui/model"
)

// SearchAdapter 是音乐搜索适配器接口；未来新增来源（如网易云）
// 只需再实现一个适配器。
type SearchAdapter interface {
	// Search 按关键词搜索，返回歌曲列表；ctx 用于超时与取消。
	Search(ctx context.Context, query string) ([]model.Track, error)
	// FetchPlaylist 拉取远端歌单内容（标题 + 歌曲列表）；ctx 用于超时与取消。
	FetchPlaylist(ctx context.Context, playlistURL string, cookies CookieArgs) (model.Playlist, error)
}

// CookieArgs 传给 yt-dlp 的 cookie 参数（File 优先于 FromBrowser）。
type CookieArgs struct {
	FromBrowser string // --cookies-from-browser 值（保留备用）
	File        string // --cookies 文件路径
}

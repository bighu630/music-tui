package ytm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"music-tui/model"
	"music-tui/playlists"
	"music-tui/search"
)

// RemotePlaylist 是 YTM 远端歌单（来自 browse 响应）。
type RemotePlaylist struct {
	ID    string // browseId（PL 开头）
	Title string
	Count int // subtitle 中的数量（可能为 0）
}

// URL 返回该歌单的可拉取 URL。
func (r RemotePlaylist) URL() string {
	return "https://music.youtube.com/playlist?list=" + url.QueryEscape(r.ID)
}

// SyncResult 是一次同步的结果。
type SyncResult struct {
	Remote     RemotePlaylist
	ListName   string // 本地播放列表名（"YT: <歌单名>"）
	New        bool   // 是否新建本地列表（false = 刷新既有）
	TrackCount int
}

// Fetcher 拉取远端歌单内容（search.YouTubeAdapter 实现 FetchPlaylist）。
type Fetcher interface {
	FetchPlaylist(ctx context.Context, playlistURL string, cookies search.CookieArgs) (model.Playlist, error)
}

// ytPrefix 是同步列表的本地命名前缀。
const ytPrefix = "YT: "

// SyncAll 枚举全部歌单 → 逐个拉取去重入库；单个失败记录错误继续，
// 返回成功 results + errors.Join 汇总错误。
func (c *Client) SyncAll(ctx context.Context, pl *playlists.Store) ([]SyncResult, error) {
	remotes, err := c.ListPlaylists(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]SyncResult, 0, len(remotes))
	var errs []error
	for _, r := range remotes {
		res, err := c.syncRemote(ctx, pl, r)
		if err != nil {
			errs = append(errs, fmt.Errorf("同步「%s」失败: %w", r.Title, err))
			continue
		}
		results = append(results, res)
	}
	return results, errors.Join(errs...)
}

// SyncOne 按 playlistID 刷新单个同步列表：直接经 SyncEntry 映射构造歌单
// URL 拉取（不走枚举；URL 导入的共享歌单不在库中也能刷新）。
// SyncEntry 不存在（本地手工改名/从未同步过）→ 报非同步列表错误。
func (c *Client) SyncOne(ctx context.Context, pl *playlists.Store, playlistID string) (SyncResult, error) {
	entry, ok := c.store.FindSync(playlistID)
	if !ok {
		return SyncResult{}, errors.New("该列表不是 YT Music 同步列表")
	}
	p, err := c.store.CookieFile()
	if err != nil {
		return SyncResult{}, err
	}
	remote := RemotePlaylist{ID: entry.PlaylistID}
	fetched, err := c.fetcher.FetchPlaylist(ctx, remote.URL(), search.CookieArgs{File: p})
	if err != nil {
		return SyncResult{}, fmt.Errorf("拉取歌单失败: %w", err)
	}
	remote.Title = fetched.Title
	remote.Count = len(fetched.Tracks)
	// 刷新既有映射：ListName 保持原名（即使远端标题已改），不重新走命名冲突逻辑
	return c.applySyncNamed(pl, entry.ListName, remote, fetched.Tracks)
}

// ImportURL 导入任意歌单 URL（公开歌单无需登录；私有歌单需已登录 cookie）。
// 列表名 = "YT: <歌单标题>"；同名冲突自动加 " (2)" 后缀。
func (c *Client) ImportURL(ctx context.Context, pl *playlists.Store, playlistURL string) (SyncResult, error) {
	args := search.CookieArgs{}
	p, err := c.store.CookieFile()
	switch {
	case err == nil:
		args.File = p // 已登录则携带 cookie
	case errors.Is(err, ErrNoLogin):
		// 未配置登录：公开歌单无需 cookie，静默降级
	default:
		// 已配置但 cookie 不可用（导出/解密失败等）：上抛，避免私有歌单静默失败
		return SyncResult{}, fmt.Errorf("获取登录 cookie 失败: %w", err)
	}
	fetched, err := c.fetcher.FetchPlaylist(ctx, playlistURL, args)
	if err != nil {
		return SyncResult{}, fmt.Errorf("拉取歌单失败: %w", err)
	}
	remote := RemotePlaylist{ID: playlistIDFromURL(playlistURL), Title: fetched.Title, Count: len(fetched.Tracks)}
	if remote.ID == "" {
		remote.ID = "url:" + playlistURL // 无 list 参数时用 URL 做映射键
	}
	return c.applySync(pl, remote, fetched.Tracks)
}

// syncRemote 拉取远端歌单并入库（SyncAll 枚举路径用；须已登录）。
func (c *Client) syncRemote(ctx context.Context, pl *playlists.Store, remote RemotePlaylist) (SyncResult, error) {
	p, err := c.store.CookieFile()
	if err != nil {
		return SyncResult{}, err
	}
	fetched, err := c.fetcher.FetchPlaylist(ctx, remote.URL(), search.CookieArgs{File: p})
	if err != nil {
		return SyncResult{}, fmt.Errorf("拉取歌单失败: %w", err)
	}
	return c.applySync(pl, remote, fetched.Tracks)
}

// applySync 去重 + 命名冲突解决 + 入库（新建/刷新）+ upsert SyncEntry。
func (c *Client) applySync(pl *playlists.Store, remote RemotePlaylist, tracks []model.Track) (SyncResult, error) {
	name, err := c.resolveListName(pl, remote)
	if err != nil {
		return SyncResult{}, err
	}
	return c.applySyncNamed(pl, name, remote, tracks)
}

// applySyncNamed 按已确定的本地列表名入库（新建/刷新）+ upsert SyncEntry。
// 列表已存在 → 整列表替换（保留 CreatedAt）；不存在（被删/改名）→ 重建。
func (c *Client) applySyncNamed(pl *playlists.Store, name string, remote RemotePlaylist, tracks []model.Track) (SyncResult, error) {
	deduped := dedupTracks(tracks)
	refresh := false
	for _, l := range pl.Lists() {
		if l.Name == name {
			refresh = true
			break
		}
	}
	if refresh {
		// 刷新 = 整列表替换：从尾到头逐个移除（保留列表 CreatedAt）
		for i := len(pl.Tracks(name)) - 1; i >= 0; i-- {
			if err := pl.RemoveTrack(name, i); err != nil {
				return SyncResult{}, err
			}
		}
	} else {
		if _, err := pl.Create(name); err != nil {
			return SyncResult{}, err
		}
	}
	for _, t := range deduped {
		if err := pl.AddTrack(name, t); err != nil {
			return SyncResult{}, err
		}
	}
	entry := SyncEntry{PlaylistID: remote.ID, ListName: name, SyncedAt: time.Now(), Count: len(deduped)}
	if err := c.store.UpsertSync(entry); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Remote: remote, ListName: name, New: !refresh, TrackCount: len(deduped)}, nil
}

// resolveListName 决定本地列表名（新建/刷新语义由 applySyncNamed 按列表存在性判断）：
//   - 无同名本地列表 → 用 "YT: <标题>"（新建；即使已有 SyncEntry 映射，
//     说明本地列表被删过，重新创建）
//   - 同名本地列表且 SyncEntry 映射到本歌单 → 用该名（刷新）
//   - 同名但非本歌单映射（手动列表或其他歌单）→ 追加 " (2)"/" (3)" 递增
func (c *Client) resolveListName(pl *playlists.Store, remote RemotePlaylist) (string, error) {
	desired := ytPrefix + remote.Title
	has := func(n string) bool {
		for _, l := range pl.Lists() {
			if l.Name == n {
				return true
			}
		}
		return false
	}
	if !has(desired) {
		return desired, nil
	}
	if e, ok := c.store.FindSync(remote.ID); ok && e.ListName == desired {
		return desired, nil
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)", desired, i)
		if !has(cand) {
			return cand, nil
		}
	}
}

// dedupTracks 按 videoId 去重（保留首次出现顺序）；空 ID 条目跳过。
func dedupTracks(tracks []model.Track) []model.Track {
	seen := make(map[string]bool, len(tracks))
	out := make([]model.Track, 0, len(tracks))
	for _, t := range tracks {
		if t.ID == "" || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	return out
}

// playlistIDFromURL 从歌单 URL 提取 list 参数；无则返回 ""。
func playlistIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get("list")
}

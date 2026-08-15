package ytm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"music-tui/model"
	"music-tui/playlists"
	"music-tui/search"
)

// fakeFetcher 记录调用并返回预置歌单。
type fakeFetcher struct {
	mu        sync.Mutex
	playlists map[string]model.Playlist
	err       error            // 全局错误（优先级低于 errs）
	errs      map[string]error // 按 URL 的错误
	urls      []string
	args      []search.CookieArgs
}

func (f *fakeFetcher) FetchPlaylist(ctx context.Context, playlistURL string, cookies search.CookieArgs) (model.Playlist, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, playlistURL)
	f.args = append(f.args, cookies)
	if e, ok := f.errs[playlistURL]; ok {
		return model.Playlist{}, e
	}
	if f.err != nil {
		return model.Playlist{}, f.err
	}
	p, ok := f.playlists[playlistURL]
	if !ok {
		return model.Playlist{}, fmt.Errorf("未预置 URL %s", playlistURL)
	}
	return p, nil
}

func testTrack(id string) model.Track {
	return model.Track{
		ID:       id,
		Title:    "歌曲 " + id,
		Artist:   "歌手",
		Duration: 180,
		URL:      "https://music.youtube.com/watch?v=" + id,
		Source:   "youtube",
	}
}

// syncTestEnv 组装完整测试环境：store（已登录）+ 假浏览器配置
// + browse httptest server + fakeFetcher。
type syncTestEnv struct {
	store   *Store
	pls     *playlists.Store
	fetcher *fakeFetcher
	client  *Client
	srv     *httptest.Server
}

// newSyncEnv 构造环境；browseResp 为 ListPlaylists 的响应（nil 时用 gridFixture）。
func newSyncEnv(t *testing.T, browseResp string) *syncTestEnv {
	t.Helper()
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=sync-sap; __Secure-3PAPISID=3p")

	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{playlists: map[string]model.Playlist{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if browseResp == "" {
			_, _ = w.Write([]byte(gridFixture))
		} else {
			_, _ = w.Write([]byte(browseResp))
		}
	}))
	t.Cleanup(srv.Close)
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	t.Cleanup(func() { ytmBrowseURL = old })

	client := &Client{store: s, fetcher: fetcher, httpClient: srv.Client()}
	return &syncTestEnv{store: s, pls: pls, fetcher: fetcher, client: client, srv: srv}
}

// trackURL 生成与 remote.URL() 一致的歌单 URL。
func trackURL(id string) string {
	return "https://music.youtube.com/playlist?list=" + id
}

func TestSyncAllCreatesAndDedups(t *testing.T) {
	env := newSyncEnv(t, "")
	// 三个远端歌单；第一个返回重复条目（去重）
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "我的最爱",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2"), testTrack("v1"), testTrack("v3")},
	}
	env.fetcher.playlists[trackURL("PLBBB")] = model.Playlist{
		ID: "PLBBB", Title: "通勤歌单",
		Tracks: []model.Track{testTrack("v4")},
	}
	env.fetcher.playlists[trackURL("PLCCC")] = model.Playlist{
		ID: "PLCCC", Title: "无 run 歌单",
		Tracks: []model.Track{},
	}

	results, err := env.client.SyncAll(context.Background(), env.pls)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	// 全部新建
	if !results[0].New || !results[1].New || !results[2].New {
		t.Errorf("应全部新建: %+v", results)
	}
	if results[0].TrackCount != 3 {
		t.Errorf("去重后 TrackCount = %d, want 3", results[0].TrackCount)
	}
	if results[0].ListName != "YT: 我的最爱" || results[1].ListName != "YT: 通勤歌单" || results[2].ListName != "YT: 无 run 歌单" {
		t.Errorf("列表名 = %q / %q / %q", results[0].ListName, results[1].ListName, results[2].ListName)
	}

	// 本地列表内容：保序去重
	tracks := env.pls.Tracks("YT: 我的最爱")
	if len(tracks) != 3 {
		t.Fatalf("本地曲目数 = %d, want 3", len(tracks))
	}
	for i, want := range []string{"v1", "v2", "v3"} {
		if tracks[i].ID != want {
			t.Errorf("tracks[%d].ID = %s, want %s", i, tracks[i].ID, want)
		}
	}

	// SyncEntry 映射
	e, ok := env.store.FindSync("PLAAA")
	if !ok || e.ListName != "YT: 我的最爱" || e.Count != 3 {
		t.Errorf("SyncEntry = %+v, %v", e, ok)
	}
	if len(env.store.SyncEntries()) != 3 {
		t.Errorf("SyncEntries = %d, want 3", len(env.store.SyncEntries()))
	}
}

func TestSyncAllFetcherUsesCookiesFile(t *testing.T) {
	env := newSyncEnv(t, "")
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{ID: "PLAAA", Title: "我的最爱", Tracks: []model.Track{testTrack("v1")}}
	env.fetcher.playlists[trackURL("PLBBB")] = model.Playlist{ID: "PLBBB", Title: "通勤歌单", Tracks: []model.Track{testTrack("v4")}}
	env.fetcher.playlists[trackURL("PLCCC")] = model.Playlist{ID: "PLCCC", Title: "无 run 歌单", Tracks: []model.Track{}}
	if _, err := env.client.SyncAll(context.Background(), env.pls); err != nil {
		t.Fatal(err)
	}
	if len(env.fetcher.args) != 3 || env.fetcher.args[0].File == "" {
		t.Fatalf("应携带 cookie 文件: %+v", env.fetcher.args)
	}
	for _, a := range env.fetcher.args {
		if _, err := os.Stat(a.File); err != nil {
			t.Errorf("cookie 文件不存在: %v", err)
		}
	}
}

func TestSyncAllPartialFailureContinues(t *testing.T) {
	env := newSyncEnv(t, "")
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{ID: "PLAAA", Title: "我的最爱", Tracks: []model.Track{testTrack("v1")}}
	env.fetcher.playlists[trackURL("PLBBB")] = model.Playlist{ID: "PLBBB", Title: "通勤歌单", Tracks: []model.Track{testTrack("v4")}}
	env.fetcher.playlists[trackURL("PLCCC")] = model.Playlist{ID: "PLCCC", Title: "无 run 歌单", Tracks: []model.Track{}}
	// 仅 PLBBB 拉取失败
	env.fetcher.errs = map[string]error{trackURL("PLBBB"): errors.New("yt-dlp 网络错误")}

	results, err := env.client.SyncAll(context.Background(), env.pls)
	if err == nil {
		t.Fatal("部分失败应汇总错误")
	}
	if len(results) != 2 {
		t.Fatalf("成功结果 = %d, want 2（失败应跳过继续）", len(results))
	}
	if results[0].ListName != "YT: 我的最爱" {
		t.Errorf("成功列表 = %q", results[0].ListName)
	}
	if !strings.Contains(err.Error(), "网络错误") {
		t.Errorf("汇总错误应包含原因: %v", err)
	}
	// 只有成功的两个歌单创建了本地列表（PLBBB 失败无列表）
	lists := env.pls.Lists()
	if len(lists) != 2 {
		t.Fatalf("本地列表 = %d, want 2", len(lists))
	}
	for _, l := range lists {
		if l.Name == "YT: 通勤歌单" {
			t.Error("失败歌单不应创建本地列表")
		}
	}
}

func TestSyncOneRefreshesExisting(t *testing.T) {
	env := newSyncEnv(t, "")
	// 预置：已同步过的列表（旧内容）
	if _, err := env.pls.Create("YT: 我的最爱"); err != nil {
		t.Fatal(err)
	}
	if err := env.pls.AddTrack("YT: 我的最爱", testTrack("old1")); err != nil {
		t.Fatal(err)
	}
	if err := env.pls.AddTrack("YT: 我的最爱", testTrack("old2")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertSync(SyncEntry{PlaylistID: "PLAAA", ListName: "YT: 我的最爱", Count: 2}); err != nil {
		t.Fatal(err)
	}
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "我的最爱",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}

	res, err := env.client.SyncOne(context.Background(), env.pls, "PLAAA")
	if err != nil {
		t.Fatal(err)
	}
	if res.New {
		t.Error("已有同步映射应刷新而非新建")
	}
	if res.ListName != "YT: 我的最爱" || res.TrackCount != 2 {
		t.Errorf("res = %+v", res)
	}
	// 旧内容被整体替换
	tracks := env.pls.Tracks("YT: 我的最爱")
	if len(tracks) != 2 || tracks[0].ID != "v1" || tracks[1].ID != "v2" {
		t.Errorf("刷新后 = %+v", tracks)
	}
	if len(env.pls.Lists()) != 1 {
		t.Errorf("刷新不应新建列表")
	}
	// CreatedAt 保留
	if e, _ := env.store.FindSync("PLAAA"); e.Count != 2 {
		t.Errorf("SyncEntry 未更新: %+v", e)
	}
}

func TestSyncOneNotFound(t *testing.T) {
	env := newSyncEnv(t, "")
	_, err := env.client.SyncOne(context.Background(), env.pls, "PLNOPE")
	if err == nil || !strings.Contains(err.Error(), "该列表不是 YT Music 同步列表") {
		t.Errorf("SyncOne 无映射应报非同步列表错误, got %v", err)
	}
}

// M1 回归：URL 导入的共享歌单（不在库枚举中）也能用 SyncOne 直拉刷新。
// 旧实现走 ListPlaylists 枚举匹配，导入歌单不在库中 → 必然失败。
func TestSyncOneRefreshesImportedPlaylist(t *testing.T) {
	env := newSyncEnv(t, "") // browse 枚举只有 PLAAA/PLBBB/PLCCC
	// 预置：已导入的公开歌单（SyncEntry 存在；本地列表有旧内容）
	if _, err := env.pls.Create("YT: 导入歌单"); err != nil {
		t.Fatal(err)
	}
	if err := env.pls.AddTrack("YT: 导入歌单", testTrack("old")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertSync(SyncEntry{PlaylistID: "PLIMPORT", ListName: "YT: 导入歌单", Count: 1}); err != nil {
		t.Fatal(err)
	}
	env.fetcher.playlists[trackURL("PLIMPORT")] = model.Playlist{
		ID: "PLIMPORT", Title: "导入歌单",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}

	res, err := env.client.SyncOne(context.Background(), env.pls, "PLIMPORT")
	if err != nil {
		t.Fatalf("导入歌单直拉刷新应成功: %v", err)
	}
	if res.New {
		t.Error("已有映射应刷新而非新建")
	}
	if res.ListName != "YT: 导入歌单" || res.TrackCount != 2 {
		t.Errorf("res = %+v", res)
	}
	// 内容整体替换；未走枚举（browse 请求未发生——断言 fetcher 只收到直拉 URL）
	tracks := env.pls.Tracks("YT: 导入歌单")
	if len(tracks) != 2 || tracks[0].ID != "v1" || tracks[1].ID != "v2" {
		t.Errorf("刷新后 = %+v", tracks)
	}
	if len(env.fetcher.urls) != 1 || env.fetcher.urls[0] != trackURL("PLIMPORT") {
		t.Errorf("fetcher 应只收到直拉 URL: %v", env.fetcher.urls)
	}
	if e, _ := env.store.FindSync("PLIMPORT"); e.ListName != "YT: 导入歌单" || e.Count != 2 {
		t.Errorf("SyncEntry 应保持原名并更新计数: %+v", e)
	}
}

// 跟进项 A：无 list 参数的导入（url: 前缀映射，如频道 URL）刷新时必须直接用
// 原始 URL 拉取，不得经 playlist?list=url%3A... 构造垃圾 URL。
func TestSyncOneRefreshesURLPrefixedEntry(t *testing.T) {
	env := newSyncEnv(t, "") // browse 枚举只有 PLAAA/PLBBB/PLCCC
	rawURL := "https://music.youtube.com/channel/xxx"
	// 预置：已导入的无 list 参数歌单（映射键为 url: 前缀）
	if _, err := env.pls.Create("YT: 频道歌单"); err != nil {
		t.Fatal(err)
	}
	if err := env.pls.AddTrack("YT: 频道歌单", testTrack("old")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertSync(SyncEntry{PlaylistID: "url:" + rawURL, ListName: "YT: 频道歌单", Count: 1}); err != nil {
		t.Fatal(err)
	}
	// fetcher 只预置原始 URL：若 SyncOne 构造出垃圾 URL 会命中“未预置 URL”错误
	env.fetcher.playlists[rawURL] = model.Playlist{
		ID: "url:" + rawURL, Title: "频道歌单",
		Tracks: []model.Track{testTrack("v1"), testTrack("v2")},
	}

	res, err := env.client.SyncOne(context.Background(), env.pls, "url:"+rawURL)
	if err != nil {
		t.Fatalf("url: 前缀映射刷新应成功: %v", err)
	}
	if res.New {
		t.Error("已有映射应刷新而非新建")
	}
	if res.ListName != "YT: 频道歌单" || res.TrackCount != 2 {
		t.Errorf("res = %+v", res)
	}
	// 断言 fetcher 收到原始 URL（非 playlist?list=url%3A... 垃圾 URL）
	if len(env.fetcher.urls) != 1 || env.fetcher.urls[0] != rawURL {
		t.Errorf("fetcher 应收到原始 URL %q, got %v", rawURL, env.fetcher.urls)
	}
	// 内容整体替换
	tracks := env.pls.Tracks("YT: 频道歌单")
	if len(tracks) != 2 || tracks[0].ID != "v1" || tracks[1].ID != "v2" {
		t.Errorf("刷新后 = %+v", tracks)
	}
	// SyncEntry 保持 url: 映射键并更新计数
	if e, _ := env.store.FindSync("url:" + rawURL); e.ListName != "YT: 频道歌单" || e.Count != 2 {
		t.Errorf("SyncEntry 应保持 url: 映射键并更新计数: %+v", e)
	}
}

// SyncOne 语义：远端标题变更时刷新仍写入原映射列表（ListName 保持原名），
// 不因标题变化新建列表。
func TestSyncOneKeepsListNameWhenRemoteTitleChanged(t *testing.T) {
	env := newSyncEnv(t, "")
	if _, err := env.pls.Create("YT: 旧标题"); err != nil {
		t.Fatal(err)
	}
	if err := env.pls.AddTrack("YT: 旧标题", testTrack("old")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertSync(SyncEntry{PlaylistID: "PLAAA", ListName: "YT: 旧标题", Count: 1}); err != nil {
		t.Fatal(err)
	}
	// 远端标题已改为新标题
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{
		ID: "PLAAA", Title: "新标题",
		Tracks: []model.Track{testTrack("v1")},
	}

	res, err := env.client.SyncOne(context.Background(), env.pls, "PLAAA")
	if err != nil {
		t.Fatal(err)
	}
	if res.ListName != "YT: 旧标题" {
		t.Errorf("ListName = %q, want 保持原名 YT: 旧标题", res.ListName)
	}
	if len(env.pls.Lists()) != 1 {
		t.Errorf("不应新建列表: %+v", env.pls.Lists())
	}
	if got := env.pls.Tracks("YT: 旧标题"); len(got) != 1 || got[0].ID != "v1" {
		t.Errorf("刷新后曲目 = %+v", got)
	}
}

func TestNameConflictAddsSuffix(t *testing.T) {
	env := newSyncEnv(t, "")
	// 本地已有同名列表但无 SyncEntry 映射 → 追加后缀（走 SyncAll 枚举路径）
	if _, err := env.pls.Create("YT: 我的最爱"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pls.Create("YT: 我的最爱 (2)"); err != nil {
		t.Fatal(err)
	}
	env.fetcher.playlists[trackURL("PLAAA")] = model.Playlist{ID: "PLAAA", Title: "我的最爱", Tracks: []model.Track{testTrack("v1")}}
	env.fetcher.playlists[trackURL("PLBBB")] = model.Playlist{ID: "PLBBB", Title: "通勤歌单", Tracks: []model.Track{}}
	env.fetcher.playlists[trackURL("PLCCC")] = model.Playlist{ID: "PLCCC", Title: "无 run 歌单", Tracks: []model.Track{}}

	results, err := env.client.SyncAll(context.Background(), env.pls)
	if err != nil {
		t.Fatal(err)
	}
	var res SyncResult
	for _, r := range results {
		if r.Remote.ID == "PLAAA" {
			res = r
		}
	}
	if res.ListName != "YT: 我的最爱 (3)" {
		t.Errorf("冲突应追加递增后缀, got %q", res.ListName)
	}
	if !res.New {
		t.Error("加后缀后应为新建")
	}
	if len(env.pls.Lists()) != 5 {
		t.Errorf("列表数 = %d, want 5（2 冲突 + 3 新建）", len(env.pls.Lists()))
	}
	if e, ok := env.store.FindSync("PLAAA"); !ok || e.ListName != "YT: 我的最爱 (3)" {
		t.Errorf("SyncEntry 应指向带后缀列表: %+v, %v", e, ok)
	}
}

func TestSyncAllNotLoggedIn(t *testing.T) {
	s, _ := newTestStore(t)
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{store: s, fetcher: &fakeFetcher{}, httpClient: &http.Client{}}
	_, err = client.SyncAll(context.Background(), pls)
	if !errors.Is(err, ErrNoLogin) {
		t.Errorf("未登录 SyncAll 应返回 ErrNoLogin, got %v", err)
	}
}

func TestImportURLNoLoginPublic(t *testing.T) {
	s, _ := newTestStore(t) // 未登录
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{playlists: map[string]model.Playlist{
		"https://music.youtube.com/playlist?list=PLIMPORT": {
			ID: "PLIMPORT", Title: "公开歌单",
			Tracks: []model.Track{testTrack("i1"), testTrack("i2"), testTrack("i1")},
		},
	}}
	client := &Client{store: s, fetcher: fetcher, httpClient: &http.Client{}}

	res, err := client.ImportURL(context.Background(), pls, "https://music.youtube.com/playlist?list=PLIMPORT")
	if err != nil {
		t.Fatal(err)
	}
	if !res.New || res.ListName != "YT: 公开歌单" || res.TrackCount != 2 {
		t.Errorf("res = %+v", res)
	}
	// 未登录 → 不携带 cookie 参数
	if fetcher.args[0].File != "" {
		t.Errorf("公开歌单不应带 cookie: %+v", fetcher.args[0])
	}
	// SyncEntry 以 list 参数为键
	if e, ok := s.FindSync("PLIMPORT"); !ok || e.Count != 2 {
		t.Errorf("SyncEntry = %+v, %v", e, ok)
	}
	// 本地列表去重保序
	tracks := pls.Tracks("YT: 公开歌单")
	if len(tracks) != 2 || tracks[0].ID != "i1" || tracks[1].ID != "i2" {
		t.Errorf("导入列表 = %+v", tracks)
	}
}

func TestImportURLReimportRefreshes(t *testing.T) {
	s, _ := newTestStore(t)
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{playlists: map[string]model.Playlist{
		"https://music.youtube.com/playlist?list=PLIMPORT": {
			ID: "PLIMPORT", Title: "公开歌单",
			Tracks: []model.Track{testTrack("i1")},
		},
	}}
	client := &Client{store: s, fetcher: fetcher, httpClient: &http.Client{}}
	if _, err := client.ImportURL(context.Background(), pls, "https://music.youtube.com/playlist?list=PLIMPORT"); err != nil {
		t.Fatal(err)
	}
	// 第二次导入 → 刷新既有列表（New=false）
	res, err := client.ImportURL(context.Background(), pls, "https://music.youtube.com/playlist?list=PLIMPORT")
	if err != nil {
		t.Fatal(err)
	}
	if res.New {
		t.Error("重复导入应刷新而非新建")
	}
	if len(pls.Lists()) != 1 {
		t.Errorf("重复导入不应产生新列表, n = %d", len(pls.Lists()))
	}
}

func TestImportURLWithoutListParam(t *testing.T) {
	s, _ := newTestStore(t)
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	url := "https://youtube.com/playlist?list=" // 无 list 参数
	fetcher := &fakeFetcher{playlists: map[string]model.Playlist{
		url: {Title: "无参数歌单", Tracks: []model.Track{testTrack("x1")}},
	}}
	client := &Client{store: s, fetcher: fetcher, httpClient: &http.Client{}}
	res, err := client.ImportURL(context.Background(), pls, url)
	if err != nil {
		t.Fatal(err)
	}
	// 映射键退化为 url: 前缀
	if e, ok := s.FindSync("url:" + url); !ok || e.ListName != res.ListName {
		t.Errorf("无 list 参数时应用 URL 作映射键: %+v, %v", e, ok)
	}
}

func TestImportURLCookiesWhenLoggedIn(t *testing.T) {
	env := newSyncEnv(t, "") // 已登录
	url := "https://music.youtube.com/playlist?list=PLPRIVATE"
	env.fetcher.playlists[url] = model.Playlist{ID: "PLPRIVATE", Title: "私有歌单", Tracks: []model.Track{testTrack("p1")}}
	if _, err := env.client.ImportURL(context.Background(), env.pls, url); err != nil {
		t.Fatal(err)
	}
	if len(env.fetcher.args) != 1 || env.fetcher.args[0].File == "" {
		t.Errorf("已登录导入应携带 cookie 文件: %+v", env.fetcher.args)
	}
}

// m5：已配置登录但 cookie 导出失败（浏览器未安装等）→ 上抛，不静默降级。
func TestImportURLCookieExportFailurePropagates(t *testing.T) {
	fakeBrowserHome(t) // 空配置目录：浏览器导出必然失败
	s, _ := newTestStore(t)
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	// 配置了浏览器方式但本机没有该浏览器配置目录 → CookieFile 导出失败
	if err := s.SetLogin(LoginConfig{Method: MethodBrowser, Browser: "chrome"}); err != nil {
		t.Fatal(err)
	}
	client := &Client{store: s, fetcher: &fakeFetcher{}, httpClient: &http.Client{}}
	_, err = client.ImportURL(context.Background(), pls, "https://music.youtube.com/playlist?list=PLX")
	if err == nil || !strings.Contains(err.Error(), "获取登录 cookie 失败") {
		t.Errorf("cookie 导出失败应上抛, got %v", err)
	}
}

// m5：MethodPasted 已配置但落盘文件缺失 → 上抛（非 ErrNoLogin 不静默降级）。
func TestImportURLCookieFileMissingPropagates(t *testing.T) {
	s, _ := newTestStore(t)
	plsPath := filepath.Join(t.TempDir(), "playlists.json")
	pls, err := playlists.NewStore(plsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogin(LoginConfig{Method: MethodPasted, CookiesPath: filepath.Join(t.TempDir(), "nope.txt")}); err != nil {
		t.Fatal(err)
	}
	client := &Client{store: s, fetcher: &fakeFetcher{}, httpClient: &http.Client{}}
	_, err = client.ImportURL(context.Background(), pls, "https://music.youtube.com/playlist?list=PLX")
	if err == nil || !strings.Contains(err.Error(), "获取登录 cookie 失败") {
		t.Errorf("cookie 文件缺失应上抛, got %v", err)
	}
}

func TestDedupTracks(t *testing.T) {
	in := []model.Track{testTrack("a"), {}, testTrack("b"), testTrack("a"), testTrack("c"), {}}
	got := dedupTracks(in)
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Errorf("dedupTracks = %+v", got)
	}
}

func TestPlaylistIDFromURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://music.youtube.com/playlist?list=PLABC&si=x", "PLABC"},
		{"https://youtube.com/playlist?list=PLABC", "PLABC"},
		{"https://youtube.com/watch?v=xxx", ""},
		{"://bad url", ""},
	}
	for _, c := range cases {
		if got := playlistIDFromURL(c.url); got != c.want {
			t.Errorf("playlistIDFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestRemotePlaylistURL(t *testing.T) {
	r := RemotePlaylist{ID: "PL_AB-12"}
	if got := r.URL(); got != "https://music.youtube.com/playlist?list=PL_AB-12" {
		t.Errorf("URL = %q", got)
	}
}

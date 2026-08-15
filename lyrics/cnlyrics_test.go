package lyrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ── 网易云客户端 ─────────────────────────────────────────────────

func TestNeteaseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search/get" {
			t.Errorf("path = %q, want /api/search/get", r.URL.Path)
		}
		if got := r.URL.Query().Get("s"); got != "病态 薛之谦" {
			t.Errorf("s = %q, want 病态 薛之谦", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"result":{"songs":[
			{"id":1374056687,"name":"病态","artists":[{"name":"薛之谦"}],"duration":280000},
			{"id":999,"name":"病态 (Live)","artists":[{"name":"薛之谦"}],"duration":300000}
		]}}`))
	}))
	defer server.Close()

	c := NewNeteaseClientWithBaseURL(server.URL)
	songs, err := c.Search(context.Background(), "病态", "薛之谦")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(songs) != 2 {
		t.Fatalf("songs = %d, want 2", len(songs))
	}
	if songs[0].ID != "1374056687" || songs[0].Title != "病态" || songs[0].Artist != "薛之谦" {
		t.Errorf("songs[0] = %+v", songs[0])
	}
	if songs[0].Duration != 280.0 {
		t.Errorf("Duration = %v, want 280（毫秒转秒）", songs[0].Duration)
	}
}

func TestNeteaseLyric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/song/lyric" {
			t.Errorf("path = %q, want /api/song/lyric", r.URL.Path)
		}
		if got := r.URL.Query().Get("id"); got != "1374056687" {
			t.Errorf("id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"lrc":{"lyric":"[00:31.032]这星球像一颗胚胎\n[00:33.897]将我们温柔地覆盖\n"}}`))
	}))
	defer server.Close()

	c := NewNeteaseClientWithBaseURL(server.URL)
	ly, err := c.Lyric(context.Background(), "1374056687")
	if err != nil {
		t.Fatalf("Lyric: %v", err)
	}
	if len(ly.Lines) != 2 || ly.Lines[1].Text != "将我们温柔地覆盖" {
		t.Errorf("Lines = %+v", ly.Lines)
	}
}

func TestNeteaseLyricEmptyRejected(t *testing.T) {
	// 歌词为空（uncollected 等）：视为未命中
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"lrc":{"lyric":""}}`))
	}))
	defer server.Close()

	c := NewNeteaseClientWithBaseURL(server.URL)
	if ly, err := c.Lyric(context.Background(), "1"); err != nil || ly != nil {
		t.Errorf("空歌词 = %v, %v, want nil", ly, err)
	}
}

func TestNeteaseLyricPlainRejected(t *testing.T) {
	// 纯文本（无时间轴）歌词：sync-only 拒绝
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"lrc":{"lyric":"这星球像一颗胚胎\n没有时间轴\n"}}`))
	}))
	defer server.Close()

	c := NewNeteaseClientWithBaseURL(server.URL)
	if ly, err := c.Lyric(context.Background(), "1"); err != nil || ly != nil {
		t.Errorf("纯文本歌词 = %v, %v, want nil（sync-only）", ly, err)
	}
}

func TestNeteaseSearchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	c := NewNeteaseClientWithBaseURL(server.URL)
	if _, err := c.Search(context.Background(), "病态", "薛之谦"); err == nil {
		t.Fatal("Search(403) = nil error, want error")
	}
}

// ── QQ 音乐客户端 ────────────────────────────────────────────────

func TestQQSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/soso/fcgi-bin/client_search_cp" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("w"); got != "病态 薛之谦" {
			t.Errorf("w = %q", got)
		}
		if got := r.Header.Get("Referer"); got != "https://y.qq.com/" {
			t.Errorf("Referer = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"song":{"list":[
			{"songmid":"001HKVU52wokMA","songname":"病态","singer":[{"name":"薛之谦"}],"interval":279},
			{"songmid":"002ABC","songname":"病态 (Live)","singer":[{"name":"薛之谦"}],"interval":300}
		]}}}`))
	}))
	defer server.Close()

	c := NewQQMusicClientWithBaseURL(server.URL)
	songs, err := c.Search(context.Background(), "病态", "薛之谦")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(songs) != 2 {
		t.Fatalf("songs = %d, want 2", len(songs))
	}
	if songs[0].ID != "001HKVU52wokMA" || songs[0].Title != "病态" || songs[0].Artist != "薛之谦" {
		t.Errorf("songs[0] = %+v", songs[0])
	}
	if songs[0].Duration != 279.0 {
		t.Errorf("Duration = %v, want 279（interval 秒）", songs[0].Duration)
	}
}

func TestQQLyric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lyric/fcgi-bin/fcg_query_lyric_new.fcg" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("songmid"); got != "001HKVU52wokMA" {
			t.Errorf("songmid = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"lyric":"[00:00.00]病态 - 薛之谦\n[00:10.31]词：薛之谦\n[00:33.99]这星球像一颗胚胎\n"}`))
	}))
	defer server.Close()

	c := NewQQMusicClientWithBaseURL(server.URL)
	ly, err := c.Lyric(context.Background(), "001HKVU52wokMA")
	if err != nil {
		t.Fatalf("Lyric: %v", err)
	}
	if len(ly.Lines) != 3 || ly.Lines[2].Text != "这星球像一颗胚胎" {
		t.Errorf("Lines = %+v", ly.Lines)
	}
}

func TestQQSearchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	c := NewQQMusicClientWithBaseURL(server.URL)
	if _, err := c.Search(context.Background(), "病态", "薛之谦"); err == nil {
		t.Fatal("Search(403) = nil error, want error")
	}
}

// TestQQSearchURLShape 搜索请求 URL 形状（format=json + 关键词转义）。
func TestQQSearchURLShape(t *testing.T) {
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"song":{"list":[]}}}`))
	}))
	defer server.Close()

	c := NewQQMusicClientWithBaseURL(server.URL)
	_, _ = c.Search(context.Background(), "病态 薛之谦", "")
	if !strings.Contains(gotURL, "format=json") {
		t.Errorf("搜索 URL 缺 format=json: %q", gotURL)
	}
	if dec, err := url.QueryUnescape(gotURL); err != nil || !strings.Contains(dec, "w=病态 薛之谦") {
		t.Errorf("搜索 URL 关键词 = %q", gotURL)
	}
}

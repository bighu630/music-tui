package ytm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pastedLogin 配置一个 MethodPasted 登录的 store（header 落盘 ytm-cookies.txt）。
func pastedLogin(t *testing.T, s *Store, header string) {
	t.Helper()
	p := filepath.Join(filepath.Dir(s.path), "ytm-cookies.txt")
	if err := os.WriteFile(p, []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogin(LoginConfig{Method: MethodPasted, CookiesPath: p}); err != nil {
		t.Fatal(err)
	}
}

// 完整结构 fixture：contents→singleColumnBrowseResultsRenderer→tabs→
// tabRenderer→content→sectionListRenderer→contents→itemSectionRenderer→
// contents→gridRenderer→items→musicTwoRowItemRenderer。
const gridFixture = `{
  "contents": {
    "singleColumnBrowseResultsRenderer": {
      "tabs": [{
        "tabRenderer": {
          "selected": true,
          "content": {
            "sectionListRenderer": {
              "contents": [{
                "itemSectionRenderer": {
                  "contents": [{
                    "gridRenderer": {
                      "items": [
                        {"musicTwoRowItemRenderer": {
                          "title": {"runs": [{"text": "我的最爱"}]},
                          "subtitle": {"runs": [{"text": "5 首"}]},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLAAA"}}
                        }},
                        {"musicTwoRowItemRenderer": {
                          "title": {"runs": [{"text": "通勤歌单"}]},
                          "subtitle": {"runs": [{"text": "通勤歌单"}, {"text": " • "}, {"text": "12 首"}]},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLBBB"}}
                        }},
                        {"musicTwoRowItemRenderer": {
                          "title": {"simpleText": "无 run 歌单"},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLCCC"}}
                        }}
                      ]
                    }
                  }]
                }
              }]
            }
          }
        }
      }]
    }
  },
  "serviceTrackingParams": [{"service": "GFEEDBACK", "params": [{"key": "logged_in", "value": "1"}]}]
}`

const loggedOutFixture = `{
  "contents": {"singleColumnBrowseResultsRenderer": {"tabs": []}},
  "serviceTrackingParams": [{"service": "GFEEDBACK", "params": [{"key": "logged_in", "value": "0"}]}]
}`

// 简化结构 fixture：gridRenderer 直接挂在 sectionListRenderer.contents 下
// （无 itemSectionRenderer 层），验证容错递归。
const flatGridFixture = `{
  "contents": {
    "singleColumnBrowseResultsRenderer": {
      "tabs": [{
        "tabRenderer": {
          "content": {
            "sectionListRenderer": {
              "contents": [{
                "gridRenderer": {
                  "items": [
                    {"musicTwoRowItemRenderer": {
                      "title": {"runs": [{"text": "扁平结构歌单"}]},
                      "subtitle": {"runs": [{"text": "3 首"}]},
                      "navigationEndpoint": {"browseEndpoint": {"browseId": "PLFLAT"}}
                    }}
                  ]
                }
              }]
            }
          }
        }
      }]
    }
  }
}`

// newTestClient 构造指向 httptest server 的 Client（store 已登录）。
func newTestClient(t *testing.T, store *Store, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{store: store, httpClient: srv.Client()}
}

func TestBrowseRequestHeadersAndBody(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap; __Secure-3PAPISID=3p-value; SID=zzz")
	clientVersionRe := regexp.MustCompile(`^1\.\d{8}\.01\.00$`)

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Cookie"); got != "SAPISID=test-sap; __Secure-3PAPISID=3p-value; SID=zzz" {
			t.Errorf("Cookie header = %q", got)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "SAPISIDHASH ") || !regexp.MustCompile(`^SAPISIDHASH \d+_[0-9a-f]{40}$`).MatchString(auth) {
			t.Errorf("Authorization = %q", auth)
		}
		for h, want := range map[string]string{
			"Origin":          "https://music.youtube.com",
			"X-Origin":        "https://music.youtube.com",
			"X-Goog-AuthUser": "0",
			"Referer":         "https://music.youtube.com/",
			"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			"Content-Type":    "application/json",
		} {
			if got := r.Header.Get(h); got != want {
				t.Errorf("header %s = %q, want %q", h, got, want)
			}
		}
		if got := r.URL.RequestURI(); got != "/youtubei/v1/browse?prettyPrint=false" {
			t.Errorf("URI = %q", got)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("body 非法 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(gridFixture))
	}))
	defer srv.Close()

	old := ytmBrowseURL
	ytmBrowseURL = srv.URL + "/youtubei/v1/browse?prettyPrint=false"
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	playlists, err := c.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 3 {
		t.Fatalf("歌单数 = %d, want 3", len(playlists))
	}
	browseID, _ := gotBody["browseId"].(string)
	if browseID != "FEmusic_liked_playlists" {
		t.Errorf("browseId = %q", browseID)
	}
	ctxMap, _ := gotBody["context"].(map[string]any)
	client, _ := ctxMap["client"].(map[string]any)
	if client["clientName"] != "WEB_REMIX" {
		t.Errorf("clientName = %v", client["clientName"])
	}
	cv, _ := client["clientVersion"].(string)
	if !clientVersionRe.MatchString(cv) {
		t.Errorf("clientVersion = %q, 应为 1.YYYYMMDD.01.00", cv)
	}
}

func TestListPlaylistsParsesGrid(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(gridFixture))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	got, err := c.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []RemotePlaylist{
		{ID: "PLAAA", Title: "我的最爱", Count: 5},
		{ID: "PLBBB", Title: "通勤歌单", Count: 12},
		{ID: "PLCCC", Title: "无 run 歌单", Count: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("playlists[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListPlaylistsToleratesFlatStructure(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(flatGridFixture))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	got, err := c.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "PLFLAT" || got[0].Title != "扁平结构歌单" || got[0].Count != 3 {
		t.Errorf("扁平结构解析 = %+v", got)
	}
}

func TestListPlaylistsLoggedOut(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(loggedOutFixture))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	_, err := c.ListPlaylists(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("logged_in=0 应返回 ErrNotLoggedIn, got %v", err)
	}
}

func TestListPlaylistsEmptyItems(t *testing.T) {
	// 200 但无歌单条目（logged_in=1）→ 未登录错误（契约：无条目视为未登录）
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contents":{},"serviceTrackingParams":[{"params":[{"key":"logged_in","value":"1"}]}]}`))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	_, err := c.ListPlaylists(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("无条目应返回 ErrNotLoggedIn, got %v", err)
	}
}

func TestListPlaylistsHTTPErrors(t *testing.T) {
	for _, code := range []int{400, 403, 500} {
		t.Run(strings.TrimPrefix(http.StatusText(code), ""), func(t *testing.T) {
			s, _ := newTestStore(t)
			pastedLogin(t, s, "SAPISID=test-sap")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()
			old := ytmBrowseURL
			ytmBrowseURL = srv.URL
			defer func() { ytmBrowseURL = old }()

			c := newTestClient(t, s, srv)
			_, err := c.ListPlaylists(context.Background())
			if !errors.Is(err, ErrSessionInvalid) {
				t.Errorf("HTTP %d 应返回 ErrSessionInvalid, got %v", code, err)
			}
		})
	}
}

// errorTransport 强制网络错误。
type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestListPlaylistsNetworkError(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	c := &Client{store: s, httpClient: &http.Client{Transport: errorTransport{}}}
	_, err := c.ListPlaylists(context.Background())
	if err == nil {
		t.Fatal("网络错误应透传")
	}
	if errors.Is(err, ErrSessionInvalid) || errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("网络错误不应归类为登录错误: %v", err)
	}
	if !strings.Contains(err.Error(), "请求 YouTube Music 失败") {
		t.Errorf("网络错误应包装说明: %v", err)
	}
}

func TestListPlaylistsNoSAPISIDCookie(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SID=only-session-cookie")
	c := &Client{store: s, httpClient: &http.Client{Transport: errorTransport{}}}
	_, err := c.ListPlaylists(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("缺少 SAPISID 应返回 ErrNotLoggedIn, got %v", err)
	}
}

func TestListPlaylistsNotConfigured(t *testing.T) {
	s, _ := newTestStore(t) // 未登录
	c := &Client{store: s, httpClient: &http.Client{Transport: errorTransport{}}}
	_, err := c.ListPlaylists(context.Background())
	if !errors.Is(err, ErrNoLogin) {
		t.Errorf("未配置登录应返回 ErrNoLogin, got %v", err)
	}
}

// ---- 分页（M2）----

// 首页 fixture：grid + 末尾 continuationItemRenderer（标准 continuationCommand.token 路径）。
const page1Fixture = `{
  "contents": {
    "singleColumnBrowseResultsRenderer": {
      "tabs": [{
        "tabRenderer": {
          "selected": true,
          "content": {
            "sectionListRenderer": {
              "contents": [{
                "itemSectionRenderer": {
                  "contents": [{
                    "gridRenderer": {
                      "items": [
                        {"musicTwoRowItemRenderer": {
                          "title": {"runs": [{"text": "我的最爱"}]},
                          "subtitle": {"runs": [{"text": "5 首"}]},
                          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLAAA"}}
                        }}
                      ]
                    }
                  }]
                }},
                {"continuationItemRenderer": {
                  "continuationEndpoint": {
                    "continuationCommand": {"token": "TOKEN1"}
                  }
                }}
              ]
            }
          }
        }
      }]
    }
  },
  "serviceTrackingParams": [{"service": "GFEEDBACK", "params": [{"key": "logged_in", "value": "1"}]}]
}`

// 第二页 fixture：onResponseReceivedActions 追加条目（含重复 PLAAA）+ 下一个令牌。
const page2Fixture = `{
  "onResponseReceivedActions": [{
    "appendContinuationItemsAction": {
      "continuationItems": [
        {"musicTwoRowItemRenderer": {
          "title": {"runs": [{"text": "通勤歌单"}]},
          "subtitle": {"runs": [{"text": "12 首"}]},
          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLBBB"}}
        }},
        {"musicTwoRowItemRenderer": {
          "title": {"runs": [{"text": "我的最爱"}]},
          "subtitle": {"runs": [{"text": "5 首"}]},
          "navigationEndpoint": {"browseEndpoint": {"browseId": "PLAAA"}}
        }},
        {"continuationItemRenderer": {
          "continuationEndpoint": {
            "continuation": "TOKEN2"
          }
        }}
      ]
    }
  }]
}`

// 第三页 fixture：无条目、无令牌（分页耗尽）。
const page3Fixture = `{"onResponseReceivedActions": []}`

func TestListPlaylistsPaginatesAndDedups(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	var bodies []string
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(data))
		var b map[string]any
		if err := json.Unmarshal(data, &b); err != nil {
			t.Fatalf("body 非法 JSON: %v", err)
		}
		if tok, _ := b["continuation"].(string); tok != "" {
			tokens = append(tokens, tok)
		}
		switch len(bodies) {
		case 1:
			_, _ = w.Write([]byte(page1Fixture))
		case 2:
			_, _ = w.Write([]byte(page2Fixture))
		default:
			_, _ = w.Write([]byte(page3Fixture))
		}
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	got, err := c.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 三页全部拉取，跨页去重（PLAAA 第二页重复不出现两次）
	if len(bodies) != 3 {
		t.Fatalf("请求页数 = %d, want 3", len(bodies))
	}
	if len(tokens) != 2 || tokens[0] != "TOKEN1" || tokens[1] != "TOKEN2" {
		t.Errorf("continuation 令牌序列 = %v, want [TOKEN1 TOKEN2]", tokens)
	}
	// 首页 body 有 browseId 无 continuation；分页 body 有 continuation 无 browseId
	var b map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &b); err != nil {
		t.Fatal(err)
	}
	if b["browseId"] != "FEmusic_liked_playlists" {
		t.Errorf("首页 browseId = %v", b["browseId"])
	}
	if _, ok := b["continuation"]; ok {
		t.Error("首页 body 不应有 continuation")
	}
	for i, body := range bodies[1:] {
		b := map[string]any{}
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			t.Fatal(err)
		}
		if _, ok := b["browseId"]; ok {
			t.Errorf("分页 body %d 不应有 browseId", i+1)
		}
		if b["continuation"] == nil {
			t.Errorf("分页 body %d 应有 continuation", i+1)
		}
	}
	want := []RemotePlaylist{
		{ID: "PLAAA", Title: "我的最爱", Count: 5},
		{ID: "PLBBB", Title: "通勤歌单", Count: 12},
	}
	if len(got) != len(want) {
		t.Fatalf("合并后歌单数 = %d, want %d（%+v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("playlists[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// 令牌提取：标准 continuationCommand.token / 直存 continuation /
// commandExecutorCommand 嵌套三路径 + 无令牌。
func TestExtractContinuationToken(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"standard", `{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"T1"}}}}`, "T1"},
		{"direct", `{"continuationItemRenderer":{"continuationEndpoint":{"continuation":"T2"}}}`, "T2"},
		{"executor-nested", `{"continuationItemRenderer":{"continuationEndpoint":{"commandExecutorCommand":{"commands":[
			{"continuationCommand":{"request":"CONTINUATION_REQUEST_TYPE_BROWSE","token":"T3"}}]}}}}`, "T3"},
		{"executor-other-request", `{"continuationItemRenderer":{"continuationEndpoint":{"commandExecutorCommand":{"commands":[
			{"continuationCommand":{"request":"CONTINUATION_REQUEST_TYPE_WATCH","token":"T4"}}]}}}}`, ""},
		{"empty", `{"continuationItemRenderer":{"continuationEndpoint":{}}}`, ""},
		{"no-renderer", `{"gridRenderer":{"items":[]}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tc.json), &v); err != nil {
				t.Fatal(err)
			}
			if _, tok := extractPlaylists(v); tok != tc.want {
				t.Errorf("token = %q, want %q", tok, tc.want)
			}
		})
	}
}

// 无限分页防御：服务端重复返回同一令牌时报错而非死循环。
func TestListPlaylistsContinuationLoopGuard(t *testing.T) {
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"contents":{},"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
			{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"SAME"}}}}
		]}}]}`))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()

	c := newTestClient(t, s, srv)
	_, err := c.ListPlaylists(context.Background())
	if err == nil || !strings.Contains(err.Error(), "分页超过") {
		t.Errorf("应报分页超限错误, got %v", err)
	}
	if n != maxContinuationPages+1 {
		t.Errorf("请求次数 = %d, want %d", n, maxContinuationPages+1)
	}
}

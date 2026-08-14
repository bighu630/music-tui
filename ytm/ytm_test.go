package ytm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupportedBrowsers(t *testing.T) {
	want := []BrowserInfo{
		{Name: "chrome", Label: "Google Chrome"},
		{Name: "chromium", Label: "Chromium"},
		{Name: "brave", Label: "Brave"},
		{Name: "edge", Label: "Microsoft Edge"},
		{Name: "vivaldi", Label: "Vivaldi"},
		{Name: "opera", Label: "Opera"},
	}
	if len(SupportedBrowsers) != len(want) {
		t.Fatalf("SupportedBrowsers = %d, want %d", len(SupportedBrowsers), len(want))
	}
	for i := range want {
		if SupportedBrowsers[i] != want[i] {
			t.Errorf("SupportedBrowsers[%d] = %+v, want %+v", i, SupportedBrowsers[i], want[i])
		}
	}
}

func TestNewClientDefaults(t *testing.T) {
	s, _ := newTestStore(t)
	c := NewClient(s, nil)
	if c.store != s || c.fetcher != nil {
		t.Errorf("NewClient 组装错误: %+v", c)
	}
	if c.httpClient == nil || c.httpClient.Timeout <= 0 {
		t.Errorf("默认 http client 应有超时: %+v", c.httpClient)
	}
}

func TestVerifyLogin(t *testing.T) {
	// 有效登录 → nil
	s, _ := newTestStore(t)
	pastedLogin(t, s, "SAPISID=test-sap")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(gridFixture))
	}))
	defer srv.Close()
	old := ytmBrowseURL
	ytmBrowseURL = srv.URL
	defer func() { ytmBrowseURL = old }()
	c := &Client{store: s, httpClient: srv.Client()}
	if err := c.VerifyLogin(context.Background()); err != nil {
		t.Errorf("有效登录 VerifyLogin 应返回 nil, got %v", err)
	}

	// 失效（HTTP 403）→ ErrSessionInvalid
	s2, _ := newTestStore(t)
	pastedLogin(t, s2, "SAPISID=expired")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv2.Close()
	ytmBrowseURL = srv2.URL
	c2 := &Client{store: s2, httpClient: srv2.Client()}
	if err := c2.VerifyLogin(context.Background()); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("失效登录应返回 ErrSessionInvalid, got %v", err)
	}

	// 未配置 → ErrNoLogin
	s3, _ := newTestStore(t)
	c3 := &Client{store: s3, httpClient: &http.Client{}}
	if err := c3.VerifyLogin(context.Background()); !errors.Is(err, ErrNoLogin) {
		t.Errorf("未配置应返回 ErrNoLogin, got %v", err)
	}
}

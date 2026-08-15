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

// Client 的 Store 门面：Login/SetLogin/ClearLogin/SyncEntries 委托正确。
func TestClientStoreFacade(t *testing.T) {
	s, _ := newTestStore(t)
	c := NewClient(s, nil)
	if c.Login().Method != MethodNone {
		t.Errorf("初始 Login = %+v, want MethodNone", c.Login())
	}
	if len(c.SyncEntries()) != 0 {
		t.Error("初始 SyncEntries 应为空")
	}
	if err := c.SetLogin(LoginConfig{Method: MethodBrowser, Browser: "chrome"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Login(); got.Method != MethodBrowser || got.Browser != "chrome" {
		t.Errorf("Login = %+v, want MethodBrowser/chrome", got)
	}
	if err := s.UpsertSync(SyncEntry{PlaylistID: "PL1", ListName: "YT: x"}); err != nil {
		t.Fatal(err)
	}
	if es := c.SyncEntries(); len(es) != 1 || es[0].ListName != "YT: x" {
		t.Errorf("SyncEntries = %+v", es)
	}
	if err := c.ClearLogin(); err != nil {
		t.Fatal(err)
	}
	if c.Login().Method != MethodNone {
		t.Error("ClearLogin 后应未登录")
	}
	// ClearLogin 保留同步映射
	if es := c.SyncEntries(); len(es) != 1 {
		t.Errorf("ClearLogin 不应清除同步映射: %+v", es)
	}
}

// SetHTTPClient：替换默认客户端；nil 恢复默认超时客户端。
func TestSetHTTPClient(t *testing.T) {
	s, _ := newTestStore(t)
	c := NewClient(s, nil)
	old := c.httpClient
	hc := &http.Client{}
	c.SetHTTPClient(hc)
	if c.httpClient != hc {
		t.Error("SetHTTPClient 应替换 http client")
	}
	c.SetHTTPClient(nil)
	if c.httpClient == nil || c.httpClient == hc {
		t.Error("SetHTTPClient(nil) 应恢复默认客户端")
	}
	if c.httpClient == old || c.httpClient.Timeout <= 0 {
		t.Error("恢复的客户端应有超时")
	}
}

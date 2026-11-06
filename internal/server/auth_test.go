package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/store"
)

// newEmptyServer 构造一个没有密钥、没有账号的网关（用于鉴权边界测试）。
func newEmptyServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return New(st)
}

// call 以指定来源地址发请求。
func call(t *testing.T, s *Server, remoteAddr string, headers map[string]string, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// 未签发密钥时，本机回环仍可用（自举 / 本地调试）。
func TestAuthWithoutKeysAllowsLoopback(t *testing.T) {
	s := newEmptyServer(t)

	for _, addr := range []string{"127.0.0.1:1", "[::1]:2", "127.0.0.1:65535"} {
		if rec := call(t, s, addr, nil, "/v1/models"); rec.Code == http.StatusServiceUnavailable {
			t.Fatalf("回环来源 %s 不应被 setup_required 拦截", addr)
		}
	}
}

// 未签发密钥时，非回环来源必须被拒绝 —— 否则 0.0.0.0 监听就是开放中继。
func TestAuthWithoutKeysRejectsNonLoopback(t *testing.T) {
	s := newEmptyServer(t)

	for _, addr := range []string{"192.168.1.10:5555", "10.0.0.1:1", "8.8.8.8:443", ""} {
		rec := call(t, s, addr, nil, "/v1/models")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("来源 %q 状态码 = %d，期望 503", addr, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "setup_required") {
			t.Fatalf("错误类型应为 setup_required，实际 %s", rec.Body.String())
		}
	}
}

// 一旦签发密钥，非回环来源需要正确的密钥。
func TestAuthWithKeys(t *testing.T) {
	s := newEmptyServer(t)
	key := s.Store.AddKey("k", "").Key

	if rec := call(t, s, "192.168.1.10:1", nil, "/v1/models"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺密钥 → %d，期望 401", rec.Code)
	}
	if rec := call(t, s, "192.168.1.10:1", map[string]string{"Authorization": "Bearer sk-wrong"}, "/v1/models"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错密钥 → %d，期望 401", rec.Code)
	}
	if rec := call(t, s, "192.168.1.10:1", map[string]string{"Authorization": "Bearer " + key}, "/v1/models"); rec.Code != http.StatusOK {
		t.Fatalf("正确密钥 → %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	// x-api-key 同样被接受（Anthropic 客户端习惯）。
	if rec := call(t, s, "192.168.1.10:1", map[string]string{"x-api-key": key}, "/v1/models"); rec.Code != http.StatusOK {
		t.Fatalf("x-api-key → %d，期望 200", rec.Code)
	}
}

func TestAuthRejectsDisabledKey(t *testing.T) {
	s := newEmptyServer(t)
	key := s.Store.AddKey("k", "").Key
	s.Store.ToggleKey(key)

	if rec := call(t, s, "192.168.1.10:1", map[string]string{"Authorization": "Bearer " + key}, "/v1/models"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("停用密钥 → %d，期望 401", rec.Code)
	}
}

// 无管理密码时，管理接口只允许本机。
func TestAdminAuthLoopbackOnlyWithoutPassword(t *testing.T) {
	s := newEmptyServer(t)

	if rec := call(t, s, "192.168.1.10:1", nil, "/admin/api/state"); rec.Code != http.StatusForbidden {
		t.Fatalf("局域网访问 → %d，期望 403", rec.Code)
	}
	if rec := call(t, s, "127.0.0.1:1", nil, "/admin/api/state"); rec.Code != http.StatusOK {
		t.Fatalf("本机访问 → %d，期望 200：%s", rec.Code, rec.Body.String())
	}
}

// 有管理密码时，任何来源都必须带正确密码。
func TestAdminAuthRequiresPassword(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpdateSettings(func(st *store.Settings) { st.AdminPassword = "secret" })

	if rec := call(t, s, "127.0.0.1:1", nil, "/admin/api/state"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺密码 → %d，期望 401", rec.Code)
	}
	if rec := call(t, s, "127.0.0.1:1", map[string]string{"X-Admin-Password": "bad"}, "/admin/api/state"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错密码 → %d，期望 401", rec.Code)
	}
	if rec := call(t, s, "192.168.1.10:1", map[string]string{"X-Admin-Password": "secret"}, "/admin/api/state"); rec.Code != http.StatusOK {
		t.Fatalf("正确密码 → %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	if rec := call(t, s, "192.168.1.10:1", map[string]string{"Authorization": "Bearer secret"}, "/admin/api/state"); rec.Code != http.StatusOK {
		t.Fatalf("Bearer 密码 → %d，期望 200", rec.Code)
	}
}

// 写操作要做同源校验，阻断 CSRF。
func TestAdminWriteRequiresSameOrigin(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpdateSettings(func(st *store.Settings) { st.AdminPassword = "pw" })

	post := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", strings.NewReader("{}"))
		req.RemoteAddr = "127.0.0.1:1"
		req.Host = "127.0.0.1:10087"
		req.Header.Set("X-Admin-Password", "pw")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post(""); code == http.StatusForbidden {
		t.Fatal("无 Origin 的请求（CLI / curl）不应被 CSRF 校验拦截")
	}
	if code := post("http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("跨站源 → %d，期望 403", code)
	}
	if code := post("http://127.0.0.1:10087"); code == http.StatusForbidden {
		t.Fatalf("同源写请求被误拦（%d）", code)
	}
}

// CORS 默认只回显本机来源，不回显任意站点。
func TestCORSDefaultsToLoopback(t *testing.T) {
	s := newEmptyServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("本机来源应被放行，实际 %q", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req2.RemoteAddr = "127.0.0.1:1"
	req2.Header.Set("Origin", "https://evil.example")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("外部来源不应被放行，实际 %q", got)
	}
}

// 健康检查不需要密钥（供探活 / 负载均衡使用）。
func TestHealthRequiresNoKey(t *testing.T) {
	s := newEmptyServer(t)
	for _, p := range []string{"/ping", "/health", "/healthz"} {
		if rec := call(t, s, "8.8.8.8:1", nil, p); rec.Code != http.StatusOK {
			t.Fatalf("%s → %d，期望 200", p, rec.Code)
		}
	}
}

// 号池为空时给出可读错误，而不是 500。
func TestNoAccountsError(t *testing.T) {
	s := newEmptyServer(t)
	key := s.Store.AddKey("k", "").Key

	rec := call(t, s, "127.0.0.1:1", map[string]string{"Authorization": "Bearer " + key}, "/v1/models")
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/models 不依赖账号，应返回 200，实际 %d", rec.Code)
	}

	// 推理端点没有账号时应报错（具体状态码由 writeUpstreamErr 决定，但不能 200）。
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer "+key)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code == http.StatusOK {
		t.Fatalf("空号池不应返回 200：%s", rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "账号") {
		t.Fatalf("错误信息应提示缺少账号，实际 %s", rec2.Body.String())
	}
}

// 确认账号池为空时的错误文案不会泄漏上游细节。
func TestErrorMessageDoesNotLeakKey(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "a", Name: "n", APIKey: "sk-super-secret", Enabled: true})
	key := s.Store.AddKey("k", "").Key

	req := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "sk-super-secret") {
		t.Fatalf("管理接口不应回传上游明文 Key：%s", rec.Body.String())
	}
}

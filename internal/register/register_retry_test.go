package register

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withFastRegisterBudget(t *testing.T, d time.Duration) {
	t.Helper()
	saved := registerPostBudget
	registerPostBudget = d
	t.Cleanup(func() { registerPostBudget = saved })
}

// 上游挂住不回时，注册 POST 必须在上限内失败，而不是让一个账号拖住 190s。
func TestRegisterRespectsBudget(t *testing.T) {
	withFastRegisterBudget(t, 300*time.Millisecond)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/daily-time-sync/registration/" {
			<-release // 模拟 504 之前的长时间挂起
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer func() { close(release); srv.Close() }()

	c := &WebClient{Origin: srv.URL, Site: srv.URL, HTTP: srv.Client()}
	start := time.Now()
	_, err := c.Register(context.Background(), "a@b.com", "pw")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("挂起的上游应返回错误")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("注册 POST 耗时 %v，应在上限 300ms 附近失败", elapsed)
	}
}

// 上游返回 504 时应把状态码透出来，供 pipeline 决定是否走登录兜底。
func TestRegisterSurfacesGatewayTimeout(t *testing.T) {
	withFastRegisterBudget(t, 2*time.Second)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, `<html>504 Gateway Time-out</html>`)
	}))
	defer srv.Close()

	c := &WebClient{Origin: srv.URL, Site: srv.URL, HTTP: srv.Client()}
	_, err := c.Register(context.Background(), "a@b.com", "pw")
	if err == nil {
		t.Fatal("504 应返回错误")
	}
	var we *WebError
	if !asWebError(err, &we) {
		t.Fatalf("错误类型 = %T，期望 *WebError", err)
	}
	if we.Status != http.StatusGatewayTimeout {
		t.Fatalf("状态码 = %d，期望 504", we.Status)
	}
}

func asWebError(err error, target **WebError) bool {
	e, ok := err.(*WebError)
	if ok {
		*target = e
	}
	return ok
}

// 「已注册」这种语义错误不能被当成超时重试，否则会把成功路径拖慢。
func TestIsAlreadyRegisteredSemantics(t *testing.T) {
	yes := []string{
		`注册: HTTP 400: {"message":"This email already has an account"}`,
		`注册: HTTP 400: account already exists`,
		`注册: HTTP 409: already registered`,
	}
	for _, m := range yes {
		if !isAlreadyRegistered(errString(m)) {
			t.Errorf("%q 应判定为已注册", m)
		}
	}
	no := []string{
		`POST /daily-time-sync/registration/: 504 Gateway Timeout`,
		`注册: HTTP 500: internal error`,
	}
	for _, m := range no {
		if isAlreadyRegistered(errString(m)) {
			t.Errorf("%q 不应判定为已注册", m)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 预算生效后，错误信息里应能看到 deadline，便于日志排查。
func TestRegisterBudgetErrorIsReadable(t *testing.T) {
	withFastRegisterBudget(t, 200*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	c := &WebClient{Origin: srv.URL, Site: srv.URL, HTTP: srv.Client()}
	_, err := c.Register(context.Background(), "a@b.com", "pw")
	if err == nil {
		t.Fatal("应返回错误")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "deadline") && !strings.Contains(msg, "timeout") && !strings.Contains(msg, "canceled") {
		t.Fatalf("错误信息不可读：%v", err)
	}
}

package register

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
)

func withFastRetry(t *testing.T) {
	t.Helper()
	saved := theosRetryDelays
	theosRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { theosRetryDelays = saved })
}

func newTheosClient(t *testing.T, h http.HandlerFunc) *deepai.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := deepai.NewClientWithFingerprint("", "")
	c.Origin = srv.URL
	return c
}

// 瞬时错误（5xx）应重试并在下一次成功。
func TestTheosWithRetrySucceedsAfterTransient(t *testing.T) {
	withFastRetry(t)
	var calls atomic.Int32
	c := newTheosClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"temporarily down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"200.00000","granted":"200.00000"}`)
	})

	acct := &deepai.Account{APIKey: "k"}
	al, err := theosWithRetry(context.Background(), c, acct)
	if err != nil {
		t.Fatalf("应重试后成功: %v", err)
	}
	if al.Balance != "200.00000" {
		t.Fatalf("balance = %q", al.Balance)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("调用次数 = %d，期望 2", got)
	}
}

// 鉴权失败不该重试（那是 Key 真的有问题）。
func TestTheosWithRetryDoesNotRetryAuthError(t *testing.T) {
	withFastRetry(t)
	var calls atomic.Int32
	c := newTheosClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_key"}}`)
	})

	if _, err := theosWithRetry(context.Background(), c, &deepai.Account{APIKey: "bad"}); err == nil {
		t.Fatal("401 应返回错误")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("401 不应重试，实际调用 %d 次", got)
	}
}

// 重试耗尽后返回最后一次错误。
func TestTheosWithRetryGivesUp(t *testing.T) {
	withFastRetry(t)
	var calls atomic.Int32
	c := newTheosClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"bad gateway"}}`)
	})

	if _, err := theosWithRetry(context.Background(), c, &deepai.Account{APIKey: "k"}); err == nil {
		t.Fatal("持续 502 应最终失败")
	}
	// 1 次首探 + 2 次重试
	if got := calls.Load(); got != int32(1+len(theosRetryDelays)) {
		t.Fatalf("调用次数 = %d，期望 %d", got, 1+len(theosRetryDelays))
	}
}

// 并发不应超过出口数量：多出来的 worker 只会排队。
func TestCapConcurrency(t *testing.T) {
	pool, err := NewProxyPool("http://u:p@a.example:1,http://u:p@b.example:2")
	if err != nil {
		t.Fatalf("NewProxyPool: %v", err)
	}

	if got, capped := pool.CapConcurrency(10); !capped || got != 2 {
		t.Fatalf("10 并发 / 2 出口 → %d (capped=%v)，期望压到 2", got, capped)
	}
	if got, capped := pool.CapConcurrency(1); capped || got != 1 {
		t.Fatalf("低于出口数不应压缩，得到 %d (capped=%v)", got, capped)
	}
	if got, capped := pool.CapConcurrency(2); capped || got != 2 {
		t.Fatalf("等于出口数不应压缩，得到 %d (capped=%v)", got, capped)
	}
	// 没有代理池时原样返回。
	var nilPool *ProxyPool
	if got, capped := nilPool.CapConcurrency(8); capped || got != 8 {
		t.Fatalf("nil pool 应原样返回，得到 %d (capped=%v)", got, capped)
	}
}

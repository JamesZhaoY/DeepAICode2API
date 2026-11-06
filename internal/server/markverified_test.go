package server

import (
	"context"
	"net/http"
	"testing"

	"deepaicode2api/internal/deepai"
)

// 后台验证完成后由注册链路回填 email_verified。
func TestMarkVerifiedByID(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "acc-1", Name: "n", APIKey: "sk-1", Email: "a@b.com"})

	rec := postAdmin(t, s, "/admin/api/accounts", `{"action":"mark-verified","id":"acc-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if a := s.Store.Account("acc-1"); a == nil || !a.EmailVerified {
		t.Fatalf("未回填 email_verified: %#v", a)
	}

	// 幂等：重复调用不报错。
	if rec := postAdmin(t, s, "/admin/api/accounts", `{"action":"mark-verified","id":"acc-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("重复调用应幂等，实际 %d", rec.Code)
	}
}

// 入池失败但验证成功时只有邮箱可用，按邮箱回退查找。
func TestMarkVerifiedByEmailFallback(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "acc-9", Name: "n", APIKey: "sk-9", Email: "fallback@b.com"})

	rec := postAdmin(t, s, "/admin/api/accounts",
		`{"action":"mark-verified","id":"stale-id","email":"FALLBACK@b.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if a := s.Store.Account("acc-9"); a == nil || !a.EmailVerified {
		t.Fatalf("按邮箱回填失败: %#v", a)
	}
}

func TestMarkVerifiedMissingAccount(t *testing.T) {
	s := newEmptyServer(t)
	rec := postAdmin(t, s, "/admin/api/accounts", `{"action":"mark-verified","id":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的账号应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 入池器必须同时能回填验证状态（后台流程依赖 register.VerifiedMarker 断言）。
func TestLocalEnrollerImplementsMarker(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "acc-z", Name: "n", APIKey: "sk-z", Email: "z@b.com"})

	if err := (localEnroller{s: s}).MarkVerified(context.Background(), "acc-z", ""); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	if a := s.Store.Account("acc-z"); a == nil || !a.EmailVerified {
		t.Fatal("本地入池器未回填 email_verified")
	}
}

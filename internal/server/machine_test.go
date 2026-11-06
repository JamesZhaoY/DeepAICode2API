package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
)

// postAdmin 以本机来源调用管理接口。
func postAdmin(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAssignMachineBackfills(t *testing.T) {
	s := newEmptyServer(t)
	missing := &deepai.Account{ID: "a1", Name: "missing", APIKey: "sk-1", Enabled: true}
	hasOne := &deepai.Account{ID: "a2", Name: "has", APIKey: "sk-2", Enabled: true,
		MachineID: "existing-machine-uuid"}
	s.Store.UpsertAccount(missing)
	s.Store.UpsertAccount(hasOne)

	rec := postAdmin(t, s, "/admin/api/accounts", `{"action":"assign-machine"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if out["assigned"].(float64) != 1 {
		t.Fatalf("应只补 1 个（已有的不覆盖），实际 %v", out["assigned"])
	}

	got := s.Store.Account("a1")
	if got.MachineID == "" {
		t.Fatal("缺失机器指纹的账号未被补齐")
	}
	// 已有指纹不能被覆盖：那会让该账号在上游的额度归属失效。
	if after := s.Store.Account("a2"); after.MachineID != "existing-machine-uuid" {
		t.Fatalf("已有机器指纹被覆盖: %q", after.MachineID)
	}

	// 再跑一次不应重复分配。
	rec = postAdmin(t, s, "/admin/api/accounts", `{"action":"assign-machine"}`)
	out = decodeJSON[map[string]any](t, rec.Body.Bytes())
	if out["assigned"].(float64) != 0 {
		t.Fatalf("重复调用不应再分配，实际 %v", out["assigned"])
	}
}

func TestAssignMachineSingleAccount(t *testing.T) {
	s := newEmptyServer(t)
	a := &deepai.Account{ID: "a1", Name: "n", APIKey: "sk-1", Enabled: true}
	b := &deepai.Account{ID: "b1", Name: "m", APIKey: "sk-2", Enabled: true}
	s.Store.UpsertAccount(a)
	s.Store.UpsertAccount(b)

	rec := postAdmin(t, s, "/admin/api/accounts", `{"action":"assign-machine","id":"a1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if s.Store.Account("a1").MachineID == "" {
		t.Fatal("指定账号未被补齐")
	}
	if s.Store.Account("b1").MachineID != "" {
		t.Fatal("未指定的账号不应被修改")
	}
}

// 入池时必须给每个账号分配独立机器指纹。
func TestEnrollAssignsMachine(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"0","granted":"0"}`)
	})
	s, _ := newTestServer(t, u)

	rec := postAdmin(t, s, "/admin/api/accounts",
		`{"action":"enroll","api_key":"sk-new-account","email":"x@y.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	id, _ := out["id"].(string)

	acct := s.Store.Account(id)
	if acct == nil {
		t.Fatal("账号未入池")
	}
	if acct.MachineID == "" {
		t.Fatal("入池时应自动分配机器指纹")
	}

	// 同时必须真的带着这个指纹去问上游要额度。
	var seen string
	u2 := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(deepai.HeaderMachine)
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"0"}`)
	})
	s2 := New(s.Store)
	s2.Up.Origin = u2.srv.URL
	if rec := postAdmin(t, s2, "/admin/api/accounts",
		`{"action":"enroll","api_key":"sk-other","email":"z@y.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("第二次入池失败: %s", rec.Body.String())
	}
	if seen == "" {
		t.Fatal("入池探测没有发送机器指纹")
	}
	if seen == deepai.MachineFingerprint("") {
		t.Fatal("机器指纹为空")
	}
}

func TestStateExposesMachineSummary(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{
		ID: "a1", Name: "with", APIKey: "sk-1", Enabled: true,
		MachineID: "12345678-1234-4234-8234-123456789012",
	})
	s.Store.UpsertAccount(&deepai.Account{ID: "a2", Name: "without", APIKey: "sk-2", Enabled: true})

	rec := call(t, s, "127.0.0.1:1", nil, "/admin/api/state")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	accts, _ := out["accounts"].([]any)
	if len(accts) != 2 {
		t.Fatalf("账号数 = %d", len(accts))
	}

	var with, without map[string]any
	for _, av := range accts {
		m := av.(map[string]any)
		if m["name"] == "with" {
			with = m
		} else {
			without = m
		}
	}
	if with["has_machine"] != true {
		t.Fatalf("has_machine 应为 true: %#v", with)
	}
	// 只下发摘要，完整 UUID 不出网。
	if got, _ := with["machine"].(string); got == "" || !strings.HasSuffix(got, "…") {
		t.Fatalf("机器指纹应脱敏为摘要，实际 %q", got)
	}
	if without["has_machine"] != false {
		t.Fatalf("无指纹账号 has_machine 应为 false: %#v", without)
	}
}

func TestCheckinProbesAllAccounts(t *testing.T) {
	var hits atomic.Int64
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"1.25"}`)
	})
	s, _ := newTestServer(t, u)
	for i := 0; i < 5; i++ {
		s.Store.UpsertAccount(&deepai.Account{
			ID: "acc-" + string(rune('a'+i)), APIKey: "sk-" + string(rune('a'+i)), Enabled: true,
		})
	}

	rec := postAdmin(t, s, "/admin/api/checkin", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	results, _ := out["results"].([]any)
	if len(results) != 6 { // 1 个来自 newTestServer + 5 个新增
		t.Fatalf("探测结果数 = %d，期望 6", len(results))
	}
	if got := hits.Load(); got != 6 {
		t.Fatalf("上游被探测 %d 次，期望 6", got)
	}
	for _, rv := range results {
		m := rv.(map[string]any)
		if m["ok"] != true {
			t.Fatalf("探测应成功: %#v", m)
		}
	}
}

// 新账号入池时上游常常还没发当日额度（balance=0），几分钟后才有。
// 入池路径必须安排一次延迟复探，否则这个 0 会被永久缓存、账号被长期跳过。
func TestEnrollSchedulesQuotaReprobe(t *testing.T) {
	var mu sync.Mutex
	probes := 0
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := probes
		probes++
		mu.Unlock()
		if n == 0 {
			_, _ = io.WriteString(w, `{"enabled":true,"balance":"0.00000","granted":"0.00000"}`)
			return
		}
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"200.00000","granted":"200.00000"}`)
	})
	s, _ := newTestServer(t, u)
	// 测试里把 3 分钟退避压到 10ms。
	s.quotaReprobeDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}

	rec := postAdmin(t, s, "/admin/api/accounts",
		`{"action":"enroll","api_key":"sk-late-quota","email":"late@y.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("入池失败: %s", rec.Body.String())
	}
	id, _ := decodeJSON[map[string]any](t, rec.Body.Bytes())["id"].(string)
	if id == "" {
		t.Fatal("入池未返回 id")
	}

	// 入池瞬间应当是 0（上游如此）。
	if a := s.Store.AccountCopy(id); a == nil || a.Theos == nil || a.Theos.Balance != "0.00000" {
		t.Fatalf("入池时应记录上游返回的 0 额度: %#v", a)
	}

	// 等复探把额度补上。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := s.Store.AccountCopy(id); a != nil && a.Theos != nil && a.Theos.Balance == "200.00000" {
			return // ok
		}
		time.Sleep(20 * time.Millisecond)
	}
	a := s.Store.AccountCopy(id)
	t.Fatalf("延迟复探未更新额度: %#v", a.Theos)
}

// 已经拿到额度的账号不应再被安排复探。
func TestEnrollWithQuotaSkipsReprobe(t *testing.T) {
	var mu sync.Mutex
	probes := 0
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		probes++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"200.00000","granted":"200.00000"}`)
	})
	s, _ := newTestServer(t, u)
	s.quotaReprobeDelays = []time.Duration{10 * time.Millisecond}

	if rec := postAdmin(t, s, "/admin/api/accounts",
		`{"action":"enroll","api_key":"sk-has-quota","email":"q@y.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("入池失败: %s", rec.Body.String())
	}
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := probes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("已有额度时不应再次探测，实际探测 %d 次", got)
	}
}

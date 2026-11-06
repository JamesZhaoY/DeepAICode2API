package deepai

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestMachineFingerprint(t *testing.T) {
	// 必须是 sha256 十六进制（64 位小写）。
	got := MachineFingerprint("11111111-2222-3333-4444-555555555555")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("指纹格式错误: %q", got)
	}
	// 大小写与首尾空白应归一。
	if again := MachineFingerprint("  11111111-2222-3333-4444-555555555555  "); again != got {
		t.Fatalf("同一 UUID 的不同写法应得到同一指纹: %q vs %q", again, got)
	}
	if upper := MachineFingerprint("11111111-2222-3333-4444-555555555555"); upper != got {
		t.Fatalf("大小写应归一: %q vs %q", upper, got)
	}
	// 空串返回空串（调用方据此不发请求头）。
	if MachineFingerprint("") != "" || MachineFingerprint("   ") != "" {
		t.Fatal("空输入应返回空指纹")
	}
	// 不同 UUID 必须不同指纹。
	if MachineFingerprint("a") == MachineFingerprint("b") {
		t.Fatal("不同输入得到了相同指纹")
	}
}

func TestNewMachineUUID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewMachineUUID()
		if !pattern.MatchString(id) {
			t.Fatalf("不是合法的 v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("生成了重复 UUID: %q", id)
		}
		seen[id] = true
	}
}

// 账号级机器指纹必须覆盖进程级默认值 —— 这是免费额度能按号发放的前提。
func TestClientPerAccountMachine(t *testing.T) {
	var got []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(HeaderMachine))
		_, _ = io.WriteString(w, `{}`)
	})
	c.Machine = MachineFingerprint("global-machine")

	acctA := &Account{APIKey: "k1", MachineID: "aaaaaaaa-1111-4111-8111-111111111111"}
	acctB := &Account{APIKey: "k2", MachineID: "bbbbbbbb-2222-4222-8222-222222222222"}
	noMachine := &Account{APIKey: "k3"}

	for _, acct := range []*Account{acctA, acctB, noMachine} {
		if _, err := c.OpenModel(context.Background(), acct, PathChat, []byte(`{}`), ""); err != nil {
			t.Fatalf("OpenModel: %v", err)
		}
	}

	if len(got) != 3 {
		t.Fatalf("请求数 = %d", len(got))
	}
	if want := MachineFingerprint(acctA.MachineID); got[0] != want {
		t.Fatalf("账号 A 的指纹 = %q，期望 %q", got[0], want)
	}
	if want := MachineFingerprint(acctB.MachineID); got[1] != want {
		t.Fatalf("账号 B 的指纹 = %q，期望 %q", got[1], want)
	}
	if got[0] == got[1] {
		t.Fatal("两个账号不应共用同一机器指纹（会导致额度互斥）")
	}
	// 没有账号级指纹时回退到进程级。
	if got[2] != c.Machine {
		t.Fatalf("无账号指纹时应回退到进程级 %q，实际 %q", c.Machine, got[2])
	}
}

func TestTheosUsesAccountMachine(t *testing.T) {
	// /theos 决定额度归属，必须用账号自己的指纹。
	var seen string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(HeaderMachine)
		_, _ = io.WriteString(w, `{"enabled":true,"balance":"1"}`)
	})
	c.Machine = MachineFingerprint("global")

	acct := &Account{APIKey: "k", MachineID: "cccccccc-3333-4333-8333-333333333333"}
	if _, err := c.Theos(context.Background(), acct); err != nil {
		t.Fatalf("Theos: %v", err)
	}
	if want := MachineFingerprint(acct.MachineID); seen != want {
		t.Fatalf("/theos 的机器指纹 = %q，期望 %q", seen, want)
	}
}

func TestRegistrationCarriesMachineID(t *testing.T) {
	acct, err := AccountFromRegistration(Registration{
		APIKey:    "sk-abc",
		Email:     "a@b.com",
		MachineID: "dddddddd-4444-4444-8444-444444444444",
	})
	if err != nil {
		t.Fatalf("AccountFromRegistration: %v", err)
	}
	if acct.MachineID != "dddddddd-4444-4444-8444-444444444444" {
		t.Fatalf("注册信息里的机器指纹丢失: %q", acct.MachineID)
	}
	// 首尾空白应被裁剪。
	acct2, err := AccountFromRegistration(Registration{
		APIKey: "sk-abc", Email: "a@b.com", MachineID: "  eeee  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if acct2.MachineID != "eeee" {
		t.Fatalf("机器指纹未裁剪: %q", acct2.MachineID)
	}
}

func TestMachineUUIDIsStableThroughFingerprint(t *testing.T) {
	// 同一账号多次请求必须发同一个指纹，否则上游会认为机器在漂移。
	id := NewMachineUUID()
	if id == "" {
		t.Fatal("生成 UUID 失败")
	}
	first := MachineFingerprint(id)
	for i := 0; i < 5; i++ {
		if got := MachineFingerprint(id); got != first {
			t.Fatalf("同一 UUID 的指纹不稳定: %q vs %q", got, first)
		}
	}
	if strings.EqualFold(id, first) {
		t.Fatal("指纹不应该是 UUID 本身")
	}
}

// 推理侧必须复用注册时的浏览器指纹，否则“Chrome 注册 + 空 UA 推理”本身就是异常信号。
func TestClientPerAccountFingerprintHeaders(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{}`)
	})

	acct := &Account{
		APIKey:         "k",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/156.0.0.0",
		AcceptLanguage: "en-US,en;q=0.9",
		SecCHUA:        `"Not:A-Brand";v="8", "Chromium";v="156"`,
		Platform:       `"Windows"`,
	}
	if _, err := c.OpenModel(context.Background(), acct, PathChat, []byte(`{}`), ""); err != nil {
		t.Fatalf("OpenModel: %v", err)
	}

	if v := got.Get("User-Agent"); v != acct.UserAgent {
		t.Fatalf("User-Agent = %q，期望 %q", v, acct.UserAgent)
	}
	if v := got.Get("Accept-Language"); v != acct.AcceptLanguage {
		t.Fatalf("Accept-Language = %q", v)
	}
	if v := got.Get("sec-ch-ua"); v != acct.SecCHUA {
		t.Fatalf("sec-ch-ua = %q", v)
	}
	if v := got.Get("sec-ch-ua-platform"); v != acct.Platform {
		t.Fatalf("sec-ch-ua-platform = %q", v)
	}
	if v := got.Get("sec-ch-ua-mobile"); v != "?0" {
		t.Fatalf("sec-ch-ua-mobile = %q", v)
	}
}

func TestClientFallsBackToClientFingerprint(t *testing.T) {
	// 没有账号级指纹时（桌面端导入 / 手工添加）回退到客户端默认值。
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{}`)
	})
	c.UserAgent = "fallback-ua/2"
	if _, err := c.OpenModel(context.Background(), &Account{APIKey: "k"}, PathChat, []byte(`{}`), ""); err != nil {
		t.Fatalf("OpenModel: %v", err)
	}
	if v := got.Get("User-Agent"); v != "fallback-ua/2" {
		t.Fatalf("User-Agent = %q，期望回退值", v)
	}
	// 账号没有指纹时不发指纹头，避免发空值。
	if v := got.Get("Accept-Language"); v != "" {
		t.Fatalf("无账号指纹时不应发送 Accept-Language，实际 %q", v)
	}
}

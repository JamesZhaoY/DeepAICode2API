package register

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// errTest 是台账测试用的哨兵错误。
var errTest = errors.New("boom")

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l := NewLedger(filepath.Join(t.TempDir(), "exports", "register-ledger.jsonl"))
	// 台账是缓冲写的：用完必须关闭，否则 Windows 上临时目录删不掉。
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestLedgerRedactsSecretsOnDisk(t *testing.T) {
	l := newTestLedger(t)

	rec := NewRecord("a@b.com")
	rec.Status = StatusKeyed
	rec.APIKey = "sk-upstream"
	rec.Password = "hunter2"
	rec.SessionKey = "sess-secret"
	rec.CSRFToken = "csrf-secret"

	if err := l.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	raw, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatalf("读台账: %v", err)
	}
	for _, secret := range []string{"hunter2", "sess-secret", "csrf-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("台账里出现了明文凭据 %q:\n%s", secret, raw)
		}
	}
	// 非敏感字段仍要在，否则台账失去排查价值。
	if !strings.Contains(string(raw), "sk-upstream") {
		t.Fatalf("API Key 应保留在台账里:\n%s", raw)
	}

	// 旁路文件必须存在且是 0600。
	secPath := l.SecretPath()
	if secPath == l.Path {
		t.Fatal("旁路文件路径不应与主台账相同")
	}
	fi, err := os.Stat(secPath)
	if err != nil {
		t.Fatalf("凭据旁路文件缺失: %v", err)
	}
	// Windows 上 os.OpenFile 的 mode 位不起作用，只校验 Unix。
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("凭据文件权限过宽: %o", perm)
		}
	}
	secRaw, _ := os.ReadFile(secPath)
	if !strings.Contains(string(secRaw), "hunter2") {
		t.Fatal("密码应保存到旁路文件（供补跑使用）")
	}
}

func TestLedgerLoadRestoresSecrets(t *testing.T) {
	l := newTestLedger(t)

	rec := NewRecord("a@b.com")
	rec.Status = StatusKeyed
	rec.APIKey = "sk-1"
	rec.Password = "pw-from-sidecar"
	rec.SessionID = "sid"
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("记录数 = %d", len(got))
	}
	// 补跑需要密码：必须能从旁路文件并回来。
	if got[0].Password != "pw-from-sidecar" {
		t.Fatalf("密码未从旁路文件恢复: %q", got[0].Password)
	}
	if got[0].APIKey != "sk-1" {
		t.Fatalf("APIKey 丢失: %q", got[0].APIKey)
	}
}

func TestLedgerWithoutSecretsCreatesNoSidecar(t *testing.T) {
	l := newTestLedger(t)

	rec := NewRecord("a@b.com")
	rec.Status = StatusKeyed
	rec.APIKey = "sk-1"
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if _, err := os.Stat(l.SecretPath()); !os.IsNotExist(err) {
		t.Fatalf("无凭据时不应创建旁路文件: %v", err)
	}
}

func TestLedgerAppendDoesNotMutateRecord(t *testing.T) {
	l := newTestLedger(t)
	rec := NewRecord("a@b.com")
	rec.Password = "keep-in-memory"
	if err := l.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 脱敏必须作用在副本上：入池（Enroll）之后还要用这份内存记录。
	if rec.Password != "keep-in-memory" {
		t.Fatalf("Append 修改了入参记录: %q", rec.Password)
	}
}

func TestLedgerLoadMergesByEmail(t *testing.T) {
	l := newTestLedger(t)

	first := NewRecord("a@b.com")
	first.Status = StatusKeyed
	first.APIKey = "sk-keep"
	if err := l.Append(first); err != nil {
		t.Fatal(err)
	}
	// 后一次尝试失败：不能覆盖掉已经赚到的 Key。
	fail := NewRecord("a@b.com")
	fail.SetError("login", errTest)
	if err := l.Append(fail); err != nil {
		t.Fatal(err)
	}

	got, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("同邮箱应合并为 1 条，实际 %d", len(got))
	}
	if got[0].Status != StatusKeyed || got[0].APIKey != "sk-keep" {
		t.Fatalf("失败记录覆盖了成功记录: %#v", got[0])
	}
	if got[0].LastError == "" {
		t.Fatal("应保留本次失败原因")
	}
}

func TestLedgerCompact(t *testing.T) {
	l := newTestLedger(t)

	// 同一邮箱写 5 次。
	for i := 0; i < 5; i++ {
		rec := NewRecord("a@b.com")
		rec.Status = StatusKeyed
		rec.APIKey = "sk-1"
		if err := l.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	before, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(before)), "\n") + 1; lines != 5 {
		t.Fatalf("append-only 应有 5 行，实际 %d", lines)
	}

	if err := l.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(after)), "\n") + 1; lines != 1 {
		t.Fatalf("压缩后应为 1 行，实际 %d:\n%s", lines, after)
	}

	// 语义不变。
	got, err := l.Load()
	if err != nil {
		t.Fatalf("压缩后 Load: %v", err)
	}
	if len(got) != 1 || got[0].APIKey != "sk-1" {
		t.Fatalf("压缩改变了语义: %#v", got)
	}
	// 临时文件不能残留。
	if _, err := os.Stat(l.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("残留 .tmp 文件")
	}
}

func TestLedgerCompactEmptyIsNoop(t *testing.T) {
	l := newTestLedger(t)
	if err := l.Compact(); err != nil {
		t.Fatalf("空台账 Compact 应无操作: %v", err)
	}
	if _, err := os.Stat(l.Path); !os.IsNotExist(err) {
		t.Fatal("空台账不应被创建")
	}
}

func TestLedgerLoadSkipsCorruptLines(t *testing.T) {
	l := newTestLedger(t)
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"email":"a@b.com","status":"keyed","api_key":"sk-1"}
这不是 JSON
{"email":"","status":"failed"}

{"email":"c@d.com","status":"enrolled"}
`
	if err := os.WriteFile(l.Path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := l.Load()
	if err != nil {
		t.Fatalf("坏行不应导致整份台账失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("有效记录数 = %d，期望 2: %#v", len(got), got)
	}
}

func TestRedactRecordKeepsNonSecrets(t *testing.T) {
	rec := &Record{
		Email: "a@b.com", Key: "a@b.com", Status: StatusEnrolled,
		APIKey: "sk-1", AccountID: "acc", Plan: "free",
		Password: "pw", SessionKey: "sk", CSRFToken: "csrf",
		Stages: map[string]string{"login": "ok"},
	}
	out := redactRecord(rec)

	if out.Password != "" || out.SessionKey != "" || out.CSRFToken != "" {
		t.Fatalf("敏感字段未清除: %#v", out)
	}
	if out.APIKey != "sk-1" || out.AccountID != "acc" || out.Plan != "free" {
		t.Fatalf("非敏感字段被误删: %#v", out)
	}
	if out.Stages["login"] != "ok" {
		t.Fatal("阶段信息应保留")
	}
	// 入参不受影响。
	if rec.Password != "pw" {
		t.Fatal("redactRecord 不应修改入参")
	}
	// 输出是深拷贝：改 map 不影响原记录。
	out.Stages["login"] = "mutated"
	if rec.Stages["login"] != "ok" {
		t.Fatal("redactRecord 应深拷贝 Stages")
	}
}

func TestSecretPathDerivation(t *testing.T) {
	cases := map[string]string{
		"exports/register-ledger.jsonl": "exports/register-ledger-credentials.jsonl",
		"ledger.txt":                    "ledger.txt.credentials.jsonl",
	}
	for in, want := range cases {
		l := NewLedger(in)
		if got := l.SecretPath(); got != want {
			t.Errorf("SecretPath(%q) = %q，期望 %q", in, got, want)
		}
	}
	if NewLedger("").SecretPath() != "" {
		t.Error("空路径应返回空")
	}
}

func TestDeliverablesRedactSecrets(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecord("a@b.com")
	rec.Status = StatusEnrolled
	rec.APIKey = "sk-deliver"
	rec.Password = "pw-should-not-leak"
	rec.AccountID = "acc-1"

	out, err := WriteDeliverables(dir, []*Record{rec})
	if err != nil {
		t.Fatalf("WriteDeliverables: %v", err)
	}

	raw, err := os.ReadFile(out.SuccessJSON)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pw-should-not-leak") {
		t.Fatalf("success.jsonl 泄漏了密码:\n%s", raw)
	}
	if !strings.Contains(string(raw), "sk-deliver") {
		t.Fatalf("success.jsonl 应保留 API Key:\n%s", raw)
	}
	// 每行必须仍是合法 JSON。
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("交付物含非法 JSON 行: %v\n%s", err, line)
		}
	}
}

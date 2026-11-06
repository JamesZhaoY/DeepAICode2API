package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
)

// newStore 在临时目录里打开一个空状态文件。
func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

func addAccount(t *testing.T, st *Store, key, group string) *deepai.Account {
	t.Helper()
	a := &deepai.Account{
		ID:      deepai.ShortHash(key)[:8],
		Name:    key,
		APIKey:  key,
		Group:   group,
		Enabled: true,
	}
	if !st.UpsertAccount(a) {
		t.Fatalf("账号 %s 应为新增", key)
	}
	return a
}

func TestOpenCreatesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s := st.Settings()
	if s.RequestTimeout <= 0 || s.MaxRetries <= 0 || s.LogKeep <= 0 {
		t.Fatalf("默认设置未填充: %#v", s)
	}
	if st.Stats().StartedAt == 0 {
		t.Fatal("StartedAt 应被初始化")
	}
	if st.HasKeys() {
		t.Fatal("新状态不应有密钥")
	}
	if got := st.Accounts(); len(got) != 0 {
		t.Fatalf("新状态不应有账号: %d", len(got))
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("损坏的状态文件应报错，而不是静默重置")
	}
}

func TestSaveAndReopenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	addAccount(t, st, "sk-aaa", "g1")
	k := st.AddKey("console", "")
	st.SetMachineID("machine-1")
	st.SetModels([]deepai.Model{{ID: "m1", Name: "M1"}}, "m1")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 状态文件必须只有属主可读写（仅 Unix 生效：Windows 上 os.WriteFile 的
	// mode 位不起作用，无法靠 chmod 收窄 ACL，需要加密兜底）。
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err == nil {
			if perm := fi.Mode().Perm(); perm&0o077 != 0 {
				t.Fatalf("状态文件权限过宽: %o", perm)
			}
		}
	}

	back, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	if len(back.Accounts()) != 1 || back.Accounts()[0].APIKey != "sk-aaa" {
		t.Fatalf("账号未持久化: %#v", back.Accounts())
	}
	if !back.ValidKey(k.Key) {
		t.Fatal("密钥未持久化")
	}
	if back.MachineID() != "machine-1" {
		t.Fatalf("机器指纹未持久化: %q", back.MachineID())
	}
	if back.DefaultModel() != "m1" {
		t.Fatalf("模型目录未持久化: %q", back.DefaultModel())
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	addAccount(t, st, "sk-1", "")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 临时文件必须已被 rename 掉，不能留下 .tmp。
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("残留临时文件: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("状态文件不是合法 JSON:\n%s", raw)
	}
}

func TestUpsertDedupesByKeyAndEmail(t *testing.T) {
	st := newStore(t)
	a := addAccount(t, st, "sk-same", "")

	// 同一 Key 再导入 → 更新而非新增，且 ID 保持不变。
	again := &deepai.Account{APIKey: "sk-same", Name: "renamed"}
	if st.UpsertAccount(again) {
		t.Fatal("同 Key 应为更新")
	}
	if again.ID != a.ID {
		t.Fatalf("更新后 ID 应保持 %q，实际 %q", a.ID, again.ID)
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("账号数 = %d，期望 1", got)
	}

	// 邮箱相同、Key 已轮换 → 仍视为同一账号。
	rotated := &deepai.Account{APIKey: "sk-new", Email: "x@y.com"}
	st.UpsertAccount(rotated)
	sameEmailAcct := &deepai.Account{APIKey: "sk-newer", Email: "X@Y.com"}
	if st.UpsertAccount(sameEmailAcct) {
		t.Fatal("同邮箱（大小写不同）应为更新")
	}
	if got := len(st.Accounts()); got != 2 {
		t.Fatalf("账号数 = %d，期望 2", got)
	}
}

func TestUpsertPreservesRuntimeState(t *testing.T) {
	st := newStore(t)
	a := addAccount(t, st, "sk-1", "")
	st.MarkAccountResult(a.ID, false, "boom")
	st.SetAccountAllowance(a.ID, &deepai.Allowance{Balance: "3"}, nil)

	// 重新导入（例如桌面端换了 Key）不应清空故障计数与配额缓存。
	st.UpsertAccount(&deepai.Account{APIKey: "sk-1", Name: "n"})
	got := st.Account(a.ID)
	if got == nil {
		t.Fatal("账号丢失")
	}
	if got.LastErr != "" {
		t.Fatalf("SetAccountAllowance 成功应清空 LastErr，实际 %q", got.LastErr)
	}
	if got.Theos == nil || got.Theos.Balance != "3" {
		t.Fatalf("配额缓存丢失: %#v", got.Theos)
	}
}

func TestNextAccountRotationIsFair(t *testing.T) {
	st := newStore(t)
	for _, k := range []string{"sk-a", "sk-b", "sk-c"} {
		addAccount(t, st, k, "")
	}

	seen := map[string]int{}
	for i := 0; i < 30; i++ {
		a := st.NextAccountGroup("")
		if a == nil {
			t.Fatal("应有可用账号")
		}
		seen[a.APIKey]++
	}
	// 轮询：30 次请求应被 3 个账号均分。
	for _, k := range []string{"sk-a", "sk-b", "sk-c"} {
		if seen[k] != 10 {
			t.Errorf("账号 %s 被选中 %d 次，期望 10；分布=%v", k, seen[k], seen)
		}
	}
}

func TestNextAccountSkipsFailedAndDisabled(t *testing.T) {
	st := newStore(t)
	good := addAccount(t, st, "sk-good", "")
	bad := addAccount(t, st, "sk-bad", "")
	off := addAccount(t, st, "sk-off", "")

	st.MarkAccountResult(bad.ID, false, "api down")
	off.Enabled = false // 与 /admin/api/accounts 的启停一致：直接改号池里的指针

	for i := 0; i < 10; i++ {
		if got := st.NextAccountGroup(""); got == nil || got.ID != good.ID {
			t.Fatalf("应只选到健康账号，实际 %#v", got)
		}
	}
}

func TestNextAccountFallsBackWhenAllFailed(t *testing.T) {
	// 全组都失败时不能返回 nil：宁可把请求打出去，也不要硬阻塞。
	st := newStore(t)
	a := addAccount(t, st, "sk-1", "")
	addAccount(t, st, "sk-2", "")
	st.MarkAccountResult(a.ID, false, "x")
	st.MarkAccountResult(a.ID, false, "x")

	if got := st.NextAccountGroup(""); got == nil {
		t.Fatal("全部失败时应回退到失败最少的账号，而不是 nil")
	}
}

func TestNextAccountGroupIsolation(t *testing.T) {
	st := newStore(t)
	addAccount(t, st, "sk-g1", "g1")
	addAccount(t, st, "sk-g2", "g2")

	for i := 0; i < 5; i++ {
		if got := st.NextAccountGroup("g1"); got == nil || got.GroupName() != "g1" {
			t.Fatalf("g1 分组取到了 %#v", got)
		}
	}
	if got := st.NextAccountGroup("missing"); got != nil {
		t.Fatalf("不存在的分组应返回 nil，实际 %#v", got)
	}
	// 空分组等价于“全部分组”。
	if got := st.NextAccountGroup(""); got == nil {
		t.Fatal("空分组应匹配全部账号")
	}
}

func TestNextAccountEmptyPool(t *testing.T) {
	st := newStore(t)
	if got := st.NextAccountGroup(""); got != nil {
		t.Fatalf("空号池应返回 nil，实际 %#v", got)
	}
}

func TestMarkAccountResultResetsFailures(t *testing.T) {
	st := newStore(t)
	a := addAccount(t, st, "sk-1", "")
	st.MarkAccountResult(a.ID, false, "boom")
	st.MarkAccountResult(a.ID, false, "boom2")
	if got := st.Account(a.ID); got.Failures != 2 || got.Status != "error" || got.LastErr != "boom2" {
		t.Fatalf("失败计数错误: %#v", got)
	}

	st.MarkAccountResult(a.ID, true, "")
	if got := st.Account(a.ID); got.Failures != 0 || got.Status != "ok" || got.LastErr != "" {
		t.Fatalf("成功后应清零: %#v", got)
	}
}

func TestRemoveAccount(t *testing.T) {
	st := newStore(t)
	a := addAccount(t, st, "sk-1", "")
	if !st.RemoveAccount(a.ID) {
		t.Fatal("删除应成功")
	}
	if st.RemoveAccount(a.ID) {
		t.Fatal("重复删除应返回 false")
	}
	if st.Account(a.ID) != nil {
		t.Fatal("账号仍存在")
	}
}

func TestKeysLifecycle(t *testing.T) {
	st := newStore(t)
	if st.HasKeys() {
		t.Fatal("空状态不应有密钥")
	}

	k := st.AddKey("cli", "grp")
	if k.Key == "" || k.Group != "grp" || !k.Enabled {
		t.Fatalf("密钥创建错误: %#v", k)
	}
	if !st.HasKeys() {
		t.Fatal("HasKeys 应为 true")
	}
	if !st.ValidKey(k.Key) {
		t.Fatal("有效密钥应通过校验")
	}
	if got := st.KeyGroup(k.Key); got != "grp" {
		t.Fatalf("KeyGroup = %q", got)
	}
	if st.KeyGroup("sk-unknown") != "" {
		t.Fatal("未知密钥的 KeyGroup 应为空")
	}

	// 计数：ValidKey 会累加使用次数。
	k2 := st.Keys()[0]
	if k2.Requests != 1 || k2.LastUsed == 0 {
		t.Fatalf("用量未累计: %#v", k2)
	}

	st.ToggleKey(k.Key)
	if st.ValidKey(k.Key) {
		t.Fatal("停用后不应通过校验")
	}
	if st.ToggleKey("sk-unknown") {
		t.Fatal("未知密钥 Toggle 应返回 false")
	}

	if !st.DeleteKey(k.Key) {
		t.Fatal("删除密钥应成功")
	}
	if st.DeleteKey(k.Key) {
		t.Fatal("重复删除应返回 false")
	}
	if st.HasKeys() {
		t.Fatal("删除后不应再有密钥")
	}
}

func TestAddKeyDefaultsToDefaultGroup(t *testing.T) {
	st := newStore(t)
	k := st.AddKey("x", "   ")
	if k.Group != "default" {
		t.Fatalf("空分组应归一为 default，实际 %q", k.Group)
	}
}

func TestRecordTrimsLogs(t *testing.T) {
	st := newStore(t)
	st.UpdateSettings(func(s *Settings) { s.LogKeep = 3 })

	for i := 0; i < 10; i++ {
		st.Record(LogEntry{Model: "m", Endpoint: "chat", Status: 200}, 10)
	}
	logs := st.Logs(0)
	if len(logs) != 3 {
		t.Fatalf("日志应被裁剪到 3 条，实际 %d", len(logs))
	}
	// 保留的应是最新的。
	if st.Stats().TotalRequests != 10 {
		t.Fatalf("统计不应被裁剪，实际 %d", st.Stats().TotalRequests)
	}
}

func TestRecordCountsFailures(t *testing.T) {
	st := newStore(t)
	st.Record(LogEntry{Status: 200}, 5)
	st.Record(LogEntry{Status: 502}, 0)
	s := st.Stats()
	if s.TotalRequests != 2 || s.FailedRequests != 1 {
		t.Fatalf("统计错误: %#v", s)
	}
}

func TestModelCatalog(t *testing.T) {
	st := newStore(t)
	// 未拉取目录时 DefaultModel 为空；兜底由 server.resolveModel 负责
	// （catalog 空 → deepai.DefaultModelEntry）。
	if got := st.DefaultModel(); got != "" {
		t.Fatalf("未设置目录时 DefaultModel 应为空，实际 %q", got)
	}
	st.SetModels([]deepai.Model{{ID: "a"}, {ID: "b"}}, "b")
	if st.DefaultModel() != "b" {
		t.Fatalf("DefaultModel = %q", st.DefaultModel())
	}
	if st.ModelsUpdated() == 0 {
		t.Fatal("ModelsUpdated 应被填充")
	}
	if len(st.Models()) != 2 {
		t.Fatalf("模型数 = %d", len(st.Models()))
	}

	// def 为空时回落到目录首项；已有默认值时不被空覆盖。
	st2 := newStore(t)
	st2.SetModels([]deepai.Model{{ID: "first"}}, "")
	if st2.DefaultModel() != "first" {
		t.Fatalf("空 def 应回落到首个模型，实际 %q", st2.DefaultModel())
	}
	st2.SetModels([]deepai.Model{{ID: "second"}}, "")
	if st2.DefaultModel() != "first" {
		t.Fatalf("已有默认值不应被空覆盖，实际 %q", st2.DefaultModel())
	}
}

func TestSettingsSnapshotIsCopy(t *testing.T) {
	st := newStore(t)
	st.UpdateSettings(func(s *Settings) {
		s.ModelAliases = map[string]string{"a": "b"}
		s.AdminPassword = "pw"
	})

	snap := st.Settings()
	snap.ModelAliases["a"] = "mutated"

	// map 是引用类型，必须被拷贝；标量字段由 Go 的值语义保证。
	if fresh := st.Settings(); fresh.ModelAliases["a"] != "b" {
		t.Fatal("Settings 返回的别名表应与内部状态隔离")
	}
	if fresh := st.Settings(); fresh.AdminPassword != "pw" {
		t.Fatal("Settings 快照不应影响内部状态")
	}
}

func TestConcurrentAccessIsRaceFree(t *testing.T) {
	// 配合 -race 跑：号池调度、密钥校验、日志写入、save 并发。
	st := newStore(t)
	for _, k := range []string{"sk-a", "sk-b"} {
		addAccount(t, st, k, "")
	}
	key := st.AddKey("k", "")

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				if a := st.NextAccountGroup(""); a != nil {
					st.MarkAccountResult(a.ID, j%2 == 0, "")
				}
				_ = st.ValidKey(key.Key)
				st.Record(LogEntry{Status: 200}, 1)
				_ = st.Accounts()
				_ = st.Settings()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if err := st.Save(); err != nil {
		t.Fatalf("并发后 Save: %v", err)
	}
}

func TestNextAccountPrefersAccountsWithQuota(t *testing.T) {
	// 上游按机器发每日额度，池里常有 balance=0 的账号；
	// 有额度的账号应被优先选中，避免每次都先被上游拒一次。
	st := newStore(t)
	empty := addAccount(t, st, "sk-empty", "")
	full := addAccount(t, st, "sk-full", "")
	now := time.Now().Unix()
	st.SetAccountAllowance(empty.ID, &deepai.Allowance{Enabled: true, Balance: "0.00000", CheckedAt: now}, nil)
	st.SetAccountAllowance(full.ID, &deepai.Allowance{Enabled: true, Balance: "2.50000", CheckedAt: now}, nil)

	for i := 0; i < 10; i++ {
		got := st.NextAccountGroup("")
		if got == nil || got.ID != full.ID {
			t.Fatalf("应优先选择有额度的账号，实际 %#v", got)
		}
	}
}

func TestNextAccountFallsBackToZeroQuota(t *testing.T) {
	// 全组都是 0 额度时不能返回 nil，否则请求直接失败。
	st := newStore(t)
	a := addAccount(t, st, "sk-a", "")
	b := addAccount(t, st, "sk-b", "")
	now := time.Now().Unix()
	st.SetAccountAllowance(a.ID, &deepai.Allowance{Enabled: true, Balance: "0", CheckedAt: now}, nil)
	st.SetAccountAllowance(b.ID, &deepai.Allowance{Enabled: true, Balance: "0.0", CheckedAt: now}, nil)

	if got := st.NextAccountGroup(""); got == nil {
		t.Fatal("全组 0 额度时应回退，而不是返回 nil")
	}
}

func TestNextAccountUnknownQuotaIsUsable(t *testing.T) {
	// 未探测 / 解析不了 / 免费额度关闭，都不能算“没额度”。
	cases := []*deepai.Allowance{
		nil,
		{Enabled: true, Balance: "abc"},
		{Enabled: true, Balance: ""},
		{Enabled: false, Balance: "0"},
	}
	for _, al := range cases {
		st := newStore(t)
		a := addAccount(t, st, "sk-1", "")
		st.SetAccountAllowance(a.ID, al, nil)
		if got := st.NextAccountGroup(""); got == nil || got.ID != a.ID {
			t.Fatalf("额度状态 %#v 不应导致账号被跳过", al)
		}
	}
}

func TestUpsertPreservesMachineID(t *testing.T) {
	st := newStore(t)
	a := addAccount(t, st, "sk-1", "")
	a.MachineID = "machine-keep"

	// 重新导入（例如桌面端刷新了 Key）不应清掉机器指纹，
	// 否则该账号在上游的额度归属会断掉。
	st.UpsertAccount(&deepai.Account{APIKey: "sk-1", Name: "n"})
	if got := st.Account(a.ID); got.MachineID != "machine-keep" {
		t.Fatalf("机器指纹被清空: %q", got.MachineID)
	}

	// 新值非空时应可以覆盖。
	st.UpsertAccount(&deepai.Account{APIKey: "sk-1", MachineID: "machine-new"})
	if got := st.Account(a.ID); got.MachineID != "machine-new" {
		t.Fatalf("新指纹未生效: %q", got.MachineID)
	}
}

func TestSaveSkipsUnchangedWrites(t *testing.T) {
	// 后台每 30s 一次 Save；内容没变时不应反复重写整个状态文件。
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	addAccount(t, st, "sk-1", "")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// 内容不变 → 跳过（mtime 不变）。
	time.Sleep(10 * time.Millisecond)
	if err := st.Save(); err != nil {
		t.Fatalf("二次 Save: %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("内容未变化时不应重写文件")
	}

	// 内容变化 → 必须落盘。
	st.AddKey("new", "")
	if err := st.Save(); err != nil {
		t.Fatalf("变更后 Save: %v", err)
	}
	third, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if third.ModTime().Equal(second.ModTime()) {
		t.Fatal("内容变化后必须重写文件")
	}
	back, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	if !back.HasKeys() {
		t.Fatal("变更未持久化")
	}
}

func TestZeroQuotaGoesStale(t *testing.T) {
	// 注册瞬间的 0 额度不能把账号永久判死；陈旧的 0 要重新被视为“未知”。
	now := time.Now().Unix()

	fresh := &deepai.Account{ID: "a", Enabled: true,
		Theos: &deepai.Allowance{Enabled: true, Balance: "0.00000", CheckedAt: now}}
	if hasQuota(fresh) {
		t.Fatal("刚探到的 0 额度不应算可用")
	}

	stale := &deepai.Account{ID: "b", Enabled: true,
		Theos: &deepai.Allowance{Enabled: true, Balance: "0.00000",
			CheckedAt: now - int64(QuotaStaleAfter/time.Second) - 60}}
	if !hasQuota(stale) {
		t.Fatal("超过时限的 0 额度应视为未知（允许重探）")
	}

	// 没有探测时间戳的 0：按未知处理。
	noStamp := &deepai.Account{ID: "c", Enabled: true,
		Theos: &deepai.Allowance{Enabled: true, Balance: "0"}}
	if !hasQuota(noStamp) {
		t.Fatal("无 CheckedAt 的 0 额度应按未知处理")
	}
}

func TestStaleZeroQuotaIsSchedulable(t *testing.T) {
	// 端到端：陈旧的 0 额度账号必须能被调度到。
	st := newStore(t)
	a := addAccount(t, st, "sk-stale", "")
	st.SetAccountAllowance(a.ID, &deepai.Allowance{
		Enabled: true, Balance: "0.00000",
		CheckedAt: time.Now().Add(-time.Hour).Unix(),
	}, nil)
	if got := st.NextAccountGroup(""); got == nil || got.ID != a.ID {
		t.Fatalf("陈旧的 0 额度账号应可被调度，实际 %#v", got)
	}
}

# 优化方案（施工前计划）

> **状态：已全部实施。** 本文是施工前的计划原文，保留作为设计依据与验收标准；
> 实际的根因、决策与验证记录见 [`changes.md`](changes.md)，当前行为以 README 为准。
> 文中出现的行号 / 文件名是当时的快照（例如 `store/seal.go` 后来更名为
> `store/state_seal.go`），不作为现状参考。

针对首轮代码扫描暴露的 6 类问题，按优先级给出可落地的改造方案。
每条都标注了**涉及的现有文件/行**、**根因**与**验收标准**，可直接拆成 issue。

优先级说明：P0 = 安全/可维护性硬缺口，P1 = 功能正确性，P2 = 工程卫生。

### 实施进度

| 编号 | 主题 | 状态 | 提交 |
| --- | --- | --- | --- |
| P0-2 | 无密钥不再对非回环放行 | ✅ 已实施 | `fix(security): 未签发密钥时不再对非回环来源放行` |
| P0-3 | 测试覆盖 | ✅ 已实施 | `test: 补 6 个包的回归测试`（顺带修 `Merge` 浅拷贝 bug） |
| P0-1 | 状态文件加密 + 台账脱敏 | ✅ 已实施 | `feat(security): 状态文件可加密 + 台账脱敏` |
| P1-1 | 账号级机器指纹 | ✅ 已实施 | 见本文 §P1-1，含 `assign-machine` 补齐动作 |
| P2-1 | 推理侧指纹与注册侧一致 | ✅ 已实施 | `Account` 增 UA / Accept-Language / sec-ch-ua / platform |
| P2-2 | 仓库与构建卫生 | ✅ 已实施 | Makefile / LICENSE / docs/recon.md |
| P2-3 | 其他可改进点 | ✅ 已实施 | Save 跳过无变化写盘、action 常量、错误体截断、README 已知坑 |
| 计划外 | 非流式请求显式 `stream=false` | ✅ 已修复 | `fix(chat)`：上游缺省 `stream` 时默认返回 SSE |
| 计划外 | 入池 0 额度延迟复探 | ✅ 已修复 | `fix(enroll)`：3/6/12 分钟退避 + 15 分钟陈旧判定 |
| 计划外 | Windows `go test -race` 链接失败 | ✅ 已修复 | `scripts/race-shim.sh` 补 `ld.exe` |

---

## P0-1 凭据面收敛：明文落盘 + 权限形同虚设

### 现状

| 资产 | 路径 | 权限 | 问题 |
| --- | --- | --- | --- |
| 上游 API Key | `deepaicode2api-data.json` | `0o600`（`store.go:154`） | 明文；Windows 上 `os.WriteFile` 的 mode 位**基本无效**，同机任意用户可读 |
| 网页账号密码 | 同上 `Account.Password` | 同上 | 明文，且 `/admin/api/state` 会回传给控制台 |
| 代理账密 | `.env` `PROXY_POOL` | 默认 | 明文 |
| 注册台账 | `exports/register-ledger.jsonl`（`ledger.go` 的 `appendLine`） | `0o600` | 明文密码 + API Key，append-only 永不过期 |
| 交付物 | `result/{keys,apikeys}.txt`、`success.jsonl`（`deliver.go`） | `0o600` | 明文 API Key |

`.gitignore` 已排除以上全部路径（本次已校验），**泄露风险不在 git，而在磁盘与备份**。

### 方案

#### A. 状态文件加密封装（约 90 行，stdlib only）

新增 `internal/store/state_seal.go`（原 `seal.go`）：

```go
// 文件格式 v2：{"v":2,"alg":"aes-256-gcm","nonce":"b64","ct":"b64"}
// v1（裸 JSON）仍可读，读到即迁移。
func seal(key []byte, plain []byte) ([]byte, error)
func unseal(key []byte, raw []byte) ([]byte, bool, error) // ok=false 表示裸 JSON
```

- 密钥来源：`DEEPAI2API_MASTER_KEY`（32 字节 hex/base64）。
- 未设置时：沿用明文写入，但启动日志打 `⚠ 状态文件未加密（含上游 Key）`，并在控制台顶部显示常驻横幅。
- `Open()` 里做版本判定 + 回写迁移；`Save()` 走 `seal`，仍然 `tmp + rename` 原子写。

#### B. Windows 权限兜底

`os.Chmod` 在 Windows 只切只读位，无 ACL 能力。二选一，建议同时做：

1. 状态文件默认路径从「可执行文件同目录」（`main.go:defaultDataPath`）改为
   `os.UserConfigDir()/deepaicode2api/` —— 落在用户私有目录，天然隔离。
2. 启动时若 `runtime.GOOS == "windows"` 且文件位于 `%ProgramFiles%`/共享目录，
   打警告并建议迁移。

#### C. 台账脱敏

- `register.Record.Password` 改为仅在内存与 `-resume` 时需要：
  `Append()` 前用 `redactRecord(rec)` 把 `Password`/`SessionKey`/`CSRFToken` 换成 `"***"`，
  原值单独写 `exports/.credentials.jsonl`（`0o600`，独立生命周期，可整体删除）。
- 台账加保留窗口（默认 30 天或最近 2000 行），`Ledger.Prune()` 在 `RunBatch` 结束时调用。
- `success.jsonl` 只保留 `email/api_key/plan/theos_*`，不含密码与 session。

**验收**：`grep -c '\"password\":\"[^*]' exports/*.jsonl` 为 0；
未设置 `DEEPAI2API_MASTER_KEY` 时启动日志出现警告；设置后文件头为 `{"v":2`，`unseal` 后内容与原来等价。

---

## P0-2 无密钥即放行：开放中继

### 现状

`internal/server/server.go:192` `withAuth`：`if !s.Store.HasKeys() { next(w, r); return }`。
默认绑 `127.0.0.1` 时是便利，但 `docker-compose.yml` 与 `Dockerfile` 都是 `0.0.0.0`，
**首次启动到创建第一个 key 之间是完全开放的**，且 `DEEPAI2API_ADMIN_PASSWORD` 为空时管理面也只剩回环校验。

### 方案

#### A. 修复「无 key」语义（改 `withAuth`，约 15 行）

```go
if !s.Store.HasKeys() {
    if isLoopbackAddr(r.RemoteAddr) { next(w, r); return }   // 本机仍免密，便于自举
    writeErr(w, http.StatusServiceUnavailable,
        "网关尚未签发 API 密钥，请在本机控制台创建后重试（/admin）", "setup_required")
    return
}
```

#### B. 启动自举密钥（改 `main.go`，约 20 行）

非回环监听且 `!HasKeys()` 时，自动 `st.AddKey("bootstrap", "default")`，
把完整 key **只打印一次**到 stdout，并提示「可在控制台改名/吊销」。
这样「无 key」状态在公网场景不再存在。

#### C. 不安全配置硬失败

`main.go` 启动校验：监听地址非回环 + `AdminPassword == ""` + 未设置 `DEEPAI2API_ALLOW_INSECURE=1`
→ `log.Fatalf`。也让 `register.exe -gateway` 的默认行为保持一致。

**验收**：`curl -H '' http://<局域网IP>:10087/v1/models` 返回 503 `setup_required`；
本机 `curl 127.0.0.1:10087/v1/models` 仍可用；`-host 0.0.0.0` 且无管理密码时进程拒绝启动。

---

## P0-3 零测试覆盖

当前 `find . -name '*_test.go'` 为 0，而最容易回归的三块是 **SSE 解析**、**双协议转换**、**账号调度**。

### 方案：按「纯函数优先」补 6 个测试文件（约 600 行，全部 `httptest` + `-race`，无第三方断言库）

| 文件 | 覆盖点 | 关键用例 |
| --- | --- | --- |
| `internal/deepai/sse_test.go` | `readSSELines`（`sse.go`） | CRLF、多行 `data:`、`: comment`、`event:`、半包/截断、末尾无空行、`[DONE]` |
| `internal/deepai/client_test.go` | `HTTPError` 语义（`client.go:51-92`） | 409 `session_busy` → `CapacityWaiting` 且 `RetryAfter` 取头部；401 → `AuthFailed`；426 `lane_version_unsupported` → 不可重试；请求头含 `Api-Key`/`X-DeepAI-Code-Theos`/`X-DeepAI-Code-Machine` |
| `internal/server/anthropic_test.go` | `v1/messages` 双向映射（`anthropic.go`） | system 数组、`tools`/`tool_choice`、图片 block、`stop_sequences`；上游 SSE → `message_start/content_block_delta/tool_use/message_stop` 事件序列与 `index` 连续性 |
| `internal/server/responses_test.go` | `v1/responses` 映射（`responses.go`） | `input` 字符串 vs 数组、`instructions` → system、`function_call` item、`stream` 开关、`usage` 字段透传 |
| `internal/store/store_test.go` | 调度与持久化（`store.go`） | 轮询公平性（N 次内每个账号都被选中）、`Failures>0` 被跳过、分组隔离、key 停用后 `ValidKey` 拒绝、`Save`/`Open` 往返等价、并发 `Record` 无 race |
| `internal/register/types_test.go` | 台账合并（`types.go:93-220`） | `Merge` 幂等且「等级高者胜」、`Normalize` 补默认值、`StatusRank` 单调、`WorthDelivering` 只认有 key |

配套（本次已加）：`.github/workflows/ci.yml` 跑 `gofmt -l` / `go vet` / `go build` / `go test -race ./...`。

**验收**：`go test -race ./...` 全绿；故意把 `readSSELines` 的 `\r` 裁剪删掉、把 anthropic `index` 递增改错，CI 必须变红。

---

## P1-1 所有账号 Theos 均为 0（最大功能缺口）

### 根因（已定位到代码）

`internal/deepai/client.go:127` `NewClientWithFingerprint` 里：

```go
Machine: MachineID(),   // 进程级全局单例（machine.go 的 sync.Once）
```

而上游的免费额度是 **按 `X-DeepAI-Code-Machine` 每天发一次**。所以：

- 21 个账号共用 1 个机器指纹 → 每天最多 1 次 grant；
- 这个 grant 已被桌面端主账号拿走了；
- 记录显示 `enabled:true` 但 `balance=0.00000 granted=0.00000` —— key 有效、只是没额度，与日志一致。

**并且**：注册链路虽然有一套完整浏览器指纹（`register/fingerprints.go`），但只用于网页注册
（`web.go`），`Record` 与 `Account` 里都**没有 machine 字段**，推理侧完全用全局值，所以注册时怎么换 IP/UA 都拿不到独立额度。

### 方案（分三步，第 1 步是关键）

#### 1. 账号级机器指纹

- `deepai.Account` 增 `MachineID string`（存原始 UUID，使用时 sha256）；`register.Record` 同步增字段。
- `pipeline.go` 注册时用 `crypto/rand` 生成稳定 UUID（与代理/指纹一起固定），网页注册与后续 key 验活**全程使用同一个** machine 头。
- `deepai.Client` 增 `NewClientFor(proxy, ua, machine string)`；`Machine` 字段从「全局」改为「按账号」。
- `server.clientFor` 的缓存键由 `proxy` 改为 `proxy|machine|ua`（否则同一代理下所有账号仍共用客户端）。
- 回填：`cmd/register -resume` 为存量账号补齐 machine（生成后立即 `handleAdminCheckin` 复验）。

#### 2. 让额度参与调度

`store.NextAccountGroup` 目前注释明确写「Theos 只做展示，不做调度」。改为：
两轮扫描 —— 第一轮只挑 `Theos.Balance > 0 || Theos.Enabled == false`，第二轮才放宽。
这样 0 额度账号不会持续吃掉请求再被上游拒。

#### 3. 额度刷新闭环

`handleAdminCheckin` 现在是**串行**探测，20 个账号 × 30s 超时 = 最坏 10 分钟。
改为 `errgroup` 限流 4 并发；并加一个「刷新间隔 > 6h 才重探」的节流，避免把探测本身打成风控特征。

**验收**：新注册 N 个账号后，`/admin/api/checkin` 中至少多数账号 `granted > 0`；
`/admin/api/state` 能看到每个账号独立 machine；0 额度账号在有多余可用账号时不被调度。

> 若上游已收紧策略（同 IP 段 / 同 Key 前缀仍不发额度），第 1 步至少能让「付费额度」与
> 「免费额度」两条路分离，且不再污染主账号的 machine 指纹 —— 这是必须修的正确性问题。

---

## P2-1 推理侧指纹与注册侧不一致

`clientFor` → `NewClientWithProxy(acct.Proxy)`（`client.go:122`）**丢掉了 UserAgent**，
而注册时用的是完整浏览器指纹（UA / `Accept-Language` / `sec-ch-ua` / `sec-ch-ua-platform`）。
同一个 key 先用 Chrome 156 注册、再用空 UA 推理，是可被识别的异常信号。

方案：`Account` 增 `UserAgent` / `FingerprintID`（注册时写入），`clientFor` 构造客户端时带上；
`deepai.Client.commonHeaders` 补 `Accept-Language` 与 `sec-ch-ua*`（与 `register/web.go:185-196` 对齐）。

---

## P2-2 仓库与构建卫生

| 项 | 现状 | 方案 |
| --- | --- | --- |
| 版本控制 | 无 git（本次已 init，39 文件 / 10.4k 行） | 分支保护 + Conventional Commits；`main` 只接受 PR |
| 逆向产物 | `deepai.org.har`（1 MB）在磁盘 | 已 ignore。把结论沉淀成 `docs/recon.md`（纯文本、可检索），HAR 只留本地 |
| 构建产物 | `*.exe` 各 10 MB 在目录里 | 已 ignore。加 `Makefile`（`make build/register/test/lint`），产物统一进 `bin/` |
| CI | 无 | 已加 `.github/workflows/ci.yml` |
| 许可证 | 无 | 按用途补 `LICENSE`（仅供个人研究 / 不提供担保的声明） |
| 依赖 | 零依赖（仅 `go.mod`） | 保持零依赖是卖点，写进 README「设计约束」一节 |
| 状态文件路径 | 可执行文件同目录（`main.go:defaultDataPath`） | 改 `os.UserConfigDir()`（与 P0-1 B 同一条改动） |

---

## P2-3 其他可改进点

1. `main.go:background` 每 30s 全量 `Save()` —— 状态里含 500 条日志，高频写盘。
   改为「脏标记 + 变更后合并写」，或把日志拆到独立文件（日志本就无审计价值，`LogKeep=500` 可降到 200）。
2. `handleAdminAccounts` 的 `action` 分支（`server.go:442`）用字符串魔法值，建议提成常量并对未知 action 返回 400。
3. `deepai.HTTPError.Error()` 会把上游原始 body 拼进错误串，再写入 `Account.LastErr` 与请求日志 → 上游可能回显 prompt 片段。
   改为只保留 `code` + 截断的 `body[:200]`。
4. `Client.CheckRedirect` 返回 `ErrUseLastResponse`（`client.go:154`）是好设计，建议在 README 的「坑」里写明原因，防止后人「修好」它。

---

## 建议执行顺序

```text
第 1 批（半天）   P0-2 开放中继 + CI + P2-3.3 错误体截断
第 2 批（1 天）   P0-3 六个测试文件（先 anthropic/store，再 sse/responses）
第 3 批（1 天）   P0-1 状态文件加密 + 台账脱敏
第 4 批（1~2 天）P1-1 账号级机器指纹（含 clientFor 缓存键改造）+ 额度参与调度
第 5 批           P2-1 指纹一致性 + P2-2 收尾
```

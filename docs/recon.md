# 逆向结论速查

内容来自对 DeepAI Code 桌面端（Electron + 本地 t3code server）的黑盒观察，
以及 `deepai.org.har` 抓包。**本文件是 HAR 的替代品**：HAR 体积大且含本人
凭据，不入库；结论以文本形式沉淀在这里，便于检索与 diff。

> 抓包时间：应用版本 0.3.17。上游随时可能变，字段以代码为准。

## 1. 进程与数据目录

| 项 | 值 |
| --- | --- |
| 应用形态 | Electron 壳（`app.asar`）+ 独立 server（`server.asar`，约 200 MB） |
| 数据目录 | `T3CODE_HOME` 或 `~/.deepai-code`；状态目录 `<base>/userdata` |
| 凭据文件 | `<stateDir>/secrets/deepai-account.bin` |
| 凭据内容 | `{"apiKey":"…","username":"…","isPro":false}`，**明文 JSON**，权限 0600 |
| API Key 形态 | UUID（`8-4-4-4-12`） |

自动探测顺序见 `internal/deepai/helpers.go` 的 `DesktopSecretDirs`。

## 2. 上游端点

基础地址 `https://api.deepai.org`（可用 `DEEPAI_API_ORIGIN` 覆盖）。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/deepai_code/chat/completions` | 推理（OpenAI chat 兼容） |
| POST | `/deepai_code/responses` | 推理（仅 `api=openai-responses` 的模型） |
| GET | `/deepai_code/models` | 模型目录（无需 API Key） |
| GET | `/deepai_code/theos` | 配额 / 余额 |
| POST | `/deepai_code/web_search` | 联网搜索 |
| POST | `/deepai_code/find_service` | 服务发现 |
| GET | `/deepai_code/sessions/list` | 会话列表 |
| GET | `/deepai_code/desktop_auth/?state=…&port=…` | 浏览器登录起点 |
| POST | `/deepai_code/desktop_auth/exchange/` | 用 code 换 Key |
| GET | `/get_usage_summary` | 订阅与用量（根路径，不在 `/deepai_code` 下） |
| POST | `/speech_to_text` | 语音转写（表单字段 `voiceRecording`） |
| POST | `/chat_attachments/upload` | 附件上传 |

## 3. 请求头

| 头 | 值 | 作用 |
| --- | --- | --- |
| `Api-Key` | 账号 API Key | **鉴权走这个头，不是 `Authorization`** |
| `X-DeepAI-Code-Theos` | `2` | 计费车道版本 |
| `X-DeepAI-Code-Machine` | `sha256(小写原始机器 UUID)` | 按天发放免费额度 |
| `X-Session-Affinity` | 任意会话 id | 会话粘性（body 的 `prompt_cache_key` 同效） |

要点：

- 机器指纹是**额度分配键**：多个账号共用一个指纹时，只有第一个能拿到当日额度。
  本项目因此给每个账号分配独立 UUID（见 README §6.9）。
- 原始 UUID 来源：Windows `Win32_ComputerSystemProduct.UUID`（失败回退 `MachineGuid`）、
  macOS `IOPlatformUUID`、Linux `/sys/class/dmi/id/product_uuid` 或 `/etc/machine-id`。
- 固件占位 UUID（全 0 / 全 f / `03000200-…`）必须过滤，否则指纹不稳定。

## 4. 错误语义

| 状态 | body | 含义 | 网关处理 |
| --- | --- | --- | --- |
| 409 | `{"error":{"code":"session_busy"}}` + `Retry-After` | 容量拒绝 | 按 `Retry-After` 等待后**换号重试** |
| 401 | — | 鉴权失败 | 记失败、换号 |
| 403 + `email_unverified` | — | 邮箱未验证 | 视为鉴权失败 |
| 426 + `lane_version_unsupported` | — | 车道版本不对 / 缺机器指纹 | 不重试，直接上报 |
| 400 / 404 / 422 | — | 请求本身的问题 | 不重试 |

`Retry-After` 会被夹到 30s 上限（`deepai.RetryAfter`）。

## 5. 已知坑

- **不要跟随上游 302**：无效 Key 会跳登录页，跟随会把鉴权失败伪装成 200。
  `deepai.Client` 的 `CheckRedirect` 返回 `ErrUseLastResponse`，是刻意的。
- 上游把思维链放在 `reasoning`；OpenAI 生态常用 `reasoning_content`，
  网关会补一份（已存在则不覆盖）。
- `models` 响应里的默认模型字段是 `default`（不是 `default_model`）。
- `/theos` 返回的 `balance` / `granted` 是**字符串**，不是数字。
- SSE 里同一事件可能有多行 `data:`，按规范用 `\n` 拼接后再解析。

## 6. 复现方法

```bash
# 读取桌面端凭据并探测（不启动网关）
go run ./cmd/probe -usage

# 用目录里的默认模型跑一次对话
go run ./cmd/probe -prompt "reply OK" -stream

# 直接查配额
go run ./cmd/probe -usage
```

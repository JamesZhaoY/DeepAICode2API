# DeepAICode2API

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue)](LICENSE)

DeepAI Code 的 Go 网关，提供 OpenAI 和 Anthropic 兼容接口、账号池以及管理控制台。它可以导入现有账号，也可以使用 Hotmail / Outlook 邮箱通过 Microsoft Graph 完成注册和邮件验证。

## 功能

- 提供 OpenAI 兼容的 Chat Completions、Responses、Models 和音频转写接口，以及 Anthropic Messages 接口。
- 按分组管理账号和网关密钥，支持配额查询、轮询、故障切换和请求日志。
- 通过控制台导入 Graph 邮箱、批量注册并查看任务进度和日志；已注册及结果待确认的邮箱会自动跳过。
- 使用独立管理登录页、管理密码和同源写入检查；可使用 AES-256-GCM 加密网关状态文件。
- 单个 Go 可执行文件，控制台资源内嵌；支持 Docker Compose。

## 快速开始

需要 Go 1.24 或 Docker Compose。复制配置示例后，设置管理密码与状态文件加密主密钥：

```bash
cp .env.example .env
# 编辑 .env，设置 DEEPAI2API_ADMIN_PASSWORD 和 DEEPAI2API_MASTER_KEY
# 例如可用 openssl rand -hex 32 生成 32 字节的十六进制主密钥
```

使用 Docker Compose：

```bash
docker compose up -d --build
```

打开 [http://127.0.0.1:10087/](http://127.0.0.1:10087/)，输入配置的管理密码后进入控制台。Compose 默认只监听本机；如需远程使用，请通过 HTTPS 反向代理或 SSH 隧道访问。

也可直接编译：

```bash
go build -o bin/deepaicode2api .
./bin/deepaicode2api
```

管理页面中可以导入本机桌面端账号、添加上游 API Key、创建网关 API 密钥，以及管理注册任务。部分机器或容器环境需要设置 `DEEPAI_MACHINE_ID`；其他配置见 [.env.example](.env.example)。

## Hotmail 邮箱注册

在控制台的「邮箱与注册」页面导入 UTF-8 文本文件，每行一个邮箱：

```text
邮箱地址----邮箱密码----client_id----refresh_token
```

Graph 使用 `client_id` 和 `refresh_token` 授权；令牌需要具有 Microsoft Graph 委托 `Mail.Read` 和 `offline_access` 权限。多次导入会按邮箱地址合并；重复地址使用最新导入的凭据，已有邮箱状态保留。文件导入后，可在页面设置注册数量（最多 1,000）、并发数（最多 10）和代理池。

邮箱状态分为「可用」「已注册」和「待确认」。注册请求发生网络中断等无法确定结果的情况时，邮箱会标为待确认，避免自动重复注册。邮箱分配状态会持久保存，重新导入时保留原有状态。不同服务实例的状态不会自动同步，不应让多个实例同时使用同一份邮箱池。

命令行工具通过 `DEEPAI_GRAPH_MAIL_FILE` 读取同样格式的邮箱文件：

```bash
go build -o bin/register ./cmd/register
./bin/register -doctor
./bin/register -count 3 -concurrency 2
```

## API

| 接口 | 用途 |
| --- | --- |
| `GET /v1/models` | 查询模型目录 |
| `POST /v1/chat/completions` | OpenAI 兼容对话 |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic 兼容消息 |
| `POST /v1/messages/count_tokens` | 估算 token 数 |
| `POST /v1/audio/transcriptions` | 音频转写 |
| `POST /v1/deepai/web_search` | 联网搜索 |
| `GET /health` | 服务健康信息 |
| `/admin/api/*` | 受管理密码保护的管理接口 |

客户端请求网关接口时，按需使用控制台创建的 API 密钥：

```bash
curl http://127.0.0.1:10087/v1/models \
  -H 'Authorization: Bearer <网关API密钥>'
```

模型目录由上游提供；可用模型、配额和接口行为可能随上游变化。

## 数据与凭据

`.env`、`data/`、注册台账和交付物均已加入 `.gitignore`。不要将管理密码、上游 API Key、邮箱文件或代理凭据提交到仓库。

`DEEPAI2API_MASTER_KEY` 加密网关状态文件，但导入的 Graph 邮箱文件和注册凭据台账仍以本地文件形式保存，包含敏感信息。请限制文件和备份的访问权限，并分别保管加密主密钥。

## 开发与测试

```bash
go test ./...
go test -race ./internal/register ./internal/server
```

原项目来源：[2951461586/DeepAICode2API](https://github.com/2951461586/DeepAICode2API)。本仓库在原项目基础上增加 Graph 邮箱注册、邮箱状态管理和控制台改进。

## 许可证

[MIT](LICENSE)

# CLIProxyAPI Plus 使用指南

本文档涵盖从零开始的部署、配置、登录、模型管理和日常运维操作。

---

## 目录

- [快速开始](#快速开始)
- [目录结构](#目录结构)
- [配置文件详解](#配置文件详解)
- [启动与停止](#启动与停止)
- [OAuth 登录](#oauth-登录)
- [API Key 直接配置](#api-key-直接配置)
- [查看模型列表](#查看模型列表)
- [发送聊天请求](#发送聊天请求)
- [模型别名与排除](#模型别名与排除)
- [代理与网络](#代理与网络)
- [管理面板](#管理面板)
- [日志与调试](#日志与调试)
- [Docker 运维](#docker-运维)
- [本地开发（不用 Docker）](#本地开发不用-docker)
- [常见问题](#常见问题)

---

## 快速开始

**30 秒启动**（需要 Docker）：

```bash
# 1. 克隆项目
git clone https://github.com/router-for-me/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus

# 2. 准备配置
cp config.example.yaml config.yaml

# 3. 编辑 config.yaml，至少设置 api-keys（见下文）

# 4. 构建并启动
docker compose build && docker compose up -d

# 5. 确认运行中
curl http://localhost:8317/
# 输出: {"endpoints":["POST /v1/chat/completions","POST /v1/completions","GET /v1/models"],"message":"CLI Proxy API Server"}
```

---

## 目录结构

```
CLIProxyAPIPlus/
├── config.yaml              # 你的配置文件（需自己创建）
├── config.example.yaml      # 配置模板
├── docker-compose.yml       # Docker Compose 配置
├── Dockerfile               # Docker 构建文件
├── auths/                   # OAuth 令牌存储目录
│   └── *.json               # 各 provider 的 OAuth token 文件
├── logs/                    # 运行日志
└── cmd/server/main.go       # 服务入口（本地运行时用）
```

**Docker 容器内的映射关系**：

| 宿主机路径 | 容器内路径 | 用途 |
|------------|-----------|------|
| `./config.yaml` | `/CLIProxyAPI/config.yaml` | 主配置 |
| `./auths/` | `/root/.cli-proxy-api/` | OAuth 令牌 |
| `./logs/` | `/CLIProxyAPI/logs/` | 运行日志 |

---

## 配置文件详解

### 最小可用配置

```yaml
# config.yaml
port: 8317

# 客户端 API 密钥（随便取名，用于访问本代理）
api-keys:
  - "my-secret-key-1"
  - "my-secret-key-2"
```

设置好 `api-keys` 后，客户端用这些 key 来调用你的代理服务器。

### 核心配置项

```yaml
# 监听地址，空字符串 = 监听全部网卡
host: ""
port: 8317

# 客户端 API 密钥
api-keys:
  - "my-secret-key-1"

# 网络代理（可选，支持 socks5/http/https）
proxy-url: "socks5://user:pass@192.168.1.1:1080"

# 请求重试次数（遇到 403/408/500/502/503/504 时自动重试）
request-retry: 3

# 路由策略：round-robin（轮询）或 fill-first（优先填满一个）
routing:
  strategy: "round-robin"

# 调试模式
debug: false
```

---

## 启动与停止

### Docker 方式（推荐）

```bash
# 构建镜像（修改代码后需要重新构建）
docker compose build

# 启动（后台运行）
docker compose up -d

# 停止
docker compose down

# 重启（配置文件修改后）
docker compose restart

# 查看运行状态
docker ps --filter "name=cli-proxy"

# 查看日志（实时）
docker logs -f cli-proxy-api-plus

# 查看最近 50 行日志
docker logs --tail 50 cli-proxy-api-plus
```

### 完整重建（代码有改动时）

```bash
docker compose down
docker compose build --no-cache
docker compose up -d
```

### 从远程镜像拉取（不需要本地构建）

```bash
# docker-compose.yml 默认 pull_policy: always
docker compose pull && docker compose up -d
```

---

## OAuth 登录

OAuth 登录需要在容器内执行命令。每个 provider 使用不同的登录命令。

### 通用步骤

```bash
# 进入容器
docker exec -it cli-proxy-api-plus sh

# 执行登录命令（见下方各 provider）
./CLIProxyAPIPlus --login        # 示例：Google/Gemini 登录

# 退出容器
exit
```

> **提示**：OAuth 令牌会保存到 `auths/` 目录，容器重启不会丢失。

### 各 Provider 登录命令

| Provider | 命令 | 说明 |
|----------|------|------|
| **Google/Gemini** | `--login` | Google OAuth，支持 Gemini CLI 模型 |
| **Claude** | `--claude-login` | Anthropic OAuth |
| **Codex (OpenAI)** | `--codex-login` | OpenAI OAuth |
| **GitHub Copilot** | `--github-copilot-login` | GitHub Device Flow 登录 |
| **Kiro** | `--kiro-login` | Kiro Google OAuth |
| **Kiro (Google)** | `--kiro-google-login` | 等同于 `--kiro-login` |
| **Kiro (AWS)** | `--kiro-aws-login` | AWS Builder ID (Device Code Flow) |
| **Kiro (AWS AuthCode)** | `--kiro-aws-authcode` | AWS Builder ID (Authorization Code，体验更好) |
| **Kiro (导入)** | `--kiro-import` | 从 Kiro IDE 导入令牌 |
| **Qwen** | `--qwen-login` | 阿里通义千问 OAuth |
| **iFlow** | `--iflow-login` | iFlow OAuth |
| **iFlow (Cookie)** | `--iflow-cookie` | iFlow Cookie 登录 |
| **Antigravity** | `--antigravity-login` | Antigravity OAuth |
| **Kimi** | `--kimi-login` | Moonshot Kimi OAuth |
| **Vertex** | `--vertex-import <file>` | 导入 Vertex 服务账号 JSON |

### GitHub Copilot 登录示例

```bash
docker exec -it cli-proxy-api-plus sh
./CLIProxyAPIPlus --github-copilot-login
# 按提示在浏览器中输入 device code 完成授权
# 授权成功后令牌自动保存
exit

# 重启服务使令牌生效
docker compose restart
```

### Kiro Web OAuth 登录

Kiro 还支持通过浏览器直接登录（不需要进入容器）：

```
http://localhost:8317/v0/oauth/kiro
```

### 登录辅助选项

| 选项 | 说明 |
|------|------|
| `--no-browser` | 不自动打开浏览器（手动复制链接） |
| `--incognito` | 隐私模式打开浏览器（多账号） |
| `--no-incognito` | 强制不使用隐私模式 |

---

## API Key 直接配置

部分 provider 支持直接在 `config.yaml` 中配置 API Key，不需要 OAuth 登录。

### Gemini API Key

```yaml
gemini-api-key:
  - api-key: "AIzaSy...your-key"
    # prefix: "test"              # 可选：强制使用 test/gemini-2.5-pro 格式
    # proxy-url: "socks5://..."   # 可选：单独代理
    # excluded-models:            # 可选：排除模型
    #   - "gemini-2.5-pro"
```

### Claude API Key

```yaml
claude-api-key:
  - api-key: "sk-ant-...your-key"
    # base-url: "https://..."     # 可选：自定义端点
    # cloak:                      # 可选：请求伪装
    #   mode: "auto"
```

### OpenAI/Codex API Key

```yaml
codex-api-key:
  - api-key: "sk-...your-key"
    # base-url: "https://..."     # 可选：自定义端点
```

### OpenAI 兼容 Provider

```yaml
openai-compatibility:
  - name: "openrouter"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-...your-key"
    models:
      - name: "moonshotai/kimi-k2:free"   # 上游真实模型名
        alias: "kimi-k2"                  # 你调用时用的别名
```

### Vertex API Key

```yaml
vertex-api-key:
  - api-key: "vk-123..."
    base-url: "https://example.com/api"
```

---

## 查看模型列表

### 查看所有可用模型

```bash
curl -s http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1" | jq '.data[].id'
```

输出示例：
```json
"gpt-5"
"gpt-5-mini"
"claude-sonnet-4"
"claude-opus-4.6"
"gemini-2.5-pro"
...
```

### 查看模型详细信息

```bash
curl -s http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1" | jq '.data[] | {id, owned_by}'
```

输出示例：
```json
{"id": "gpt-5", "owned_by": "github-copilot"}
{"id": "claude-sonnet-4", "owned_by": "github-copilot"}
{"id": "gemini-2.5-pro", "owned_by": "github-copilot"}
```

### 查看完整 JSON 响应

```bash
curl -s http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1" | jq .
```

### 按 provider 过滤模型

```bash
# 只看 github-copilot 的模型
curl -s http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1" | \
  jq '.data[] | select(.owned_by == "github-copilot") | .id'
```

### 统计模型数量

```bash
curl -s http://localhost:8317/v1/models \
  -H "Authorization: Bearer my-secret-key-1" | jq '.data | length'
```

### 关于动态模型发现

GitHub Copilot provider 支持**动态模型发现**：

1. 服务启动后，首次请求模型列表时会自动调用 GitHub Copilot API 获取最新模型
2. 结果缓存 10 分钟，避免频繁请求
3. 如果动态获取失败（网络问题、API 不支持等），自动回退到静态列表
4. 静态列表包含常见模型如 `gpt-5`、`claude-sonnet-4`、`claude-opus-4.6` 等

这意味着当 GitHub Copilot 新增模型时，你不需要更新代码——重启服务或等缓存过期即可自动获取。

---

## 发送聊天请求

### 基础请求

```bash
curl -s http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer my-secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### 流式请求

```bash
curl -s http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer my-secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "messages": [
      {"role": "user", "content": "Write a haiku"}
    ],
    "stream": true
  }'
```

### 使用带前缀的模型

如果 credential 配置了 `prefix`，则需要带前缀调用：

```bash
curl -s http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer my-secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "test/gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Hi"}]
  }'
```

---

## 模型别名与排除

### 模型别名

在 `config.yaml` 中为 OAuth provider 的模型起别名：

```yaml
oauth-model-alias:
  github-copilot:
    - name: "gpt-5"              # 原模型名
      alias: "copilot-gpt5"     # 别名
    - name: "claude-sonnet-4"
      alias: "sonnet4"
      fork: true                 # 保留原名，同时增加别名
  gemini-cli:
    - name: "gemini-2.5-pro"
      alias: "g2.5p"
  claude:
    - name: "claude-sonnet-4-5-20250929"
      alias: "cs4.5"
```

- `fork: true` 表示保留原模型名，同时新增一个别名
- `fork: false`（默认）表示用别名替换原模型名

### 排除模型

隐藏不需要的模型：

```yaml
oauth-excluded-models:
  github-copilot:
    - "raptor-mini"         # 精确匹配
    - "gpt-5-*"            # 通配符：前缀匹配
    - "*-preview"           # 通配符：后缀匹配
    - "*codex*"             # 通配符：包含匹配
  claude:
    - "claude-3-5-haiku-20241022"
```

支持的通配符模式：
- `model-name` — 精确匹配
- `prefix-*` — 前缀匹配
- `*-suffix` — 后缀匹配
- `*substring*` — 子串匹配

---

## 代理与网络

### 全局代理

```yaml
proxy-url: "socks5://user:pass@192.168.1.1:1080"
```

### 单个 credential 代理

每个 API Key 条目都支持独立的 `proxy-url`：

```yaml
gemini-api-key:
  - api-key: "AIzaSy..."
    proxy-url: "http://proxy.example.com:8080"
```

### TLS/HTTPS

```yaml
tls:
  enable: true
  cert: "/path/to/cert.pem"
  key: "/path/to/key.pem"
```

---

## 管理面板

### 启用管理 API

```yaml
remote-management:
  allow-remote: false           # true = 允许远程访问
  secret-key: "your-mgmt-key"  # 管理密钥（首次启动后自动哈希）
  disable-control-panel: false
```

### 访问管理面板

```
http://localhost:8317/v0/management/
```

需要输入 `secret-key` 登录。管理面板会从 GitHub 自动下载前端资源。

---

## 日志与调试

### 查看容器日志

```bash
# 实时日志
docker logs -f cli-proxy-api-plus

# 最近 100 行
docker logs --tail 100 cli-proxy-api-plus

# 按时间过滤
docker logs --since 1h cli-proxy-api-plus
```

### 写入文件日志

```yaml
logging-to-file: true
logs-max-total-size-mb: 100     # 日志总大小限制（MB），0 = 不限
error-logs-max-files: 10        # 最多保留的错误日志文件数
```

日志文件存储在 `./logs/` 目录。

### 开启调试模式

```yaml
debug: true
```

### pprof 性能分析

```yaml
pprof:
  enable: true
  addr: "127.0.0.1:8316"
```

访问 `http://localhost:8316/debug/pprof/` 查看 Go 性能数据。

---

## Docker 运维

### 查看容器状态

```bash
docker ps --filter "name=cli-proxy" --format "table {{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}"
```

### 端口说明

| 端口 | 用途 |
|------|------|
| `8317` | 主 API 服务（必须） |
| `8085` | OAuth 回调 |
| `1455` | OAuth 回调 |
| `54545` | OAuth 回调 |
| `51121` | OAuth 回调 |
| `11451` | OAuth 回调 |

> **提示**：如果不需要 OAuth 登录，只映射 `8317` 端口即可。

### 最小化 docker-compose.yml

```yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api-plus:latest
    container_name: cli-proxy-api-plus
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
    restart: unless-stopped
```

### 使用自定义路径

通过环境变量覆盖默认路径：

```bash
CLI_PROXY_CONFIG_PATH=/etc/proxy/config.yaml \
CLI_PROXY_AUTH_PATH=/data/auths \
CLI_PROXY_LOG_PATH=/var/log/proxy \
docker compose up -d
```

### 进入容器排查问题

```bash
docker exec -it cli-proxy-api-plus sh

# 查看容器内文件
ls /CLIProxyAPI/
ls /root/.cli-proxy-api/

# 查看进程
ps aux
```

### 更新到最新版本

```bash
# 方式 1：使用远程镜像
docker compose pull && docker compose up -d

# 方式 2：本地源码构建
git pull
docker compose down
docker compose build --no-cache
docker compose up -d
```

---

## 本地开发（不用 Docker）

### 前置条件

- Go 1.24+

### 编译运行

```bash
# 编译
go build -o CLIProxyAPIPlus ./cmd/server/

# 运行
./CLIProxyAPIPlus --config config.yaml

# 直接登录（不启动服务器）
./CLIProxyAPIPlus --github-copilot-login --config config.yaml
./CLIProxyAPIPlus --login --config config.yaml
```

### 运行测试

```bash
# 运行全部测试
go test ./...

# 运行特定包的测试
go test ./internal/runtime/executor/ -v

# 代码检查
go vet ./...
```

---

## 常见问题

### Q: 启动后显示 "failed to load config: read config.yaml: is a directory"

**原因**：Docker 在挂载 volume 时，如果宿主机上 `config.yaml` 不存在，会自动创建一个同名**目录**。

**修复**：
```bash
docker compose down
rm -rf config.yaml
cp config.example.yaml config.yaml
# 编辑 config.yaml 填入你的配置
docker compose up -d
```

### Q: 调用模型返回 "Invalid API key"

**原因**：`config.yaml` 中 `api-keys` 未配置，或者你请求时用的 key 不在列表中。

**检查**：
```bash
# 确认配置中有你的 key
grep -A 3 "api-keys" config.yaml
```

### Q: 模型列表为空

**原因**：没有任何 provider 登录成功或配置了 API Key。

**解决**：先完成至少一个 provider 的登录或 API Key 配置，然后重启。

### Q: 看不到 claude-opus-4.6 等新模型

**可能原因**：
1. 你的 GitHub Copilot 账号没有该模型权限
2. 使用的是旧版代码（静态列表中没有该模型）

**解决**：
- 确认你的 Copilot 订阅支持该模型
- 更新代码并重建 Docker 镜像
- 当前版本已支持动态模型发现，会自动获取 Copilot API 返回的所有模型

### Q: 如何同时使用多个 provider？

每个 provider 独立登录或配置即可，模型列表会自动合并：

```bash
docker exec -it cli-proxy-api-plus sh
./CLIProxyAPIPlus --github-copilot-login   # 登录 Copilot
./CLIProxyAPIPlus --login                  # 登录 Gemini
./CLIProxyAPIPlus --claude-login           # 登录 Claude
exit
docker compose restart
```

### Q: OAuth 令牌过期了怎么办？

服务内置了**后台令牌刷新**机制，会在令牌过期前 10 分钟自动刷新。如果刷新失败，重新执行对应的登录命令即可。

### Q: 如何让 Claude Code / Cursor / 其他客户端使用这个代理？

在客户端中设置 API Base URL 和 API Key：

```
Base URL: http://localhost:8317/v1
API Key:  my-secret-key-1
```

具体设置方式因客户端而异，请参考各客户端文档中的"自定义 API 端点"相关设置。

### Q: 如何查看当前哪些账号已登录？

```bash
# 查看 auth 目录下的令牌文件
ls -la auths/

# 或进入容器查看
docker exec cli-proxy-api-plus ls -la /root/.cli-proxy-api/
```

### Q: 配置文件修改后需要重建 Docker 吗？

**不需要**。`config.yaml` 是通过 volume 挂载的，修改后只需重启容器：

```bash
docker compose restart
```

只有**代码**改动才需要重新 `docker compose build`。

### Q: Codex 连接显示 502 "unknown provider for model"

**典型报错**：
```
Unexpected status 502 Bad Gateway: unknown provider for model gpt-5.2-codex
```

**原因**：代理服务器没有任何已登录的 provider 能处理该模型。通常是因为 OAuth 令牌丢失（容器重建后 `auths/` 被清空）。

**排查**：
```bash
# 检查是否有登录令牌
docker exec cli-proxy-api-plus ls /root/.cli-proxy-api/

# 检查模型列表是否为空
curl -s http://localhost:8317/v1/models -H "Authorization: Bearer your-api-key-1" | jq '.data | length'
```

**修复**：如果 `auths/` 为空或模型列表返回 0，重新登录：
```bash
docker exec -it cli-proxy-api-plus sh
./CLIProxyAPIPlus --github-copilot-login
exit
docker compose restart
```

### Q: Docker 重建后丢失了 OAuth 令牌

**原因**：`docker compose down` 删除了容器，但只要 `./auths/` 目录挂载正确，令牌文件不会丢失。

**检查**：确认宿主机上 `./auths/` 目录有内容：
```bash
ls -la auths/
# 应该看到类似 github-copilot-username.json 的文件
```

如果为空，说明之前的容器没有正确挂载 volume，需要重新登录。

### Q: `docker compose build` 后 `up -d` 仍然用旧代码

**原因**：`docker-compose.yml` 配置了 `pull_policy: always`，`docker compose up -d` 会先从 Docker Hub 拉取远程镜像，**覆盖你本地 build 的镜像**。

**解决**：build 后用 `--pull never` 启动，阻止拉取远程镜像：
```bash
docker compose build --no-cache
docker compose up -d --pull never
```

或者临时改 `docker-compose.yml` 中的 `pull_policy: always` 为 `pull_policy: never`。

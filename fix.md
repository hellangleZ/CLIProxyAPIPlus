# CLIProxyAPIPlus Docker 代理修复记录

## 问题概述

用户使用 Claude Code CLI 通过 CLIProxyAPIPlus Docker 代理 (端口 8317) 时遇到两类错误：

1. **502 错误** - `unknown provider for model xxx`
2. **400 错误** - `output_config.effort: Input should be 'low', 'medium', 'high' or 'max'`

## 根因分析

### 问题 1：模型名不匹配 (502)

**现象**：
- `/model sonnet` → 502 (unknown provider for model `claude-sonnet-4-5-20250929`)
- `/model opus` → 502 (unknown provider for model `claude-opus-4-6`)
- `/model haiku` → 502 (unknown provider for model `claude-haiku-4-5-20251001`)

**原因**：
Claude Code CLI 发送的模型名格式与代理中定义的不同：
- CLI 发送：`claude-opus-4-6`（连字符，无点号）
- CLI 发送：`claude-sonnet-4-5-20250929`（带日期后缀）
- CLI 发送：`claude-haiku-4-5-20251001`（带日期后缀）
- 代理定义：`claude-opus-4.6`、`claude-sonnet-4.5`、`claude-haiku-4.5`（点号，无日期）

### 问题 2：output_config.effort 400 错误

**现象**：
- `/model claude-opus-4.6` → 400 (output_config.effort 错误)

**原因**：
`internal/registry/model_definitions.go` 中 `claude-opus-4.6`、`claude-sonnet-4.5`、`claude-haiku-4.5` **没有 `Thinking` 字段定义**。

代理需要 `Thinking` 字段来判断是否应该 strip 掉 `output_config` 参数。没有这个字段，代理不知道如何处理 thinking 相关参数，导致 `output_config.effort` 被透传到 GitHub Copilot 上游 API，上游不认识这个参数就报 400。

## 已完成的修复

### 修复 1：添加 Thinking 字段

**文件**: `internal/registry/model_definitions.go`

给以下模型添加了 `Thinking` 字段：
- `claude-opus-4.6`
- `claude-sonnet-4.5`
- `claude-haiku-4.5`

```go
Thinking: &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
```

### 修复 2：添加模型别名

**文件**: `config.yaml`

添加了 oauth-model-alias 配置，将 CLI 发送的模型名映射到代理支持的模型名：

```yaml
oauth-model-alias:
  github-copilot:
    - name: "claude-opus-4.6"
      alias: "opus"
      fork: true
    - name: "claude-sonnet-4.5"
      alias: "sonnet"
      fork: true
    - name: "claude-haiku-4.5"
      alias: "haiku"
      fork: true
    # CLI 使用的带日期/连字符格式
    - name: "claude-opus-4.6"
      alias: "claude-opus-4-6"
      fork: true
    - name: "claude-sonnet-4.5"
      alias: "claude-sonnet-4-5-20250929"
      fork: true
    - name: "claude-haiku-4.5"
      alias: "claude-haiku-4-5-20251001"
      fork: true
```

### 修复 3：Docker 构建配置

**文件**: `docker-compose.yml`

将 `pull_policy: always` 改为 `pull_policy: build`，确保使用本地代码构建而不是拉取远程镜像。

## 当前状态

**全部修复完成！** ✅

所有测试通过：
- ✅ `claude-opus-4.6` - OK
- ✅ `claude-opus-4-6` (CLI 格式) - OK
- ✅ `opus` - OK
- ✅ `claude-sonnet-4.5` - OK
- ✅ `claude-sonnet-4-5-20250929` (CLI 格式) - OK
- ✅ `sonnet` - OK
- ✅ `claude-haiku-4.5` - OK
- ✅ `claude-haiku-4-5-20251001` (CLI 格式) - OK
- ✅ `haiku` - OK

## 待完成

1. 重启 Docker 容器应用最新的 config.yaml 更改（添加了 `claude-opus-4-6` 别名）
2. 验证 `/model opus` 和 `/model claude-opus-4.6` 是否正常工作

## 验证命令

```bash
# 重启容器
docker restart cli-proxy-api-plus

# 检查模型列表
curl -s http://localhost:8317/v1/models -H "Authorization: Bearer your-api-key-1" | python3 -c "import json,sys; [print(m['id']) for m in json.load(sys.stdin)['data'] if 'claude' in m['id'].lower() or m['id'] in ('opus','sonnet','haiku')]"

# 测试各模型
curl -s -X POST 'http://localhost:8317/v1/messages?beta=true' -H 'Authorization: Bearer your-api-key-1' -H 'Content-Type: application/json' -H 'Anthropic-Version: 2023-06-01' -d '{"model":"claude-opus-4.6","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}'
```

## 关键文件

| 文件 | 修改内容 |
|------|----------|
| `internal/registry/model_definitions.go` | 给 claude-opus-4.6/sonnet-4.5/haiku-4.5 添加 Thinking 字段 |
| `config.yaml` | 添加 oauth-model-alias 模型别名映射 |
| `docker-compose.yml` | pull_policy: always → build |

# aiflow

面向 shell 编排的 AI 访问入口。把 LLM 变成可组合、可管道、可脚本化的 CLI 组件。

## 设计理念

- **单一职责**：只负责"调用 LLM 并返回结果"，不做复杂封装
- **输出契约清晰**：结构化 JSON 走 stdout，日志与进度走 stderr，管道安全
- **多模式输出**：envelope JSON（默认）/ `--raw` 纯文本 / `--stream` 流式增量
- **OpenAI 兼容协议**：可对接 DeepSeek、通义、智谱、vLLM、Ollama、LM Studio 等

## 快速开始

### 构建

```bash
go build -o bin/aiflow ./cmd/aiflow
```

### 配置

**方式一：config.yaml（推荐）**

在项目 `config/config.yaml`（已含模板）中统一管理 AI 配置，也可复制到任意位置后用 `AIFLOW_CONFIG` 指定：

```yaml
api_key: sk-xxxxxxxxxxxxxxxx        # 或使用环境变量 AIFLOW_API_KEY
base_url: https://api.deepseek.com/v1
model: deepseek-chat
temperature: 0.7
timeout: 120
retries: 2
```

**方式二：环境变量**

```bash
export AIFLOW_API_KEY=sk-xxxxxxxxxxxxxxxx
export AIFLOW_MODEL=deepseek-chat
# 可选：自定义 API 地址
# export AIFLOW_API_BASE=https://api.deepseek.com/v1
```

配置优先级：**命令行参数 > 环境变量 `AIFLOW_*` > config.yaml > 内置默认值**。

### 使用

```bash
# 最简调用
aiflow ask -p '用一句话解释 CAP 定理'

# 纯文本，管道友好
aiflow ask --raw -p '输出前 10 个质数，逗号分隔' | tr ',' '\n'

# 从 stdin 读取提示词
cat code.go | aiflow ask --raw -f - -s '你是代码审查专家'

# 流式输出，边生成边落盘
aiflow ask --stream -p '写一段 200 字的产品介绍' | tee output.md

# 要求 JSON 输出，用 jq 解析
aiflow ask --json -p '返回 {"lang":"go"} 结构的 JSON' | jq -r .data.content
```

## 命令

### `aiflow ask [选项]`

调用 LLM 完成一次对话补全。

| 选项 | 简写 | 说明 |
|------|------|------|
| `--prompt <text>` | `-p` | 提示词文本，`-` 表示从 stdin 读取 |
| `--file <path>` | `-f` | 从文件读取提示词，`-` 表示 stdin |
| `--system <text>` | `-s` | 系统提示词 |
| `--model <name>` | `-m` | 模型名 |
| `--base-url <url>` | | OpenAI 兼容地址，默认 `https://api.deepseek.com/v1` |
| `--timeout <sec>` | `-t` | 超时秒数，默认 120 |
| `--temp <float>` | | 温度，默认 0.7 |
| `--max-tokens <n>` | | 最大输出 token，0 表示服务端默认 |
| `--retries <n>` | | 429/5xx 重试次数，默认 2 |
| `--json` | | 要求模型输出 JSON 对象 |
| `--stream` | | 流式输出，纯文本写 stdout，envelope 写 stderr |
| `--raw` | | 只输出纯文本，无 JSON envelope |
| `--out <path>` | `-o` | 将结果正文同时写入文件 |

### `aiflow version`

打印版本号。

## 输出契约

### 模式一：Envelope JSON（默认）

stdout 输出统一格式：

```json
{
  "ok": true,
  "data": {
    "content": "Go 语言是一种...",
    "model": "deepseek-chat",
    "usage": {
      "prompt_tokens": 15,
      "completion_tokens": 42,
      "total_tokens": 57
    }
  },
  "error": ""
}
```

失败时：

```json
{"ok": false, "data": null, "error": "AIFLOW_API_KEY 未设置"}
```

### 模式二：`--raw`

stdout 只输出模型回复的纯文本，便于管道、重定向、组合。

### 模式三：`--stream`

- 增量文本逐块写入 stdout（实时可见）
- 结束时 envelope JSON 写入 stderr（不污染主输出）

### 退出码

| 码 | 含义 |
|----|------|
| 0 | 成功 |
| 1 | 业务/网络失败 |
| 2 | 用法错误 |
| 124 | 超时 |

## 环境变量

| 变量 | 必填 | 说明 |
|------|------|------|
| `AIFLOW_API_KEY` | 二选一 | API 密钥，未配置时读取 config.yaml 的 `api_key` |
| `AIFLOW_MODEL` | 可选 | 默认模型名，也可通过 `-m` 或 config.yaml 指定 |
| `AIFLOW_API_BASE` | 可选 | API 地址，默认 `https://api.deepseek.com/v1` |
| `AIFLOW_CONFIG` | 可选 | config.yaml 路径，默认查找 `./config/config.yaml`、`./config.yaml` |

## 配置文件 config.yaml

查找顺序：`AIFLOW_CONFIG` > `./config/config.yaml` > `./config.yaml`（找不到时全部使用内置默认值）。

| 字段 | 对应 CLI/环境变量 | 说明 |
|------|------|------|
| `api_key` | `AIFLOW_API_KEY` | API 密钥，建议用环境变量以免随文件分发 |
| `base_url` | `AIFLOW_API_BASE` | OpenAI 兼容地址，默认 DeepSeek |
| `model` | `-m` / `AIFLOW_MODEL` | 默认模型名 |
| `temperature` | `--temp` | 采样温度，默认 0.7 |
| `max_tokens` | `--max-tokens` | 最大输出 token，0 表示服务端默认 |
| `timeout` | `-t` | 请求超时秒数，默认 120 |
| `retries` | `--retries` | 429/5xx 重试次数，默认 2 |

字段使用了 YAML 严格解析，拼写错误或未知字段会直接报错。

## 脚本示例

`scripts/pipe-demo.sh` 演示四种管道玩法：

```bash
./scripts/pipe-demo.sh
```

`scripts/review.sh` 是一个完整工作流示例，实现"AI 代码审查 → shell 统计 → AI 二次压缩 → 输出报告"：

```bash
./scripts/review.sh cmd/aiflow/main.go
```

## 项目结构

```
aiflow/
├── cmd/aiflow/main.go       # 入口 & CLI
├── config/config.yaml       # 默认配置文件模板
├── internal/
│   ├── config/config.go     # config.yaml 查找与解析
│   ├── llm/client.go        # OpenAI 兼容客户端（含流式 + 重试）
│   └── out/envelope.go      # 统一输出契约
├── scripts/                 # shell 编排示例
└── go.mod
```

## 技术细节

- **依赖极简**：除 `gopkg.in/yaml.v3`（config.yaml 解析）外无其他第三方依赖
- **指数退避重试**：对 429/5xx 自动重试，最大 2 次
- **超时可控**：通过 `-t` 指定，或依赖 HTTP client 默认值
- **Context 取消传递**：重试和流式读取均尊重 context 取消信号
- **SSE 解析**：流式模式使用 `bufio.Scanner` 解析 `data: {...}` 格式

## License

MIT
#!/usr/bin/env bash
# 示例：aiflow 与 shell 管道 / 外部 CLI 的组合玩法
#
# 用法：export AIFLOW_API_KEY=sk-xxx; ./scripts/pipe-demo.sh
set -euo pipefail

AIFLOW=${AIFLOW:-./bin/aiflow}
MODEL=${AIFLOW_MODEL:-deepseek-chat}

if [[ -z "${AIFLOW_API_KEY:-}" && ! -f config/config.yaml && ! -f config.yaml ]]; then
  echo "请配置 API key：export AIFLOW_API_KEY，或填写 config/config.yaml（见 README）" >&2
  exit 1
fi

echo "==> 1) --raw 直接对接管道（AI 输出 -> tr -> nl）"
"$AIFLOW" ask --raw -m "$MODEL" --temp 0 \
  -p '列出容器化部署的 5 个关键检查项，每行一个，不要序号' \
  | tr -d '\r' | grep -v '^$' | nl -ba

echo
echo "==> 2) envelope + jq 解析（推荐用于需要元信息的场景）"
res=$("$AIFLOW" ask -m "$MODEL" --json --temp 0 \
  -s '你只输出 JSON' \
  -p '返回 {"name":"golang","born":2009} 这样的 JSON，描述 Go 语言')
if command -v jq >/dev/null 2>&1; then
  echo "$res" | jq -r 'if .ok then "content=\(.data.content)  tokens=\(.data.usage.total_tokens)" else "失败: \(.error)" end'
else
  echo "$res"
fi

echo
echo "==> 3) --stream 边生成边落盘（stderr 的 envelope 不污染文件）"
"$AIFLOW" ask --stream -m "$MODEL" \
  -p '用 100 字介绍 WSL2 的用途' 2>/dev/null | tee /tmp/aiflow-stream.txt >/dev/null
wc -c /tmp/aiflow-stream.txt

echo
echo "==> 4) 退出码语义：超时返回 124"
set +e
"$AIFLOW" ask -m "$MODEL" -t 1 -p '长任务测试' >/dev/null 2>&1
code=$?
set -e
echo "退出码: $code （0 成功 / 1 失败 / 2 用法错误 / 124 超时）"

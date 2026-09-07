#!/usr/bin/env bash
# 示例工作流：AI 代码审查 -> shell 侧统计 -> 二次调用 AI 压缩 -> 输出报告
#
# 依赖：aiflow、jq（本例用 --raw 模式，可不装 jq）
# 用法：export AIFLOW_API_KEY=sk-xxx; ./scripts/review.sh <file> [model]
set -euo pipefail

usage() {
  echo "用法: $0 <file> [model]" >&2
  exit 2
}
[[ $# -ge 1 ]] || usage

FILE=$1
MODEL=${2:-${AIFLOW_MODEL:-deepseek-chat}}
AIFLOW=${AIFLOW:-./bin/aiflow}

[[ -f "$FILE" ]] || { echo "文件不存在: $FILE" >&2; exit 1; }
if [[ -z "${AIFLOW_API_KEY:-}" && ! -f config/config.yaml && ! -f config.yaml ]]; then
  echo "请配置 API key：export AIFLOW_API_KEY，或填写 config/config.yaml（见 README）" >&2
  exit 1
fi
command -v "$AIFLOW" >/dev/null 2>&1 || { echo "找不到 aiflow，请先构建：go build -o bin/aiflow ./cmd/aiflow" >&2; exit 1; }

# ask <system> <file>：调用 AI 并只取正文（--raw）
ask_file() {
  local system="$1" file="$2"
  "$AIFLOW" ask --raw -m "$MODEL" -s "$system" -f "$file"
}

echo "==> [1/3] 审查 $FILE"
review=$(ask_file '你是资深代码审查专家。只输出 markdown 无序列表，每条以 "- " 开头，最多 8 条，不要客套话。' "$FILE")

# shell 侧用外部 CLI 做统计，AI 不参与
count=$(printf '%s\n' "$review" | grep -c '^- ' || true)
words=$(printf '%s\n' "$review" | wc -w | tr -d ' ')

echo "==> [2/3] 统计：${count} 条建议 / ${words} 词"

echo "==> [3/3] 生成一句话总结"
summary=$("$AIFLOW" ask --raw -m "$MODEL" -s '你是技术编辑，输出中文一句话，不超过 40 字。' \
  -p "把以下审查意见压缩成一句话：
$review")

echo
echo "-------- 报告 --------"
echo "文件:   $FILE"
echo "模型:   $MODEL"
echo "建议数: $count"
echo "总结:   $summary"
echo "----------------------"
printf '%s\n' "$review"

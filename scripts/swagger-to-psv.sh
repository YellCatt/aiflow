#!/usr/bin/env bash
# 示例工作流：swagger.json -> AI 生成 PipeGo 用例 -> shell 清洗/校验 -> 落盘 PSV
#
# 输出格式（PipeGo JSON 请求体用例）：
#   # 注释标题
#   id|skip|desc|method|url|headers|json|expected_status|tags
#
# 依赖：aiflow（必需）；jq（可选，用于裁剪 swagger，去掉解释字段能省 token）
#
# 用法：
#   export AIFLOW_API_KEY=sk-xxx
#   ./scripts/swagger-to-psv.sh scripts/testdata/petstore.swagger.json
#   ./scripts/swagger-to-psv.sh api.json cases.psv --filter '/pet,/store'
#   DRY_RUN=1 ./scripts/swagger-to-psv.sh scripts/testdata/petstore.swagger.json   # 不调 AI，演示整个管道
set -euo pipefail

AIFLOW=${AIFLOW:-./bin/aiflow}
MODEL=${AIFLOW_MODEL:-deepseek-chat}
HEADER='id|skip|desc|method|url|headers|json|expected_status|tags'
MAX_BYTES=${MAX_BYTES:-24000}   # 送给 AI 的 swagger 片段上限（字节）

usage() {
  cat >&2 <<'EOF'
用法: swagger-to-psv.sh <swagger.json> [输出.psv] [--filter 路径前缀,逗号分隔]

环境变量:
  DRY_RUN=1      跳过 AI 调用，用内置样例回复演示清洗/校验流程
  MODEL          通过 AIFLOW_MODEL 覆盖模型名
  MAX_BYTES      裁剪后 swagger 的最大字节数，默认 24000
EOF
  exit 2
}

SWAGGER=""
OUT="cases.psv"
FILTER=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --filter) FILTER=${2:-}; shift 2 ;;
    -h|--help) usage ;;
    *)
      if [[ -z "$SWAGGER" ]]; then SWAGGER=$1; else OUT=$1; fi
      shift ;;
  esac
done
[[ -n "$SWAGGER" ]] || usage

[[ -f "$SWAGGER" ]] || { echo "文件不存在: $SWAGGER" >&2; exit 1; }
if [[ -z "${AIFLOW_API_KEY:-}" && ! -f config/config.yaml && ! -f config.yaml && "${DRY_RUN:-0}" != "1" ]]; then
  echo "请配置 API key：export AIFLOW_API_KEY，或填写 config/config.yaml（见 README）" >&2
  exit 1
fi
[[ "${DRY_RUN:-0}" == "1" ]] || command -v "$AIFLOW" >/dev/null 2>&1 \
  || { echo "找不到 aiflow，请先构建：go build -o bin/aiflow ./cmd/aiflow" >&2; exit 1; }

echo "==> [1/4] 读取并裁剪 $SWAGGER"

# 只保留生成用例必需的字段；没有 jq 时退化为原始文本截断
if command -v jq >/dev/null 2>&1; then
  api=$(jq -c --arg kw "$FILTER" '
      # 按 --filter 给的路径前缀过滤
      def pick($keys):
        if ($keys | length) == 0 then .
        else with_entries(select(.key as $k | $keys | any(. as $p | ($k | startswith($p)))))
        end;
      {
        info, host, basePath,
        paths: ((.paths // {})
          | pick($kw | split(",") | map(select(. != "")))
          | map_values(
              with_entries(select(["get","put","post","delete","patch","options","head"] | index(.key)))
              | map_values({summary, tags, consumes, produces, parameters, requestBody, responses: (.responses | keys)})
            )),
        schemas: (.components.schemas // .definitions // {})
      }' "$SWAGGER" 2>/dev/null || jq -c '.' "$SWAGGER")
else
  echo "    提示：未安装 jq，使用原文截断（建议安装以获得更省 token 的裁剪）" >&2
  api=$(tr -d '\r' < "$SWAGGER" | tr -s ' \n\t' ' ' | head -c "$MAX_BYTES")
fi

[[ $(printf '%s' "$api" | wc -c | tr -d ' ') -le "$MAX_BYTES" ]] \
  || api=$(printf '%s' "$api" | head -c "$MAX_BYTES")

echo "    裁剪后 $(printf '%s' "$api" | wc -c | tr -d ' ') 字节，接口 $(printf '%s' "$api" | grep -o '"\(get\|post\|put\|patch\|delete\)":' | wc -l | tr -d ' ') 个"

# ---------------------------------------------------------------- prompt 组装
SYSTEM='你是资深接口测试用例生成专家，精通 PipeGo 的 PSV 用例格式。只按用户要求的格式输出，不要寒暄、不要解释、不要 markdown 代码块。'

PROMPT_FILE=$(mktemp)
trap 'rm -f "$PROMPT_FILE"' EXIT

{
  cat <<EOF
请根据下面的 API 描述，生成 PipeGo 的「JSON 请求体」测试用例，输出一段纯 PSV 文本。

=== 输出格式（严格遵守）===
第 1 行：注释行，以 # 开头，写一句标题，例如：# <接口集合> JSON请求体测试用例
第 2 行：表头，固定为下面这一行，不要增删字段
$HEADER
第 3 行起：每条用例一行，9 个字段用 | 分隔，整行内不得出现换行

=== 字段规则 ===
id: 小写下划线风格，建议 <method>_<两位序号>，如 post_01
skip: 固定填 0
desc: 中文短语，15 字以内，说明这条用例测什么
method: 大写 HTTP 方法（GET/POST/PUT/PATCH/DELETE）
url: '{{base_url}}' 开头 + 接口路径；路径参数用示例值替换，路径中含 . 或 {id} 的一律替换为具体值
headers: 单行 JSON 字符串，至少包含 {"Content-Type":"application/json"}；无 body 的接口可填 {}
json: 单行压缩 JSON 字符串（不能换行、不能含竖线 |，没有 body 时填 {}），字段取值参照 requestBody / body schema 的字段类型、example、enum、minimum；需要跨用例复用数据时写 {{变量名}} 占位
expected_status: 整数，取 responses 里的成功状态码
tags: 小写逗号分隔，如 json,api 或 json,api,pet

=== 生成要求 ===
- 只覆盖带请求体（POST/PUT/PATCH）的接口，每个接口 2~4 条，总条数控制在 30 以内
- 每个接口至少包含：一条正常样例、一条边界或特殊字符样例（缺失必填 / 空对象 / 空数组 / 特殊字符串任选）
- 请求体为数组的接口单独出一条多条数据的用例
- 中文直接写中文，不要 \\uXXXX 转义
- 除了 PSV 内容，不要输出任何其它文字

=== API 描述开始 ===
EOF
  printf '%s\n' "$api"
  echo "=== API 描述结束 ==="
} > "$PROMPT_FILE"

echo "==> [2/4] 调用 AI（模型 $MODEL）"
if [[ "${DRY_RUN:-0}" == "1" ]]; then
  echo "    DRY_RUN=1，使用内置样例回复"
  raw=$(cat <<'MOCK'
好的，下面是生成的 PipeGo 测试用例：

```psv
# Petstore JSON请求体测试用例
id|skip|desc|method|url|headers|json|expected_status|tags
post_01|0|新增宠物-正常|POST|{{base_url}}/pet|{"Content-Type":"application/json"}|{"name":"旺财","category":{"id":1,"name":"犬类"},"photoUrls":["https://img.example.com/1.png"],"status":"available","tags":[{"id":1,"name":"温顺"}]}|201|json,api,pet
post_02|0|新增宠物-缺失必填|POST|{{base_url}}/pet|{"Content-Type":"application/json"}|{"photoUrls":["https://img.example.com/2.png"]}|400|json,api,pet
put_01|0|更新宠物状态|PUT|{{base_url}}/pet|{"Content-Type":"application/json"}|{"id":{{pet_id}},"name":"旺财-已售","status":"sold"}|200|json,api,pet
post_03|0|批量新增宠物|POST|{{base_url}}/pet/batch|{"Content-Type":"application/json"}|[{"name":"用户A","status":"available"},{"name":"用户B","status":"pending"}]|200|json,api,pet
post_04|0|创建订单-含特殊字符|POST|{{base_url}}/store/order|{"Content-Type":"application/json"}|{"orderNo":"ORD202401001","petId":{{pet_id}},"quantity":2,"shipDate":"2024-01-15T10:30:00Z","remark":"Hello World! @#$%^&*()"}|201|json,api,store
post_05|0|创建订单-数量越界|POST|{{base_url}}/store/order|{"Content-Type":"application/json"}|{"orderNo":"ORD202401002","quantity":0}|400|json,api,store
post_06|0|创建用户-嵌套对象|POST|{{base_url}}/user|{"Content-Type":"application/json"}|{"username":"zhangsan","email":"zhangsan@example.com","phone":"13800138000","age":28,"profile":{"nickname":"张三","city":"杭州"}}|201|json,api,user
post_07|0|创建用户-空对象|POST|{{base_url}}/user|{"Content-Type":"application/json"}|{}|400|json,api,user
post_08|0|列表批量创建用户|POST|{{base_url}}/user/createWithList|{"Content-Type":"application/json"}|[{"username":"lisi","email":"lisi@example.com"}]|200|json,api,user
```

以上用例可以直接放进 PipeGo 执行。
MOCK
)
else
  raw=$("$AIFLOW" ask --raw -m "$MODEL" --temp 0 -s "$SYSTEM" -f "$PROMPT_FILE")
fi
[[ -n "${raw//[[:space:]]/}" ]] || { echo "AI 返回为空" >&2; exit 1; }

echo "==> [3/4] 清洗：去代码块围栏 / 去空行 / 去前后废话 / 统一换行"
psv=$(printf '%s\n' "$raw" \
  | tr -d '\r' \
  | awk '
      /^[[:space:]]*```/               { next }   # markdown 代码围栏
      /^[[:space:]]*$/                 { next }   # 空行
      {
        line = $0
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
        if (line ~ /^#/ || line ~ /\|/) print line   # 只留注释行与含 | 的数据行
      }')

# 表头兜底：AI 偶尔会漏表头
if ! printf '%s\n' "$psv" | grep -qF "$HEADER"; then
  echo "    警告：AI 输出缺少表头，已自动补齐" >&2
  psv=$(printf '%s\n%s\n' "$HEADER" "$psv")
fi

# 注释标题兜底
title=$(printf '%s\n' "$psv" | grep '^#' | head -1 || true)
if [[ -z "$title" ]]; then
  psv=$(printf '# %s JSON请求体测试用例\n%s\n' "$(basename "$SWAGGER" .json)" "$psv")
fi

echo "==> [4/4] 校验并写入 $OUT"
total=$(printf '%s\n' "$psv" | awk -F'|' 'NF == 9 && $1 != "id" { c++ } END { print c + 0 }')
bad=$(printf '%s\n' "$psv" | awk -F'|' '!/^#/ && NF != 9 { c++ } END { print c + 0 }')
dup=$(printf '%s\n' "$psv" | cut -d'|' -f1 | grep -vx 'id' | sort | uniq -d | wc -l | tr -d ' ')

printf '%s\n' "$psv" > "$OUT"

echo
echo "-------- 结果 --------"
echo "输出文件: $OUT"
echo "用例条数: $total"
echo "字段异常: $bad 行（每行应为 9 个字段，若异常多为 json 内含 | 或换行）"
echo "重复 id : $dup 个"
echo "----------------------"
echo "前 5 条预览："
head -7 "$OUT"
exit 0

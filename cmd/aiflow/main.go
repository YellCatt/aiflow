// Command aiflow 是面向 shell 编排的 AI 访问入口。
//
// 输出契约：
//   - 结构化 JSON（envelope）写 stdout，日志/进度写 stderr
//   - --raw     stdout 只输出纯文本，便于管道与重定向
//   - --stream  增量文本写 stdout，结束时 envelope 写 stderr
//
// 退出码：0 成功，1 业务/网络失败，2 用法错误，124 超时
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"aiflow/internal/config"
	"aiflow/internal/llm"
	"aiflow/internal/out"
)

const (
	version        = "0.1.0"
	defaultBaseURL = "https://api.deepseek.com/v1"

	exitOK      = 0
	exitFail    = 1
	exitUsage   = 2
	exitTimeout = 124
)

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stderr)

	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}

	switch os.Args[1] {
	case "ask":
		os.Exit(runAsk(os.Args[2:]))
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "version", "-v", "--version":
		fmt.Println(version)
		os.Exit(exitOK)
	case "help", "-h", "--help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `aiflow %s - AI 访问入口，供 shell 工作流调用

用法:
  aiflow ask [选项]
  aiflow init [路径]      # 生成默认配置文件（默认 config/config.yaml）
  aiflow version

选项:
  -p, --prompt <text>     提示词，'-' 表示从 stdin 读取
  -f, --file <path>       从文件读取提示词，'-' 表示 stdin
  -s, --system <text>     系统提示词
  -m, --model <name>      模型名（默认取 AIFLOW_MODEL）
      --base-url <url>    OpenAI 兼容地址（默认 AIFLOW_API_BASE 或 %s）
  -t, --timeout <sec>     超时秒数（默认 120）
      --temp <float>      温度（默认 0.7）
      --max-tokens <n>    最大输出 token（0 表示服务端默认）
      --retries <n>       429/5xx 重试次数（默认 2）
      --json              要求模型输出 JSON 对象
      --stream            流式输出纯文本到 stdout
      --raw               只输出纯文本（无 JSON envelope）
  -o, --out <path>        将结果正文同时写入文件

环境变量:
  AIFLOW_API_KEY   必填，API 密钥
  AIFLOW_API_BASE  API 地址
  AIFLOW_MODEL     默认模型
  AIFLOW_CONFIG    config.yaml 路径（可选）

配置文件:
  查找顺序: AIFLOW_CONFIG > ./config/config.yaml > ./config.yaml
  配置项: api_key / base_url / model / temperature / max_tokens / timeout / retries
  优先级: 命令行参数 > 环境变量 AIFLOW_* > config.yaml > 内置默认

示例:
  export AIFLOW_API_KEY=sk-xxx
  aiflow ask -m deepseek-chat -p '用一句话解释 CAP 定理'
  aiflow ask --raw -p '输出前 10 个质数，逗号分隔' | tr ',' '\n'
  cat code.go | aiflow ask --raw -f - -s '你是代码审查专家'
  aiflow ask --json -p '返回 {"lang":"go"} 结构的 JSON' | jq -r .data.content
`, version, defaultBaseURL)
}

// askData 是 ask 命令成功时 envelope 中的 data 负载。
type askData struct {
	Content string    `json:"content"`
	Model   string    `json:"model"`
	Usage   llm.Usage `json:"usage"`
}

func runInit(args []string) int {
	path := config.DefaultPath
	if len(args) >= 1 && args[0] != "" {
		path = args[0]
	}
	if err := config.Generate(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	fmt.Printf("已生成默认配置: %s\n", path)
	fmt.Println("请编辑该文件填写 api_key，或设置环境变量 AIFLOW_API_KEY")
	return exitOK
}

func runAsk(args []string) int {
	if _, created, err := config.EnsureDefault(); err != nil {
		log.Printf("警告: %v", err)
	} else if created {
		log.Printf("已自动生成默认配置: %s（可使用 aiflow init 手动生成）", config.DefaultPath)
	}

	fileCfg, cfgPath, err := config.Load()
	if err != nil {
		out.Fail(err)
		return exitFail
	}
	if cfgPath != "" {
		log.Printf("配置文件: %s", cfgPath)
	}

	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		prompt     = fs.String("p", "", "prompt")
		file       = fs.String("f", "", "prompt file")
		system     = fs.String("s", "", "system prompt")
		model      = fs.String("m", envOr("AIFLOW_MODEL", fileCfg.Model), "model")
		baseURL    = fs.String("base-url", envOr("AIFLOW_API_BASE", nonEmpty(fileCfg.BaseURL, defaultBaseURL)), "base url")
		timeoutSec = fs.Int("t", intOr(fileCfg.Timeout, 120), "timeout seconds")
		temp       = fs.Float64("temp", floatOr(fileCfg.Temp, 0.7), "temperature")
		maxTokens  = fs.Int("max-tokens", intOr(fileCfg.MaxTokens, 0), "max tokens")
		retries    = fs.Int("retries", intOr(fileCfg.Retries, 2), "retries")
		jsonMode   = fs.Bool("json", false, "json object output")
		stream     = fs.Bool("stream", false, "stream output")
		raw        = fs.Bool("raw", false, "raw text output")
		outFile    = fs.String("o", "", "write content to file")
	)
	fs.StringVar(prompt, "prompt", *prompt, "prompt 文本")
	fs.StringVar(file, "file", *file, "提示词文件")
	fs.StringVar(system, "system", *system, "系统提示词")
	fs.StringVar(model, "model", *model, "模型名")
	fs.IntVar(timeoutSec, "timeout", *timeoutSec, "超时秒数")
	fs.StringVar(outFile, "out", *outFile, "结果输出文件")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	text, err := readPrompt(*prompt, *file)
	if err != nil {
		out.Fail(err)
		return exitFail
	}
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(os.Stderr, "错误：提示词为空，请使用 -p / -f，或 -p - 从 stdin 读取")
		return exitUsage
	}

	apiKey := envOr("AIFLOW_API_KEY", fileCfg.APIKey)
	if apiKey == "" {
		out.Fail(errors.New("API key 未配置：请设置 AIFLOW_API_KEY 或填写 config.yaml 的 api_key"))
		return exitFail
	}
	if *model == "" {
		out.Fail(errors.New("模型名为空：请用 -m、设置 AIFLOW_MODEL 或填写 config.yaml 的 model"))
		return exitUsage
	}
	if *timeoutSec <= 0 {
		out.Fail(errors.New("timeout 必须为正整数"))
		return exitUsage
	}

	req := &llm.ChatRequest{
		Model:       *model,
		Temperature: temp,
		MaxTokens:   *maxTokens,
	}
	if *system != "" {
		req.Messages = append(req.Messages, llm.Message{Role: "system", Content: *system})
	}
	req.Messages = append(req.Messages, llm.Message{Role: "user", Content: text})
	if *jsonMode {
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
	}

	client := llm.New(llm.Config{
		BaseURL:    *baseURL,
		APIKey:     apiKey,
		Model:      *model,
		Timeout:    time.Duration(*timeoutSec) * time.Second,
		MaxRetries: *retries,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	var (
		content string
		data    askData
	)

	if *stream {
		var sb strings.Builder
		usage, streamErr := client.ChatStream(ctx, req, func(delta string) error {
			sb.WriteString(delta)
			_, werr := os.Stdout.WriteString(delta)
			return werr
		})
		content = sb.String()
		if usage != nil {
			data.Usage = *usage
		}
		data.Content = content
		data.Model = *model
		if streamErr != nil {
			emitToStderr(func() { out.Fail(streamErr) })
			return classifyErr(streamErr)
		}
		emitToStderr(func() { out.OK(data) })
	} else {
		resp, chatErr := client.Chat(ctx, req)
		if chatErr != nil {
			out.Fail(chatErr)
			return classifyErr(chatErr)
		}
		content = resp.Choices[0].Message.Content
		data = askData{Content: content, Model: resp.Model, Usage: resp.Usage}
		if *raw {
			writeRaw(content)
		} else {
			out.OK(data)
		}
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, []byte(content), 0o644); err != nil {
			log.Printf("写入文件失败: %v", err)
			return exitFail
		}
	}

	return exitOK
}

func classifyErr(err error) int {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return exitTimeout
	}
	return exitFail
}

func emitToStderr(fn func()) {
	saved := out.Writer
	out.Writer = os.Stderr
	defer func() { out.Writer = saved }()
	fn()
}

func writeRaw(s string) {
	if s == "" {
		return
	}
	fmt.Print(s)
	if !strings.HasSuffix(s, "\n") {
		fmt.Println()
	}
}

func readPrompt(prompt, file string) (string, error) {
	if file != "" {
		if file == "-" {
			b, err := io.ReadAll(os.Stdin)
			return string(b), err
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("读取提示词文件失败: %w", err)
		}
		return string(b), nil
	}
	if prompt == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	return prompt, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func nonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func intOr(v *int, fallback int) int {
	if v != nil {
		return *v
	}
	return fallback
}

func floatOr(v *float64, fallback float64) float64 {
	if v != nil {
		return *v
	}
	return fallback
}
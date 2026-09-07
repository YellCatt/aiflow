// Package config 负责从 config.yaml 读取 aiflow 的默认配置，
// 并提供自动生成默认配置文件的能力。
//
// 配置文件查找顺序（优先级由高到低）：
//  1. 环境变量 AIFLOW_CONFIG 指定的路径
//  2. 当前工作目录下的 config/config.yaml
//  3. 当前工作目录下的 config.yaml
//
// 所有配置项的最终优先级为：
// 命令行参数 > 环境变量 AIFLOW_* > 配置文件 > 内置默认值
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是自动生成时使用的默认路径。
const DefaultPath = "config/config.yaml"

// defaultTemplate 是带注释的默认配置模板。
const defaultTemplate = `# aiflow 默认配置
#
# 配置优先级（高 -> 低）：
#   命令行参数 > 环境变量 AIFLOW_* > 本文件 > 内置默认值
#
# 查找顺序：AIFLOW_CONFIG 环境变量指定的路径 > ./config/config.yaml > ./config.yaml
# 建议使用 export AIFLOW_API_KEY 设置密钥，避免把密钥随本文件分发；
# 也可直接填写下方的 api_key。

# API 密钥（缺省时读取环境变量 AIFLOW_API_KEY）
api_key: ""

# OpenAI 兼容 API 地址（缺省时读取 AIFLOW_API_BASE）
base_url: https://api.deepseek.com/v1

# 默认模型名（缺省时读取 AIFLOW_MODEL，也可用 -m 覆盖）
model: deepseek-chat

# 采样温度，范围通常 0~2（缺省 0.7）
temperature: 0.7

# 最大输出 token 数，0 表示使用服务端默认
max_tokens: 0

# 请求超时时间（秒）
timeout: 120

# 429 / 5xx 时的重试次数
retries: 2
`

// File 对应 config.yaml 中的可配置项。
// 指针字段用于区分"未设置"与"显式设为 0"（如 temperature: 0）。
type File struct {
	APIKey    string   `yaml:"api_key"`
	BaseURL   string   `yaml:"base_url"`
	Model     string   `yaml:"model"`
	Temp      *float64 `yaml:"temperature"`
	MaxTokens *int     `yaml:"max_tokens"`
	Timeout   *int     `yaml:"timeout"`
	Retries   *int     `yaml:"retries"`
}

// Load 按查找顺序读取配置文件，返回解析结果与所用路径。
// 找不到任何配置文件时返回空配置与空路径（全部使用内置默认值）。
// 文件存在但无法解析（YAML 语法错误、未知字段等）时返回错误。
func Load() (*File, string, error) {
	path := ""
	switch {
	case os.Getenv("AIFLOW_CONFIG") != "":
		path = os.Getenv("AIFLOW_CONFIG")
	case fileExists("config/config.yaml"):
		path = "config/config.yaml"
	case fileExists("config.yaml"):
		path = "config.yaml"
	}
	if path == "" {
		return &File{}, "", nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	cfg := &File{}
	// KnownFields=true：出现未识别字段直接报错，尽早发现拼写错误
	if err := yaml.UnmarshalStrict(b, cfg); err != nil {
		return nil, path, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	return cfg, path, nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// Generate 将默认配置模板写入指定路径。
// 若父目录不存在会自动创建。文件已存在时不覆盖，返回明确错误。
func Generate(path string) error {
	if fileExists(path) {
		return fmt.Errorf("配置文件已存在: %s（如需覆盖请先删除）", path)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(defaultTemplate), 0o644); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", path, err)
	}
	return nil
}

// EnsureDefault 在默认路径没有配置文件时自动生成一份。
// 返回 (path, created)，created=true 表示本次生成了新文件。
func EnsureDefault() (string, bool, error) {
	switch {
	case os.Getenv("AIFLOW_CONFIG") != "":
		// 用户显式指定了路径，不管是否存在都不自动生成
		return "", false, nil
	case fileExists(DefaultPath):
		return DefaultPath, false, nil
	case fileExists("config.yaml"):
		return "config.yaml", false, nil
	}
	if err := Generate(DefaultPath); err != nil {
		return "", false, err
	}
	return DefaultPath, true, nil
}
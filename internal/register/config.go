package register

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 默认值（可在 .env / 环境变量 / 命令行覆盖）。
const (
	DefaultGateway     = "http://127.0.0.1:10087"
	DefaultLanguage    = "en"
	DefaultUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	DefaultVerifyWait  = 120 * time.Second
	DefaultPollEvery   = 1 * time.Second
	DefaultMinInterval = 800 * time.Millisecond
	DefaultProbeModel  = "glm-5.3-flash"
)

// Config 是注册链路的运行配置。
type Config struct {
	GraphMailFile string

	Gateway       string
	AdminPassword string

	// Proxy / ProxyPool：单个代理与轮询代理池（完整 URL）。
	Proxy     string
	ProxyPool string
	ProxyFile string

	Language     string
	UserAgent    string
	VerifyWait   time.Duration
	PollEvery    time.Duration
	MinInterval  time.Duration
	ProbeModel   string
	DefaultGroup string
}

// LoadConfig 读取环境变量，并尝试加载当前目录 / 可执行文件目录的 .env。
func LoadConfig() *Config {
	loadEnvFiles()

	cfg := &Config{
		GraphMailFile: env("DEEPAI_GRAPH_MAIL_FILE"),
		Gateway:       firstNonEmpty(env("DEEPAI2API_GATEWAY"), DefaultGateway),
		AdminPassword: env("DEEPAI2API_ADMIN_PASSWORD"),
		Proxy:         env("DEEPAI_REGISTER_PROXY"),
		ProxyPool:     firstNonEmpty(env("PROXY_POOL"), env("DEEPAI_REGISTER_PROXY_POOL")),
		ProxyFile:     env("PROXY_POOL_FILE"),
		Language:      firstNonEmpty(env("DEEPAI_REGISTER_LANGUAGE"), DefaultLanguage),
		UserAgent:     firstNonEmpty(env("DEEPAI_REGISTER_UA"), DefaultUserAgent),
		VerifyWait:    DefaultVerifyWait,
		PollEvery:     DefaultPollEvery,
		MinInterval:   DefaultMinInterval,
		ProbeModel:    firstNonEmpty(env("DEEPAI_REGISTER_PROBE_MODEL"), DefaultProbeModel),
		DefaultGroup:  firstNonEmpty(env("DEEPAI_REGISTER_GROUP"), "default"),
	}
	return cfg
}

// AutoBackend 返回唯一支持的邮箱后端。
func (c *Config) AutoBackend() string { return "graph" }

// Validate 返回缺失的必需项。
func (c *Config) Validate(backend string) []string {
	if c.GraphMailFile == "" {
		return []string{"DEEPAI_GRAPH_MAIL_FILE"}
	}
	return nil
}

// loadEnvFiles 按优先级加载 .env；真实环境变量优先。
func loadEnvFiles() {
	var paths []string
	if p := strings.TrimSpace(os.Getenv("DEEPAI2API_ENV_FILE")); p != "" {
		paths = append(paths, p)
	}
	if wd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(wd, ".env"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		paths = append(paths, filepath.Join(dir, ".env"), filepath.Join(dir, "..", ".env"))
	}
	for _, p := range paths {
		loadDotEnv(p)
	}
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

package register

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
)

// ProxyPool 是线程安全的轮询代理池。
type ProxyPool struct {
	entries []string

	mu   sync.Mutex
	next int
}

// NewProxyPool 解析代理列表；raw 为空时返回 (nil, nil)。
//
// 支持换行 / 逗号 / 分号分隔，元素必须是完整 URL：
// http://user:pass@host:port、https://... 或 socks5://...
func NewProxyPool(raw string) (*ProxyPool, error) {
	entries := ParseProxyList(raw)
	if len(entries) == 0 {
		return nil, nil
	}
	for _, entry := range entries {
		u, err := url.Parse(entry)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("代理格式非法：%s", ProxyLabel(entry))
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("不支持的代理协议 %q：%s", u.Scheme, ProxyLabel(entry))
		}
	}
	return &ProxyPool{entries: entries}, nil
}

// ParseProxyList 把多行 / 逗号分隔的代理串解析为列表。
func ParseProxyList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer("\r\n", "\n", "\r", "\n", ";", "\n", ",", "\n", " ", "\n", "\t", "\n")
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(replacer.Replace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// Len 返回代理数量。
func (p *ProxyPool) Len() int {
	if p == nil {
		return 0
	}
	return len(p.entries)
}

// Next 轮询返回一个代理。
// CapConcurrency 把并发压到不超过出口数量。
//
// 并发高于出口时多出来的 worker 只会排队等代理，wall clock 不变而
// p95 变差；返回调整后的并发与是否发生了压缩（便于调用方提示）。
func (p *ProxyPool) CapConcurrency(n int) (int, bool) {
	if p == nil || n <= 0 {
		return n, false
	}
	if size := p.Len(); size > 0 && n > size {
		return size, true
	}
	return n, false
}

func (p *ProxyPool) Next() string {
	if p == nil || len(p.entries) == 0 {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	raw := p.entries[p.next%len(p.entries)]
	p.next++
	return raw
}

// All 返回代理列表副本。
func (p *ProxyPool) All() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.entries))
	copy(out, p.entries)
	return out
}

// ProxyLabel 返回不含凭据的 host:port（用于日志 / 控制台）。
func ProxyLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Host
}

// ProxyFingerprint 是代理的稳定短标识（用于去重/展示，不含凭据）。
func ProxyFingerprint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return deepai.KeyFingerprint(raw)
}

// CheckProxy 通过代理请求上游模型目录，验证“代理 → 上游”链路。
func CheckProxy(ctx context.Context, raw string, timeout time.Duration) error {
	client := deepai.NewClientWithProxy(raw)
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	models, _, err := client.FetchModels(cctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("代理返回空模型目录")
	}
	return nil
}

// CheckProxyPool 并发检查代理池，返回 标签→错误 的映射（成功为空串）。
func CheckProxyPool(ctx context.Context, pool *ProxyPool, concurrency int) map[string]string {
	results := map[string]string{}
	if pool == nil || pool.Len() == 0 {
		return results
	}
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > pool.Len() {
		concurrency = pool.Len()
	}
	type item struct{ label, raw string }
	ch := make(chan item)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				err := CheckProxy(ctx, it.raw, 30*time.Second)
				mu.Lock()
				if err != nil {
					results[it.label] = err.Error()
				} else {
					results[it.label] = ""
				}
				mu.Unlock()
			}
		}()
	}
	seen := map[string]bool{}
	for _, raw := range pool.All() {
		label := ProxyLabel(raw)
		if seen[label] {
			label = label + "#" + ProxyFingerprint(raw)[:4]
		}
		seen[label] = true
		ch <- item{label: label, raw: raw}
	}
	close(ch)
	wg.Wait()
	return results
}

// ReadProxyFile 读取代理文件（每行一个，支持 # 注释）。
func ReadProxyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// EmptyProxyClient 返回一个禁用代理的客户端（测试用）。
func EmptyProxyClient() *http.Client { return &http.Client{} }

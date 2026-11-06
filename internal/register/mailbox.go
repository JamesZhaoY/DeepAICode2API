package register

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrNoInbox 表示当前邮箱后端没有收件箱（无法自动点验证链接）。
var ErrNoInbox = errors.New("邮箱后端没有收件箱")

// verifyLinkRe 匹配 DeepAI 的邮箱验证链接。
var verifyLinkRe = regexp.MustCompile(`https?://api\.deepai\.org/verify_email\?key=[0-9a-fA-F-]+`)

// Mail 是一封收件箱邮件。
type Mail struct {
	ID         string
	To         string
	From       string
	Subject    string
	Body       string
	ReceivedAt int64 // 毫秒
}

// Mailbox 是可收信的邮箱。
type Mailbox interface {
	Email() string
	Provider() string
	// Fresh 报告这个地址是否是本次新建的。
	//
	// 新建地址必然未注册，"邮箱是否已存在"的预检（一次网络往返，实测 ~3.3s）
	// 可以直接跳过；补跑 / 用户指定的地址则必须预检。
	Fresh() bool
	// WaitForVerification 轮询等待验证邮件并返回验证链接；无收件箱时返回 ErrNoInbox。
	WaitForVerification(ctx context.Context, since time.Time, timeout, interval time.Duration) (string, error)
}

// Provider 是邮箱后端工厂。
type Provider interface {
	Name() string
	Create(ctx context.Context) (Mailbox, error)
}

// InboxProvider 允许为已有地址打开收件箱（不新建邮箱）。
type InboxProvider interface {
	Inbox(email string) (Mailbox, bool)
}

// HealthProvider 允许后端做只读健康检查（doctor 用）。
type HealthProvider interface {
	Health(ctx context.Context) error
}

// NewProvider 构造唯一支持的 Microsoft Graph 邮箱后端。
func NewProvider(cfg *Config, backend, domain, email string) (Provider, error) {
	if cfg == nil {
		cfg = LoadConfig()
	}
	name := strings.ToLower(strings.TrimSpace(backend))
	if name != "" && name != "graph" {
		return nil, fmt.Errorf("不支持邮箱后端 %q，仅支持 Hotmail / Microsoft Graph", name)
	}
	return newGraphProvider(cfg.GraphMailFile)
}

// extractVerifyLink 从主题 / 正文中提取验证链接。
func extractVerifyLink(m Mail) string {
	text := m.Subject + "\n" + m.Body
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "verif") && !strings.Contains(lower, "verify_email") {
		return ""
	}
	return verifyLinkRe.FindString(text)
}

// --- 无收件箱邮箱 ---

type noInboxMailbox struct {
	email    string
	provider string
	fresh    bool
}

func (m *noInboxMailbox) Email() string    { return m.email }
func (m *noInboxMailbox) Provider() string { return m.provider }
func (m *noInboxMailbox) Fresh() bool      { return m.fresh }
func (m *noInboxMailbox) WaitForVerification(context.Context, time.Time, time.Duration, time.Duration) (string, error) {
	return "", ErrNoInbox
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// CheckProvider 对邮箱后端做只读健康检查；无健康检查接口时直接通过。
func CheckProvider(ctx context.Context, cfg *Config, backend, domain string) error {
	p, err := NewProvider(cfg, backend, domain, "probe@example.com")
	if err != nil {
		return err
	}
	if hp, ok := p.(HealthProvider); ok {
		return hp.Health(ctx)
	}
	return nil
}

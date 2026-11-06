package register

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type graphAccount struct {
	email, clientID, refresh, access string
	expires                          time.Time
	mu                               sync.Mutex
}
type graphProvider struct {
	accounts []*graphAccount
	byEmail  map[string]*graphAccount
	path     string
	client   *http.Client
}

func newGraphProvider(path string) (*graphProvider, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("DEEPAI_GRAPH_MAIL_FILE 未配置")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 Graph 邮箱文件失败: %w", err)
	}
	defer f.Close()
	p := &graphProvider{path: path, byEmail: map[string]*graphAccount{}, client: &http.Client{Timeout: 30 * time.Second}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		parts := strings.Split(s, "----")
		if len(parts) != 4 {
			return nil, fmt.Errorf("Graph 邮箱文件第 %d 行应包含 4 个字段", line)
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if !strings.Contains(parts[0], "@") || parts[2] == "" || parts[3] == "" {
			return nil, fmt.Errorf("Graph 邮箱文件第 %d 行缺少邮箱、客户端 ID 或刷新令牌", line)
		}
		key := strings.ToLower(parts[0])
		if p.byEmail[key] != nil {
			return nil, fmt.Errorf("Graph 邮箱文件第 %d 行邮箱重复", line)
		}
		a := &graphAccount{email: parts[0], clientID: parts[2], refresh: parts[3]}
		p.accounts = append(p.accounts, a)
		p.byEmail[key] = a
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 Graph 邮箱文件失败: %w", err)
	}
	if len(p.accounts) == 0 {
		return nil, errors.New("Graph 邮箱文件为空")
	}
	return p, nil
}
func (p *graphProvider) Name() string { return "graph" }

// graphStateMu serializes allocations across providers in this process.
var graphStateMu sync.Mutex

type GraphMailStatus struct {
	Email  string `json:"email"`
	Status string `json:"status"`
}

func (p *graphProvider) readState() (map[string]string, error) {
	state := map[string]string{}
	raw, err := os.ReadFile(p.path + ".state.json")
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("Graph 邮箱状态文件无效: %w", err)
	}
	return state, nil
}
func (p *graphProvider) writeState(state map[string]string) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.path), ".graph-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p.path+".state.json")
}
func (p *graphProvider) Statuses() ([]GraphMailStatus, error) {
	graphStateMu.Lock()
	defer graphStateMu.Unlock()
	state, err := p.readState()
	if err != nil {
		return nil, err
	}
	out := make([]GraphMailStatus, 0, len(p.accounts))
	for _, a := range p.accounts {
		status := state[strings.ToLower(a.email)]
		if status == "" {
			status = "available"
		}
		out = append(out, GraphMailStatus{a.email, status})
	}
	return out, nil
}
func (p *graphProvider) MergeHistory(history map[string]string) error {
	graphStateMu.Lock()
	defer graphStateMu.Unlock()
	state, err := p.readState()
	if err != nil {
		return err
	}
	changed := false
	for email, status := range history {
		key := strings.ToLower(strings.TrimSpace(email))
		if p.byEmail[key] == nil {
			continue
		}
		if state[key] == "registered" {
			continue
		}
		if state[key] == "" || status == "registered" {
			state[key] = status
			changed = true
		}
	}
	if changed {
		return p.writeState(state)
	}
	return nil
}
func (p *graphProvider) MarkRegistered(email string) error {
	graphStateMu.Lock()
	defer graphStateMu.Unlock()
	state, err := p.readState()
	if err != nil {
		return err
	}
	state[strings.ToLower(strings.TrimSpace(email))] = "registered"
	return p.writeState(state)
}
func (p *graphProvider) Reserve(email string) (Mailbox, error) {
	graphStateMu.Lock()
	defer graphStateMu.Unlock()
	state, err := p.readState()
	if err != nil {
		return nil, err
	}
	var a *graphAccount
	if email != "" {
		a = p.byEmail[strings.ToLower(strings.TrimSpace(email))]
		if a == nil {
			return nil, errors.New("指定邮箱不在 Graph 邮箱文件中")
		}
		if status := state[strings.ToLower(a.email)]; status != "" && status != "available" {
			return nil, errors.New("该邮箱已使用或注册状态待确认，不能再次注册")
		}
	} else {
		for _, candidate := range p.accounts {
			if status := state[strings.ToLower(candidate.email)]; status == "" || status == "available" {
				a = candidate
				break
			}
		}
	}
	if a == nil {
		return nil, errors.New("没有可用的 Graph 邮箱")
	}
	state[strings.ToLower(a.email)] = "pending"
	if err = p.writeState(state); err != nil {
		return nil, fmt.Errorf("保存邮箱状态失败: %w", err)
	}
	return &graphInbox{p, a}, nil
}
func (p *graphProvider) Create(context.Context) (Mailbox, error) { return p.Reserve("") }
func (p *graphProvider) Inbox(email string) (Mailbox, bool) {
	a, ok := p.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return nil, false
	}
	return &graphInbox{p, a}, true
}
func (m *graphInbox) MarkRegistered() error { return m.p.MarkRegistered(m.a.email) }

type graphInbox struct {
	p *graphProvider
	a *graphAccount
}

func (m *graphInbox) Email() string    { return m.a.email }
func (m *graphInbox) Provider() string { return "graph" }
func (m *graphInbox) Fresh() bool      { return false }
func (m *graphInbox) token(ctx context.Context) (string, error) {
	a := m.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.access != "" && time.Now().Before(a.expires) {
		return a.access, nil
	}
	v := url.Values{"client_id": {a.clientID}, "grant_type": {"refresh_token"}, "refresh_token": {a.refresh}, "scope": {"https://graph.microsoft.com/Mail.Read offline_access"}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://login.microsoftonline.com/consumers/oauth2/v2.0/token", strings.NewReader(v.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Graph 令牌请求失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Error   string `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", errors.New("Graph 令牌响应无效")
	}
	if resp.StatusCode != 200 || out.Access == "" {
		return "", fmt.Errorf("Graph 令牌刷新失败（HTTP %d，%s）", resp.StatusCode, out.Error)
	}
	a.access = out.Access
	if out.Refresh != "" {
		a.refresh = out.Refresh
	}
	a.expires = time.Now().Add(time.Duration(out.Expires)*time.Second - time.Minute)
	return a.access, nil
}
func (m *graphInbox) list(ctx context.Context, since time.Time) ([]Mail, error) {
	token, err := m.token(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"$top": {"50"}, "$orderby": {"receivedDateTime desc"}, "$select": {"id,subject,body,receivedDateTime"}}
	if !since.IsZero() {
		q.Set("$filter", "receivedDateTime ge "+since.UTC().Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Graph 收信失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Graph 收信失败（HTTP %d）", resp.StatusCode)
	}
	var out struct {
		Value []struct {
			ID, Subject string
			Received    time.Time `json:"receivedDateTime"`
			Body        struct {
				Content string `json:"content"`
			} `json:"body"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, errors.New("Graph 邮件响应无效")
	}
	mails := make([]Mail, 0, len(out.Value))
	for _, v := range out.Value {
		mails = append(mails, Mail{ID: v.ID, Subject: v.Subject, Body: html.UnescapeString(v.Body.Content), ReceivedAt: v.Received.UnixMilli()})
	}
	return mails, nil
}
func (m *graphInbox) WaitForVerification(ctx context.Context, since time.Time, timeout, interval time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultVerifyWait
	}
	if interval <= 0 {
		interval = DefaultPollEvery
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		mails, err := m.list(ctx, since)
		if err != nil {
			last = err
		} else {
			for _, mail := range mails {
				if link := extractVerifyLink(mail); link != "" {
					return link, nil
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if last != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("等待验证邮件超时: %w", last)
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

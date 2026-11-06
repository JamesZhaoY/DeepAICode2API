package register

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPEnroller 通过 2API 网关的管理接口把账号写入号池。
type HTTPEnroller struct {
	BaseURL       string
	AdminPassword string
	HTTP          *http.Client
}

// NewHTTPEnroller 构造入池客户端。
func NewHTTPEnroller(baseURL, adminPassword string) *HTTPEnroller {
	return &HTTPEnroller{
		BaseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		AdminPassword: adminPassword,
		HTTP:          &http.Client{Timeout: 90 * time.Second},
	}
}

// Enroll 调用 POST /admin/api/accounts（action=enroll）。
func (e *HTTPEnroller) Enroll(ctx context.Context, rec *Record) (string, error) {
	if e == nil || e.BaseURL == "" {
		return "", fmt.Errorf("未配置 2API 网关地址")
	}
	body := map[string]any{
		"action":          "enroll",
		"api_key":         rec.APIKey,
		"email":           rec.Email,
		"password":        rec.Password,
		"username":        rec.Username,
		"session_id":      rec.SessionID,
		"csrftoken":       rec.CSRFToken,
		"user_pk":         rec.UserPK,
		"plan":            rec.Plan,
		"email_verified":  rec.EmailVerified,
		"group":           rec.Group,
		"proxy":           rec.Proxy,
		"fingerprint":     rec.Fingerprint,
		"machine_id":      rec.MachineID,
		"user_agent":      rec.UserAgent,
		"accept_language": rec.AcceptLanguage,
		"sec_ch_ua":       rec.SecCHUA,
		"platform":        rec.Platform,
		"theos_balance":   rec.TheosBalance,
		"theos_granted":   rec.TheosGranted,
		// 本链路刚刚验活过，网关不必再探一次 /theos。
		"skip_probe": strings.TrimSpace(rec.TheosBalance) != "",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/admin/api/accounts", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.AdminPassword != "" {
		req.Header.Set("X-Admin-Password", e.AdminPassword)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接 2API 网关失败: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("入池失败 HTTP %d: %s", resp.StatusCode, truncate(string(payload), 300))
	}
	var out struct {
		ID  string `json:"id"`
		New bool   `json:"new"`
		Err any    `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("入池响应解析失败: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("入池响应缺少账号 ID: %s", truncate(string(payload), 200))
	}
	return out.ID, nil
}

// MarkVerified 回填账号的 email_verified。
//
// 验证邮件常常在入池之后才到（实测 14/50 等到 120s 超时），
// 所以验证结果走后台回填而不是阻塞入池。
func (e *HTTPEnroller) MarkVerified(ctx context.Context, accountID, email string) error {
	if e == nil || e.BaseURL == "" {
		return fmt.Errorf("未配置 2API 网关地址")
	}
	raw, err := json.Marshal(map[string]any{
		"action": "mark-verified",
		"id":     accountID,
		"email":  email,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/admin/api/accounts", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.AdminPassword != "" {
		req.Header.Set("X-Admin-Password", e.AdminPassword)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连接 2API 网关失败: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("回填验证状态失败 HTTP %d: %s", resp.StatusCode, truncate(string(payload), 300))
	}
	return nil
}

// Health 检查网关是否可达（doctor 用）。
func (e *HTTPEnroller) Health(ctx context.Context) error {
	if e == nil || e.BaseURL == "" {
		return fmt.Errorf("未配置网关地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("网关 /health HTTP %d", resp.StatusCode)
	}
	return nil
}

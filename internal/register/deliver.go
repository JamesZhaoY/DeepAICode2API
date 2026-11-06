package register

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Deliverables 是一次运行写出的交付物路径。
type Deliverables struct {
	KeysTxt     string
	APIKeysTxt  string
	SuccessJSON string
}

// WriteDeliverables 写出 keys.txt / apikeys.txt / success.jsonl。
//
// 只写“已验活且有 API Key”的记录；按邮箱去重，等级更高者胜出。
func WriteDeliverables(dir string, records []*Record) (*Deliverables, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("交付物目录为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	byEmail := map[string]*Record{}
	for _, rec := range records {
		if rec == nil || rec.APIKey == "" {
			continue
		}
		rec.Normalize()
		old := byEmail[rec.Email]
		if old == nil || StatusRank(rec.Status) >= StatusRank(old.Status) {
			byEmail[rec.Email] = rec
		}
	}

	emails := make([]string, 0, len(byEmail))
	for email := range byEmail {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	out := &Deliverables{
		KeysTxt:     filepath.Join(dir, "keys.txt"),
		APIKeysTxt:  filepath.Join(dir, "apikeys.txt"),
		SuccessJSON: filepath.Join(dir, "success.jsonl"),
	}

	var keys, apiKeys, success strings.Builder
	for _, email := range emails {
		rec := byEmail[email]
		accountID := rec.AccountID
		if accountID == "" {
			accountID = "-"
		}
		keys.WriteString(fmt.Sprintf("%s----%s----%s\n", email, rec.APIKey, accountID))
		apiKeys.WriteString(rec.APIKey + "\n")
		// 交付物会被打包分发，必须脱敏：密码 / Session 不进 success.jsonl。
		raw, err := json.Marshal(redactRecord(rec))
		if err != nil {
			continue
		}
		success.Write(raw)
		success.WriteByte('\n')
	}

	if err := os.WriteFile(out.KeysTxt, []byte(keys.String()), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out.APIKeysTxt, []byte(apiKeys.String()), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out.SuccessJSON, []byte(success.String()), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

// Summary 汇总一次运行的结果。
type Summary struct {
	Total     int
	Enrolled  int
	Keyed     int
	Verified  int
	Failed    int
	WithKey   int
	VerifiedE int
}

// Summarize 统计记录。
func Summarize(records []*Record) Summary {
	var s Summary
	for _, rec := range records {
		if rec == nil {
			continue
		}
		s.Total++
		switch rec.Status {
		case StatusEnrolled:
			s.Enrolled++
		case StatusKeyed:
			s.Keyed++
		case StatusVerified:
			s.Verified++
		case StatusFailed:
			s.Failed++
		}
		if rec.APIKey != "" {
			s.WithKey++
		}
		if rec.EmailVerified {
			s.VerifiedE++
		}
	}
	return s
}

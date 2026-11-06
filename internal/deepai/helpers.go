package deepai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// RandomHex 生成 n 字节的随机十六进制串。
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

// RandomToken 生成 n 字节随机数的十六进制串（用于 OAuth state）。
func RandomToken(n int) string { return RandomHex(n) }

// KeyFingerprint 返回 API Key 的 sha256 前 16 位十六进制。
func KeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:8])
}

// ShortHash 返回 API Key 的短哈希，用于展示。
func ShortHash(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:4])
}

// MaskSecret 对敏感串脱敏，仅保留首尾片段。
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 10 {
		return s[:1] + "***"
	}
	return s[:6] + "..." + s[len(s)-4:]
}

// HomeDir 返回当前用户主目录（失败时回落到当前目录）。
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return h
	}
	return "."
}

// DesktopBaseDirs 返回本机 DeepAI Code 桌面端数据目录候选。
//
// 上游逻辑（apps/desktop/DesktopStatePaths.ts）：
//
//	baseDir = T3CODE_HOME || ~/.deepai-code
//	stateDir = baseDir/userdata（开发模式且未显式指定时用 baseDir/dev）
//
// secrets 位于 stateDir/secrets。
func DesktopBaseDirs(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	var out []string
	for _, env := range []string{"T3CODE_HOME", "DEEPAI_CODE_HOME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			out = append(out, v)
		}
	}
	out = append(out, filepath.Join(HomeDir(), ".deepai-code"))
	return dedupe(out)
}

// DesktopSecretDirs 把 baseDir 展开为可能包含 secrets 的目录。
//
// 同时接受 baseDir、stateDir、secrets 目录或凭据文件本身，
// 方便用户直接把路径指到任意层级。
func DesktopSecretDirs(explicit string) []string {
	var out []string
	for _, base := range DesktopBaseDirs(explicit) {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		info, err := os.Stat(base)
		if err == nil && !info.IsDir() {
			out = append(out, filepath.Dir(base))
			continue
		}
		out = append(out,
			filepath.Join(base, "userdata", "secrets"),
			filepath.Join(base, "secrets"),
			filepath.Join(base, "dev", "secrets"),
		)
	}
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		key := strings.ToLower(filepath.Clean(v))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

// EnvOr 返回环境变量值或默认值。
func EnvOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// EnvInt 返回整型环境变量或默认值。
func EnvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// MaskProxy 只返回代理的 host:port（绝不泄露用户名/密码）。
func MaskProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

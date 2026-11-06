package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 状态文件封装。
//
// 状态文件里有上游 Api-Key、网页账号密码与网关密钥；文件权限 0600 只在
// Unix 上有效（Windows 的 os.WriteFile 不设 ACL），所以支持可选的整文件加密：
//
//	v1（历史格式，裸 JSON）  {"accounts":[...],...}
//	v2（加密格式）           {"v":2,"alg":"aes-256-gcm","nonce":"…","ct":"…"}
//
// 读到 v1 且已配置主密钥时，下一次 Save 会自动写成 v2；未配置主密钥时
// 保持 v1 不变（不破坏既有部署）。
const (
	sealVersion = 2
	sealAlg     = "aes-256-gcm"

	// EnvMasterKey 是状态文件加密主密钥的环境变量名。
	EnvMasterKey = "DEEPAI2API_MASTER_KEY"

	aes256KeyLen = 32
)

// 封装层可能出现的错误。
var (
	// ErrKeyRequired 表示文件已加密但未提供主密钥。
	ErrKeyRequired = errors.New("状态文件已加密，但未设置 " + EnvMasterKey)
	// ErrKeyMismatch 表示主密钥不匹配（解密后 GCM 校验失败）。
	ErrKeyMismatch = errors.New("主密钥不匹配，无法解密状态文件")
)

// sealEnvelope 是加密状态文件的外层结构。
type sealEnvelope struct {
	V     int    `json:"v"`
	Alg   string `json:"alg"`
	Nonce string `json:"nonce"`
	CT    string `json:"ct"`
}

// ParseMasterKey 解析主密钥：支持 64 位 hex 或 44 位 base64，必须解出 32 字节。
func ParseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == aes256KeyLen {
		return key, nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(raw); err == nil && len(key) == aes256KeyLen {
			return key, nil
		}
	}
	return nil, fmt.Errorf("%s 必须是 32 字节（64 位 hex 或 base64），可用 openssl rand -hex 32 生成", EnvMasterKey)
}

// MasterKeyFromEnv 从环境变量读取主密钥（未设置时返回 nil）。
func MasterKeyFromEnv() ([]byte, error) {
	return ParseMasterKey(os.Getenv(EnvMasterKey))
}

// isEnvelope 判断原始文件内容是否为加密封装。
func isEnvelope(raw []byte) bool {
	var probe sealEnvelope
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	return probe.V == sealVersion && probe.Alg != ""
}

// seal 加密明文状态。
func seal(key, plain []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成随机数失败: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	return json.MarshalIndent(sealEnvelope{
		V:     sealVersion,
		Alg:   sealAlg,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		CT:    base64.StdEncoding.EncodeToString(ct),
	}, "", "  ")
}

// unseal 解密状态文件内容。
//
// 返回值 encrypted 表示该文件原本是加密格式；plain 是明文字节。
// 明文（v1）文件直接原样返回。
func unseal(key, raw []byte) (plain []byte, encrypted bool, err error) {
	if !isEnvelope(raw) {
		return raw, false, nil
	}
	if len(key) == 0 {
		return nil, true, ErrKeyRequired
	}
	var env sealEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, true, fmt.Errorf("状态文件封装解析失败: %w", err)
	}
	if env.Alg != sealAlg {
		return nil, true, fmt.Errorf("不支持的加密算法 %q（期望 %q）", env.Alg, sealAlg)
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, true, fmt.Errorf("nonce 解码失败: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		return nil, true, fmt.Errorf("密文解码失败: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, true, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, true, fmt.Errorf("nonce 长度 %d，期望 %d", len(nonce), gcm.NonceSize())
	}
	opened, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// GCM 校验失败几乎总是密钥不对，给出可执行的提示而不是底层错误。
		return nil, true, ErrKeyMismatch
	}
	return opened, true, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != aes256KeyLen {
		return nil, fmt.Errorf("主密钥长度 %d，期望 %d", len(key), aes256KeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

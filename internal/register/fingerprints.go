package register

import (
	"crypto/rand"
	"hash/fnv"
	"math/big"
	"strings"
)

// Fingerprint 是一套自洽的浏览器指纹请求头。
type Fingerprint struct {
	ID             string
	UserAgent      string
	Platform       string // 带引号，例如 "Windows"
	SecCHUA        string
	Mobile         bool
	AcceptLanguage string
	Language       string // x-deepai-language
}

// fingerprintProfiles 是内置指纹库（UA / Sec-CH-UA / 平台保持一致）。
var fingerprintProfiles = []Fingerprint{
	{
		ID:             "win-chrome-156",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36",
		Platform:       `"Windows"`,
		SecCHUA:        `"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"`,
		AcceptLanguage: "en-US,en;q=0.9",
		Language:       "en",
	},
	{
		ID:             "win-chrome-155",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36",
		Platform:       `"Windows"`,
		SecCHUA:        `"Chromium";v="155", "Google Chrome";v="155", "Not_A Brand";v="99"`,
		AcceptLanguage: "en-US,en;q=0.9",
		Language:       "en",
	},
	{
		ID:             "mac-chrome-156",
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36",
		Platform:       `"macOS"`,
		SecCHUA:        `"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"`,
		AcceptLanguage: "en-US,en;q=0.9",
		Language:       "en",
	},
	{
		ID:             "win-edge-156",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0",
		Platform:       `"Windows"`,
		SecCHUA:        `"Not:A-Brand";v="8", "Chromium";v="156", "Microsoft Edge";v="156"`,
		AcceptLanguage: "en-US,en;q=0.9",
		Language:       "en",
	},
	{
		ID:             "linux-chrome-152",
		UserAgent:      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36",
		Platform:       `"Linux"`,
		SecCHUA:        `"Chromium";v="152", "Google Chrome";v="152", "Not_A Brand";v="99"`,
		AcceptLanguage: "en-US,en;q=0.9",
		Language:       "en",
	},
	{
		ID:             "win-chrome-156-zh",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36",
		Platform:       `"Windows"`,
		SecCHUA:        `"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"`,
		AcceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8",
		Language:       "zh-hans",
	},
}

// FingerprintByID 按 ID 查找指纹；未找到返回零值。
func FingerprintByID(id string) Fingerprint {
	id = strings.TrimSpace(id)
	for _, fp := range fingerprintProfiles {
		if fp.ID == id {
			return fp
		}
	}
	return Fingerprint{}
}

// AssignFingerprint 按种子稳定分配指纹（同一邮箱补跑得到同一套请求头）。
func AssignFingerprint(seed string) Fingerprint {
	if len(fingerprintProfiles) == 0 {
		return Fingerprint{}
	}
	seed = strings.ToLower(strings.TrimSpace(seed))
	if seed == "" {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(fingerprintProfiles))))
		if err != nil {
			return fingerprintProfiles[0]
		}
		return fingerprintProfiles[n.Int64()]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return fingerprintProfiles[int(h.Sum32())%len(fingerprintProfiles)]
}

// Fingerprints 返回指纹库副本（doctor / 控制台展示用）。
func Fingerprints() []Fingerprint {
	out := make([]Fingerprint, len(fingerprintProfiles))
	copy(out, fingerprintProfiles)
	return out
}

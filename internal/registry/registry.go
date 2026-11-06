// Package registry 维护对外暴露的模型清单与别名解析。
//
// DeepAI Code 的模型目录由上游 /deepai_code/models 动态下发，本包只做
// 缓存快照上的解析与别名扩展，不承担网络请求。
package registry

import (
	"strings"

	"deepaicode2api/internal/deepai"
)

// Resolve 把用户传入的模型名解析为目录中的模型。
//
// 依次尝试：去掉 deepai/ 前缀 → 精确 ID → 自定义别名 → 内置别名 →
// 包含匹配 → 回落到 defaultID → 回落到第一个模型。
//
// 无法识别时不报错而是回落：这样任意客户端（Claude Code / Cline /
// OpenAI SDK）都能用自己习惯的模型名开箱即用。
func Resolve(catalog []deepai.Model, aliases map[string]string, name, defaultID string) deepai.Model {
	n := normalize(name)
	if n != "" {
		if m, ok := lookup(catalog, n); ok {
			return m
		}
		if target, ok := lookupAlias(aliases, n); ok {
			if m, ok := lookup(catalog, target); ok {
				return m
			}
		}
		if target, ok := builtinAliases[n]; ok {
			if m, ok := lookup(catalog, target); ok {
				return m
			}
		}
		// 宽松匹配：模型名包含用户输入，或用户输入包含模型 ID。
		for _, m := range catalog {
			if strings.Contains(strings.ToLower(m.ID), n) {
				return m
			}
		}
		for _, m := range catalog {
			if n == strings.ToLower(strings.ReplaceAll(m.Name, " ", "-")) {
				return m
			}
		}
	}

	if m, ok := lookup(catalog, normalize(defaultID)); ok {
		return m
	}
	if len(catalog) > 0 {
		return catalog[0]
	}
	return deepai.DefaultModelEntry()
}

// List 返回对外模型清单（含自定义别名）。
func List(catalog []deepai.Model, aliases map[string]string) []deepai.Model {
	out := make([]deepai.Model, len(catalog))
	copy(out, catalog)
	for alias, target := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if m, ok := lookup(catalog, normalize(target)); ok {
			m.ID = alias
			m.Name = alias + " → " + m.Name
			out = append(out, m)
		}
	}
	return out
}

// builtinAliases 是常见客户端模型名到 DeepAI 模型的映射。
//
// 只包含默认免费模型：用户可在控制台覆盖为任意目标。
var builtinAliases = map[string]string{
	"deepai":            deepai.DefaultModel,
	"deepai-code":       deepai.DefaultModel,
	"default":           deepai.DefaultModel,
	"gpt-3.5-turbo":     deepai.DefaultModel,
	"gpt-4":             deepai.DefaultModel,
	"gpt-4o":            deepai.DefaultModel,
	"gpt-4o-mini":       deepai.DefaultModel,
	"gpt-4.1":           deepai.DefaultModel,
	"claude-3-5-sonnet": deepai.DefaultModel,
	"claude-sonnet-4":   deepai.DefaultModel,
	"claude-3-haiku":    deepai.DefaultModel,
	"deepseek-chat":     deepai.DefaultModel,
	"glm-4-flash":       deepai.DefaultModel,
}

func normalize(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	// 客户端常用 deepai/<id> 或 openai/<id> 前缀。
	for _, prefix := range []string{"deepai/", "deepai:", "openai/", "openai:"} {
		if strings.HasPrefix(n, prefix) {
			n = strings.TrimSpace(strings.TrimPrefix(n, prefix))
			break
		}
	}
	return n
}

func lookup(catalog []deepai.Model, id string) (deepai.Model, bool) {
	if id == "" {
		return deepai.Model{}, false
	}
	for _, m := range catalog {
		if strings.EqualFold(m.ID, id) {
			return m, true
		}
	}
	return deepai.Model{}, false
}

func lookupAlias(aliases map[string]string, key string) (string, bool) {
	for k, v := range aliases {
		if strings.EqualFold(strings.TrimSpace(k), key) {
			return normalize(v), true
		}
	}
	return "", false
}

// Find 返回 ID 精确匹配的模型。
func Find(catalog []deepai.Model, id string) (deepai.Model, bool) {
	return lookup(catalog, normalize(id))
}

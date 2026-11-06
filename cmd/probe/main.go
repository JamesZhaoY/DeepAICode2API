// probe 是 DeepAICode2API 的诊断工具：验证凭据、模型目录与一次真实推理。
//
//	go run ./cmd/probe -prompt "hello"
//	go run ./cmd/probe -key <api-key> -model glm-5.3-flash -stream
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"deepaicode2api/internal/deepai"
)

func main() {
	var (
		dir    = flag.String("dir", "", "DeepAI Code 数据目录（留空自动探测）")
		key    = flag.String("key", "", "直接使用指定 API Key（留空读取桌面端凭据）")
		model  = flag.String("model", "", "要测试的模型（留空使用目录默认）")
		prompt = flag.String("prompt", "", "设置后执行一次对话测试")
		stream = flag.Bool("stream", false, "对话测试是否使用流式")
		usage  = flag.Bool("usage", false, "查询 Theos 配额与用量摘要")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := deepai.NewClient()
	fmt.Println("上游:", client.Origin)
	if client.Machine == "" {
		fmt.Println("机器指纹: 不可用（免费 Theos 可能被拒绝）")
	} else {
		fmt.Println("机器指纹:", client.Machine)
	}

	acct := resolveAccount(*dir, *key)
	if acct == nil {
		fmt.Println("未找到账号：请先在桌面端登录，或用 -key 指定 API Key")
		os.Exit(1)
	}
	fmt.Printf("账号: %s（username=%s，key=%s）\n", acct.Name, acct.Username, deepai.MaskSecret(acct.APIKey))

	models, def, err := client.FetchModels(ctx)
	if err != nil {
		fmt.Println("模型目录拉取失败:", err)
		os.Exit(1)
	}
	if *model == "" {
		*model = def
	}
	fmt.Printf("模型目录: %d 个，默认 %s\n", len(models), def)
	for _, m := range models {
		mark := " "
		if m.ID == *model {
			mark = "*"
		}
		api := m.API
		if api == "" {
			api = "chat"
		}
		fmt.Printf(" %s %-22s %-24s %-6s theos=%-4s ctx=%-8d api=%s\n",
			mark, m.ID, m.Name, plan(m), price(m), m.ContextWindow, api)
	}

	if *usage {
		al, err := client.Theos(ctx, acct)
		if err != nil {
			fmt.Println("Theos 查询失败:", err)
		} else {
			fmt.Printf("Theos: enabled=%v balance=%s granted=%s refresh_at=%s lane=%d\n",
				al.Enabled, al.Balance, al.Granted, al.RefreshAt, al.LaneVersion)
		}
		us, err := client.UsageSummary(ctx, acct)
		if err != nil {
			fmt.Println("用量查询失败:", err)
		} else {
			b, _ := json.Marshal(us)
			fmt.Println("用量:", string(b))
		}
	}

	if *prompt == "" {
		return
	}

	payload := map[string]any{
		"model":    *model,
		"stream":   *stream,
		"messages": []any{map[string]any{"role": "user", "content": *prompt}},
	}
	body, _ := json.Marshal(payload)
	path := deepai.PathChat
	if m, ok := find(models, *model); ok && m.IsResponses() {
		path = deepai.PathResponses
		fmt.Println("注意: 该模型使用 /responses 端点，probe 仅支持 chat 请求体")
	}

	resp, err := client.OpenModel(ctx, acct, path, body, "")
	if err != nil {
		fmt.Println("请求失败:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	fmt.Println("HTTP", resp.StatusCode)

	if !*stream {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		var out map[string]any
		if json.Unmarshal(raw, &out) == nil {
			pretty, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(pretty))
		} else {
			fmt.Println(string(raw))
		}
		return
	}

	fmt.Println("--- stream ---")
	_ = deepai.ReadSSE(resp.Body, func(ev deepai.SSEEvent) error {
		if ev.Data == "[DONE]" {
			fmt.Println("[DONE]")
			return nil
		}
		fmt.Println(ev.Data)
		return nil
	})
}

func resolveAccount(dir, key string) *deepai.Account {
	if key != "" {
		acct, err := deepai.AccountFromAPIKey(key, "", "probe", false)
		if err != nil {
			fmt.Println("API Key 非法:", err)
			return nil
		}
		return acct
	}
	results, err := deepai.ImportAllFromDesktop(dir)
	if err != nil {
		return nil
	}
	if len(results) == 0 {
		return nil
	}
	return results[0].Account
}

func find(models []deepai.Model, id string) (deepai.Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return deepai.Model{}, false
}

func plan(m deepai.Model) string {
	if m.Premier {
		return "premier"
	}
	return "free"
}

func price(m deepai.Model) string {
	if m.TheosPerRequest == "" {
		return "?"
	}
	return m.TheosPerRequest
}

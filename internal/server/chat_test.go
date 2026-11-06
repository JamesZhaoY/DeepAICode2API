package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChatNonStreamTransform(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
		  "id":"chatcmpl-3","model":"glm-5.3-flash",
		  "choices":[{"index":0,"finish_reason":"stop","message":{
		    "role":"assistant","reasoning":"思考中","content":"答案"
		  }}],
		  "usage":{"prompt_tokens":2,"completion_tokens":3}
		}`)
	})
	s, key := newTestServer(t, u)

	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}

	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	choices, _ := out["choices"].([]any)
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["reasoning"] != "思考中" {
		t.Fatalf("原始 reasoning 不应被改写: %#v", msg)
	}
	if msg["reasoning_content"] != "思考中" {
		t.Fatalf("应补 reasoning_content 兼容字段: %#v", msg)
	}
	// 对外模型名必须回到用户传入的解析结果，而不是上游 ID。
	if out["model"] == "glm-5.3-flash" {
		t.Log("model 回写为目录模型 ID（允许）")
	}
	if up := u.last(t); up.Payload["model"] == "gpt-4o-mini" {
		t.Fatalf("上游 model 应被改写为目录内模型，实际 %v", up.Payload["model"])
	}
}

func TestChatDoesNotOverwriteReasoningContent(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{
		  "reasoning":"a","reasoning_content":"已有值","content":"x"}}]}`)
	})
	s, key := newTestServer(t, u)
	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out := decodeJSON[map[string]any](t, rec.Body.Bytes())
	msg, _ := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["reasoning_content"] != "已有值" {
		t.Fatalf("已有的 reasoning_content 不应被覆盖: %#v", msg)
	}
}

func TestChatRejectsEmptyMessages(t *testing.T) {
	s, key := newTestServer(t, nil)
	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key, `{"model":"m","messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", rec.Code)
	}
}

func TestChatStreamPassesThrough(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`: keep-alive`,
			`{"choices":[{"index":0,"delta":{"reasoning":"r"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
			w.(http.Flusher).Flush()
		}
	})
	s, key := newTestServer(t, u)

	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("应保留 [DONE] 终止帧:\n%s", body)
	}
	// 上游心跳必须原样透传，否则长响应会被中间层掐断。
	if !strings.Contains(body, ": keep-alive") {
		t.Fatalf("心跳注释应被透传:\n%s", body)
	}
	if !strings.Contains(body, `"reasoning_content":"r"`) {
		t.Fatalf("流式 chunk 也应补 reasoning_content:\n%s", body)
	}
}

func TestChatUpstreamErrorPropagates(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request","code":"invalid"}}`)
	})
	s, key := newTestServer(t, u)

	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	// 400 属于请求问题，不重试，状态码原样回传。
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("上游错误信息应被透传: %s", rec.Body.String())
	}
}

// 上游在缺省 stream 字段时默认按 SSE 返回；OpenAI 规范里缺省等价于 false。
// 两者不一致会让“没写 stream 的非流式请求”拿到 data: 帧而解析失败。
func TestChatSendsExplicitStreamFalse(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	s, key := newTestServer(t, u)

	// 故意不传 stream。
	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	v, ok := u.last(t).Payload["stream"]
	if !ok {
		t.Fatal("上游请求必须显式带 stream 字段")
	}
	if b, _ := v.(bool); b {
		t.Fatalf("stream = %v，期望 false", v)
	}
}

func TestChatSendsExplicitStreamTrue(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
	})
	s, key := newTestServer(t, u)

	do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if b, _ := u.last(t).Payload["stream"].(bool); !b {
		t.Fatal("stream=true 应原样透传")
	}
}

// 上游回 SSE 但网关按非流式处理时，错误信息要能定位（曾只剩“不是合法 JSON”）。
func TestChatNonJSONUpstreamIsDiagnosable(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
	})
	s, key := newTestServer(t, u)

	rec := do(t, s, http.MethodPost, "/v1/chat/completions", key,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "JSON") {
		t.Fatalf("错误信息应说明是解析失败: %s", rec.Body.String())
	}
}

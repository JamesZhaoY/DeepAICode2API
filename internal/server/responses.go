package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"deepaicode2api/internal/deepai"
)

// handleResponses 是 /v1/responses 的入口。
//
// 两条路径：
//   - 目录里 api=openai-responses 的模型：原样透传到上游 /responses；
//   - 其余模型：把 Responses 请求翻译成 Chat Completions，再把结果
//     合成为 Responses 对象 / 事件流。
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	raw, err := readBodyWithLimit(w, r)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大", "invalid_request_error")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	model := s.resolveModel(requestModelName(payload))
	payload["model"] = model.ID
	stream, _ := payload["stream"].(bool)
	affinity := affinityFor(r, payload)
	key := extractKey(r)
	promptLen := estimatePromptLen(payload)

	if model.IsResponses() {
		s.passthroughResponses(w, r, payload, model.ID, stream, affinity, key, start, promptLen)
		return
	}

	chatPayload, err := responsesToChatPayload(payload, model.ID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if stream {
		s.streamResponsesAsChat(w, r, chatPayload, model.ID, affinity, key, start, promptLen)
		return
	}
	s.blockingResponsesAsChat(w, r, chatPayload, model.ID, affinity, key, start, promptLen)
}

// passthroughResponses 透传上游 /responses。
func (s *Server) passthroughResponses(w http.ResponseWriter, r *http.Request, payload map[string]any, publicModel string, stream bool, affinity, key string, start time.Time, promptLen int) {
	ctx, cancel := contextWithTimeout(r, s.requestTimeout())
	defer cancel()

	// 同上：上游缺省 stream 时按 SSE 返回，必须显式下发。
	payload["stream"] = stream
	body, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求序列化失败", "invalid_request_error")
		return
	}
	resp, acct, err := s.openModel(ctx, deepai.PathResponses, body, affinity, s.requestGroup(r))
	if err != nil {
		s.record(key, acct, publicModel, "responses", stream, statusOf(err), start, promptLen, 0, Usage{}, err)
		writeUpstreamErr(w, err)
		return
	}
	defer resp.Body.Close()

	if !stream {
		rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			s.record(key, acct, publicModel, "responses", false, http.StatusBadGateway, start, promptLen, 0, Usage{}, err)
			writeErr(w, http.StatusBadGateway, "读取上游响应失败: "+err.Error(), "upstream_error")
			return
		}
		var out map[string]any
		if err := json.Unmarshal(rawResp, &out); err != nil {
			writeRawJSON(w, http.StatusOK, rawResp)
			return
		}
		rewriteResponsesModel(out, publicModel)
		usage := parseUsage(out["usage"])
		outLen := responsesTextLen(out)
		encoded, _ := json.Marshal(out)
		s.record(key, acct, publicModel, "responses", false, http.StatusOK, start, promptLen, outLen, usage, nil)
		writeRawJSON(w, http.StatusOK, encoded)
		return
	}

	sw := newSSEWriter(w)
	w.WriteHeader(http.StatusOK)
	var (
		usage  Usage
		outLen int
	)
	streamErr := readSSELines(resp.Body, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		if strings.HasPrefix(trimmed, "event:") {
			_, _ = w.Write([]byte(trimmed + "\n"))
			return true
		}
		if !strings.HasPrefix(trimmed, "data:") {
			return true
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" {
			sw.done()
			return false
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			_, _ = w.Write([]byte("data: " + data + "\n\n"))
			sw.flush()
			return true
		}
		rewriteResponsesModel(obj, publicModel)
		if respObj, ok := obj["response"].(map[string]any); ok {
			if u := parseUsage(respObj["usage"]); u.TotalTokens > 0 || u.Cost > 0 {
				usage = u
			}
		}
		outLen += responsesEventTextLen(obj)
		_, _ = w.Write([]byte("data: " + data + "\n\n"))
		sw.flush()
		return true
	})
	if streamErr != nil {
		payload := jsonEvent(map[string]any{
			"type": "error", "message": streamErr.Error(),
		})
		if payload != nil {
			sw.event("error", payload)
		}
	}
	s.record(key, acct, publicModel, "responses", true, statusOf(streamErr), start, promptLen, outLen, usage, streamErr)
}

// blockingResponsesAsChat 用 Chat Completions 合成 Responses 响应。
func (s *Server) blockingResponsesAsChat(w http.ResponseWriter, r *http.Request, chatPayload map[string]any, publicModel, affinity, key string, start time.Time, promptLen int) {
	ctx, cancel := contextWithTimeout(r, s.requestTimeout())
	defer cancel()

	out, acct, err := s.callJSON(ctx, deepai.PathChat, chatPayload, affinity, s.requestGroup(r))
	if err != nil {
		s.record(key, acct, publicModel, "responses", false, statusOf(err), start, promptLen, 0, Usage{}, err)
		writeUpstreamErr(w, err)
		return
	}
	respObj := chatToResponsesObject(out, publicModel)
	usage := parseUsage(out["usage"])
	outLen := responsesTextLen(respObj)

	s.record(key, acct, publicModel, "responses", false, http.StatusOK, start, promptLen, outLen, usage, nil)
	writeJSON(w, http.StatusOK, respObj)
}

// streamResponsesAsChat 用 Chat Completions 流合成 Responses 事件流。
func (s *Server) streamResponsesAsChat(w http.ResponseWriter, r *http.Request, chatPayload map[string]any, publicModel, affinity, key string, start time.Time, promptLen int) {
	ctx, cancel := contextWithTimeout(r, s.requestTimeout())
	defer cancel()

	body, err := json.Marshal(chatPayload)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求序列化失败", "invalid_request_error")
		return
	}
	resp, acct, err := s.openModel(ctx, deepai.PathChat, body, affinity, s.requestGroup(r))
	if err != nil {
		s.record(key, acct, publicModel, "responses", true, statusOf(err), start, promptLen, 0, Usage{}, err)
		writeUpstreamErr(w, err)
		return
	}
	defer resp.Body.Close()

	sw := newSSEWriter(w)
	w.WriteHeader(http.StatusOK)
	gen := newResponsesStreamGen(sw, publicModel)
	gen.created()

	var (
		usage  Usage
		outLen int
	)
	streamErr := readSSELines(resp.Body, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			return true
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" || data == "" {
			return data != "[DONE]"
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return true
		}
		if u := parseUsage(chunk["usage"]); u.TotalTokens > 0 || u.Cost > 0 {
			usage = u
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			return true
		}
		cm, _ := choices[0].(map[string]any)
		if cm == nil {
			return true
		}
		if delta, ok := cm["delta"].(map[string]any); ok {
			if v, ok := delta["reasoning"].(string); ok && v != "" {
				gen.reasoning(v)
				outLen += len([]rune(v))
			}
			if v, ok := delta["content"].(string); ok && v != "" {
				gen.textDelta(v)
				outLen += len([]rune(v))
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, tcv := range tcs {
					tc, ok := tcv.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tc["id"].(string)
					name, args := "", ""
					if fn, ok := tc["function"].(map[string]any); ok {
						name, _ = fn["name"].(string)
						args, _ = fn["arguments"].(string)
					}
					gen.toolCall(intOf(tc["index"]), id, name, args)
				}
			}
		}
		if fr, ok := cm["finish_reason"].(string); ok && fr != "" {
			gen.finishReason = fr
		}
		return true
	})

	if streamErr != nil {
		gen.fail(streamErr)
	}
	gen.complete(usage)
	s.record(key, acct, publicModel, "responses", true, statusOf(streamErr), start, promptLen, outLen, usage, streamErr)
}

// --- Responses → Chat ---

// responsesToChatPayload 把 Responses 请求翻译成 Chat Completions。
func responsesToChatPayload(payload map[string]any, modelID string) (map[string]any, error) {
	out := map[string]any{
		"model":  modelID,
		"stream": boolOf(payload["stream"]),
	}
	if v, ok := payload["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := payload["top_p"]; ok {
		out["top_p"] = v
	}
	if v := intOf(payload["max_output_tokens"]); v > 0 {
		out["max_tokens"] = v
	}
	if v, ok := payload["prompt_cache_key"]; ok {
		out["prompt_cache_key"] = v
	}

	messages := make([]any, 0, 4)
	if instructions, ok := payload["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	switch input := payload["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": input})
	case []any:
		for _, itemv := range input {
			item, ok := itemv.(map[string]any)
			if !ok {
				if s, ok := itemv.(string); ok {
					messages = append(messages, map[string]any{"role": "user", "content": s})
				}
				continue
			}
			switch strOf(item["type"]) {
			case "message", "":
				role := strOf(item["role"])
				if role == "" {
					role = "user"
				}
				messages = append(messages, map[string]any{
					"role": role, "content": responsesInputContent(item["content"]),
				})
			case "function_call":
				callID := firstString(item["call_id"], item["id"])
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id": callID, "type": "function",
						"function": map[string]any{
							"name":      strOf(item["name"]),
							"arguments": strOf(item["arguments"]),
						},
					}},
				})
			case "function_call_output":
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": firstString(item["call_id"], item["id"]),
					"content":      responsesToolOutput(item["output"]),
				})
			case "reasoning":
				// 历史推理内容不再回喂上游。
			}
		}
	}
	if len(messages) == 0 {
		return nil, errNoMessages
	}
	out["messages"] = messages

	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		converted := make([]any, 0, len(tools))
		for _, tv := range tools {
			t, ok := tv.(map[string]any)
			if !ok || strOf(t["type"]) != "function" {
				continue
			}
			fn := map[string]any{"name": strOf(t["name"])}
			if d := strOf(t["description"]); d != "" {
				fn["description"] = d
			}
			if p, ok := t["parameters"]; ok {
				fn["parameters"] = p
			}
			converted = append(converted, map[string]any{"type": "function", "function": fn})
		}
		if len(converted) > 0 {
			out["tools"] = converted
			if tc, ok := payload["tool_choice"]; ok {
				out["tool_choice"] = convertResponsesToolChoice(tc)
			}
		}
	}
	return out, nil
}

// responsesInputContent 转换 Responses 的 content 字段。
func responsesInputContent(v any) any {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := make([]any, 0, len(c))
		for _, pv := range c {
			p, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			switch strOf(p["type"]) {
			case "input_text", "output_text", "text":
				if t := strOf(p["text"]); t != "" {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "input_image":
				if u := strOf(p["image_url"]); u != "" {
					parts = append(parts, map[string]any{
						"type": "image_url", "image_url": map[string]any{"url": u},
					})
				}
			case "refusal":
				if t := strOf(p["refusal"]); t != "" {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return parts
	}
	return ""
}

func responsesToolOutput(v any) string {
	switch o := v.(type) {
	case string:
		return o
	case nil:
		return ""
	default:
		raw, err := json.Marshal(o)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func convertResponsesToolChoice(v any) any {
	switch tc := v.(type) {
	case string:
		return tc
	case map[string]any:
		if strOf(tc["type"]) == "function" && strOf(tc["name"]) != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": strOf(tc["name"])}}
		}
	}
	return nil
}

// --- Chat → Responses ---

// chatToResponsesObject 把 Chat Completions 响应合成为 Responses 对象。
func chatToResponsesObject(chat map[string]any, publicModel string) map[string]any {
	output := make([]any, 0, 3)
	outputText := ""
	reasoningText := ""

	choices, _ := chat["choices"].([]any)
	if len(choices) > 0 {
		if cm, ok := choices[0].(map[string]any); ok {
			if msg, ok := cm["message"].(map[string]any); ok {
				if r := strOf(msg["reasoning"]); r != "" {
					reasoningText = r
					output = append(output, map[string]any{
						"type": "reasoning", "id": "rs_" + deepai.RandomHex(12),
						"summary": []any{map[string]any{"type": "summary_text", "text": r}},
					})
				}
				if t := strOf(msg["content"]); t != "" {
					outputText = t
					output = append(output, map[string]any{
						"type": "message", "id": "msg_" + deepai.RandomHex(12),
						"status": "completed", "role": "assistant",
						"content": []any{map[string]any{"type": "output_text", "text": t, "annotations": []any{}}},
					})
				}
				if tcs, ok := msg["tool_calls"].([]any); ok {
					for _, tcv := range tcs {
						tc, ok := tcv.(map[string]any)
						if !ok {
							continue
						}
						callID := strOf(tc["id"])
						name, args := "", ""
						if fn, ok := tc["function"].(map[string]any); ok {
							name = strOf(fn["name"])
							args = strOf(fn["arguments"])
						}
						output = append(output, map[string]any{
							"type": "function_call", "id": "fc_" + deepai.RandomHex(12),
							"call_id": callID, "name": name, "arguments": args,
							"status": "completed",
						})
					}
				}
			}
		}
	}

	usage := parseUsage(chat["usage"])
	resp := map[string]any{
		"id":                  "resp_" + deepai.RandomHex(12),
		"object":              "response",
		"created_at":          time.Now().Unix(),
		"status":              "completed",
		"model":               publicModel,
		"output":              output,
		"output_text":         outputText,
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
		"usage": map[string]any{
			"input_tokens":          usage.PromptTokens,
			"output_tokens":         usage.CompletionTokens,
			"total_tokens":          usage.TotalTokens,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": usage.ReasoningTokens},
		},
	}
	if reasoningText != "" {
		resp["reasoning"] = map[string]any{"summary": reasoningText}
	}
	return resp
}

// --- Responses 流事件生成器 ---

type responsesStreamGen struct {
	sw            *sseWriter
	model         string
	respID        string
	createdAt     int64
	outputIndex   int
	textItemID    string
	textBuf       strings.Builder
	textOpen      bool
	reasoningOpen bool
	reasoningID   string
	reasoningBuf  strings.Builder
	toolItems     map[int]*responsesToolState
	finishReason  string
	output        []any
}

type responsesToolState struct {
	itemID   string
	callID   string
	name     string
	args     strings.Builder
	index    int
	finished bool
}

func newResponsesStreamGen(sw *sseWriter, model string) *responsesStreamGen {
	return &responsesStreamGen{
		sw:        sw,
		model:     model,
		respID:    "resp_" + deepai.RandomHex(12),
		createdAt: time.Now().Unix(),
		toolItems: map[int]*responsesToolState{},
		output:    []any{},
	}
}

func (g *responsesStreamGen) emit(event string, v map[string]any) {
	v["type"] = event
	if raw := jsonEvent(v); raw != nil {
		g.sw.event(event, raw)
	}
}

func (g *responsesStreamGen) response(status string, usage Usage) map[string]any {
	resp := map[string]any{
		"id": g.respID, "object": "response", "created_at": g.createdAt,
		"status": status, "model": g.model,
		"output":              g.output,
		"output_text":         g.textBuf.String(),
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
	}
	if usage.TotalTokens > 0 {
		resp["usage"] = map[string]any{
			"input_tokens":          usage.PromptTokens,
			"output_tokens":         usage.CompletionTokens,
			"total_tokens":          usage.TotalTokens,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": usage.ReasoningTokens},
		}
	}
	return resp
}

func (g *responsesStreamGen) created() {
	g.emit("response.created", map[string]any{
		"response": g.response("in_progress", Usage{}),
	})
}

func (g *responsesStreamGen) reasoning(delta string) {
	if !g.reasoningOpen {
		g.closeText()
		g.reasoningOpen = true
		g.reasoningID = "rs_" + deepai.RandomHex(12)
		g.emit("response.output_item.added", map[string]any{
			"output_index": g.outputIndex,
			"item": map[string]any{
				"id": g.reasoningID, "type": "reasoning", "status": "in_progress",
				"summary": []any{},
			},
		})
		g.emit("response.reasoning_summary_part.added", map[string]any{
			"item_id": g.reasoningID, "output_index": g.outputIndex, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": ""},
		})
	}
	g.reasoningBuf.WriteString(delta)
	g.emit("response.reasoning_summary_text.delta", map[string]any{
		"item_id": g.reasoningID, "output_index": g.outputIndex, "summary_index": 0,
		"delta": delta,
	})
}

func (g *responsesStreamGen) textDelta(delta string) {
	if !g.textOpen {
		g.closeReasoning()
		g.textOpen = true
		g.textItemID = "msg_" + deepai.RandomHex(12)
		g.emit("response.output_item.added", map[string]any{
			"output_index": g.outputIndex,
			"item": map[string]any{
				"id": g.textItemID, "type": "message", "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		})
		g.emit("response.content_part.added", map[string]any{
			"item_id": g.textItemID, "output_index": g.outputIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
	}
	g.textBuf.WriteString(delta)
	g.emit("response.output_text.delta", map[string]any{
		"item_id": g.textItemID, "output_index": g.outputIndex, "content_index": 0,
		"delta": delta,
	})
}

func (g *responsesStreamGen) toolCall(idx int, id, name, args string) {
	g.closeText()
	state, ok := g.toolItems[idx]
	if !ok {
		state = &responsesToolState{
			itemID: "fc_" + deepai.RandomHex(12),
			callID: firstString(id, "call_"+deepai.RandomHex(8)),
			name:   name,
			index:  g.outputIndex,
		}
		g.toolItems[idx] = state
		g.outputIndex++
		g.emit("response.output_item.added", map[string]any{
			"output_index": state.index,
			"item": map[string]any{
				"id": state.itemID, "type": "function_call", "status": "in_progress",
				"call_id": state.callID, "name": state.name, "arguments": "",
			},
		})
	}
	if name != "" && state.name == "" {
		state.name = name
	}
	if args != "" {
		state.args.WriteString(args)
		g.emit("response.function_call_arguments.delta", map[string]any{
			"item_id": state.itemID, "output_index": state.index, "delta": args,
		})
	}
}

func (g *responsesStreamGen) closeReasoning() {
	if !g.reasoningOpen {
		return
	}
	g.reasoningOpen = false
	text := g.reasoningBuf.String()
	item := map[string]any{
		"id": g.reasoningID, "type": "reasoning", "status": "completed",
		"summary": []any{map[string]any{"type": "summary_text", "text": text}},
	}
	g.output = append(g.output, item)
	g.emit("response.reasoning_summary_text.done", map[string]any{
		"item_id": g.reasoningID, "output_index": g.outputIndex, "summary_index": 0, "text": text,
	})
	g.emit("response.output_item.done", map[string]any{
		"output_index": g.outputIndex, "item": item,
	})
	g.outputIndex++
}

func (g *responsesStreamGen) closeText() {
	if !g.textOpen {
		g.closeReasoning()
		return
	}
	g.textOpen = false
	text := g.textBuf.String()
	item := map[string]any{
		"id": g.textItemID, "type": "message", "status": "completed",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
	g.output = append(g.output, item)
	g.emit("response.output_text.done", map[string]any{
		"item_id": g.textItemID, "output_index": g.outputIndex, "content_index": 0, "text": text,
	})
	g.emit("response.content_part.done", map[string]any{
		"item_id": g.textItemID, "output_index": g.outputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
	})
	g.emit("response.output_item.done", map[string]any{
		"output_index": g.outputIndex, "item": item,
	})
	g.outputIndex++
}

func (g *responsesStreamGen) closeTools() {
	for _, state := range g.toolItems {
		if state.finished {
			continue
		}
		state.finished = true
		args := state.args.String()
		item := map[string]any{
			"id": state.itemID, "type": "function_call", "status": "completed",
			"call_id": state.callID, "name": state.name, "arguments": args,
		}
		g.output = append(g.output, item)
		g.emit("response.function_call_arguments.done", map[string]any{
			"item_id": state.itemID, "output_index": state.index, "arguments": args,
		})
		g.emit("response.output_item.done", map[string]any{
			"output_index": state.index, "item": item,
		})
	}
}

func (g *responsesStreamGen) fail(err error) {
	g.emit("error", map[string]any{"message": err.Error()})
}

func (g *responsesStreamGen) complete(usage Usage) {
	g.closeText()
	g.closeReasoning()
	g.closeTools()
	g.emit("response.completed", map[string]any{
		"response": g.response("completed", usage),
	})
}

// --- 工具函数 ---

var errNoMessages = &clientError{Message: "input 不能为空"}

type clientError struct{ Message string }

func (e *clientError) Error() string { return e.Message }

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// rewriteResponsesModel 把 Responses 对象 / 事件里的 model 改为对外模型名。
func rewriteResponsesModel(obj map[string]any, publicModel string) {
	if publicModel == "" {
		return
	}
	if _, ok := obj["model"]; ok {
		obj["model"] = publicModel
	}
	if resp, ok := obj["response"].(map[string]any); ok {
		rewriteResponsesModel(resp, publicModel)
	}
}

// responsesTextLen 统计 Responses 对象中的输出文本长度。
func responsesTextLen(obj map[string]any) int {
	total := 0
	if s, ok := obj["output_text"].(string); ok {
		total += len([]rune(s))
	}
	output, _ := obj["output"].([]any)
	for _, itemv := range output {
		item, ok := itemv.(map[string]any)
		if !ok {
			continue
		}
		switch strOf(item["type"]) {
		case "message":
			content, _ := item["content"].([]any)
			for _, cv := range content {
				c, ok := cv.(map[string]any)
				if ok {
					total += lenOfString(c["text"])
				}
			}
		case "reasoning":
			summary, _ := item["summary"].([]any)
			for _, sv := range summary {
				sm, ok := sv.(map[string]any)
				if ok {
					total += lenOfString(sm["text"])
				}
			}
		}
	}
	return total
}

// responsesEventTextLen 统计单个流事件的文本增量长度。
func responsesEventTextLen(obj map[string]any) int {
	if strOf(obj["type"]) == "response.output_text.delta" {
		return len([]rune(strOf(obj["delta"])))
	}
	if strOf(obj["type"]) == "response.reasoning_summary_text.delta" {
		return len([]rune(strOf(obj["delta"])))
	}
	return 0
}

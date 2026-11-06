package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/store"
)

// TestMain 固定机器指纹：MachineID 带 sync.Once，必须在任何 Client 构造前设置，
// 否则 Windows 下会真的去起 powershell 读 SMBIOS。
func TestMain(m *testing.M) {
	os.Setenv("DEEPAI_MACHINE_ID", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	os.Exit(m.Run())
}

// upstream 是可由测试驱动的假上游。
type upstream struct {
	srv *httptest.Server

	// mu 保护 requests：checkin / 并发探测会同时打进来。
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Path    string
	Header  http.Header
	Payload map[string]any
	Body    []byte
}

func (u *upstream) last(t *testing.T) recordedRequest {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		t.Fatal("上游未收到任何请求")
	}
	return u.requests[len(u.requests)-1]
}

// newUpstream 启动假上游；handler 负责写响应。
func newUpstream(t *testing.T, handler http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		u.mu.Lock()
		u.requests = append(u.requests, recordedRequest{
			Path: r.URL.Path, Header: r.Header.Clone(), Payload: payload, Body: raw,
		})
		u.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// newTestServer 构造一个指向假上游、已有一个账号和一个密钥的网关。
func newTestServer(t *testing.T, u *upstream) (*Server, string) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.UpsertAccount(&deepai.Account{
		ID: "acc-1", Name: "test", APIKey: "upstream-key", Enabled: true, Status: "ok",
	})
	if err := st.Save(); err != nil {
		t.Fatalf("保存状态: %v", err)
	}
	key := st.AddKey("test", "").Key

	s := New(st)
	if u != nil {
		s.Up.Origin = u.srv.URL
	}
	return s, key
}

// do 发一个请求到网关，返回响应记录器。
func do(t *testing.T, s *Server, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:34567" // 视为本机来源
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// sseEvents 把 SSE 响应体解析为 (event, data) 序列。
func sseEvents(t *testing.T, raw string) []sseEvent {
	t.Helper()
	var out []sseEvent
	var cur sseEvent
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			if cur.Event != "" || cur.Data != "" {
				out = append(out, cur)
				cur = sseEvent{}
			}
		case strings.HasPrefix(line, "event:"):
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			cur.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if cur.Event != "" || cur.Data != "" {
		out = append(out, cur)
	}
	return out
}

type sseEvent struct {
	Event string
	Data  string
}

func (e sseEvent) JSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
		t.Fatalf("事件 %q 的 data 不是 JSON: %v\n%s", e.Event, err, e.Data)
	}
	return m
}

// eventNames 提取事件名序列。
func eventNames(events []sseEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Event
	}
	return out
}

func decodeJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, raw)
	}
	return v
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		t.Fatalf("序列化请求体: %v", err)
	}
	return b.String()
}

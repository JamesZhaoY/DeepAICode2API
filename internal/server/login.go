package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
)

// 浏览器登录（RFC 8252 本机回调）与桌面端完全同款：
//
//	GET  https://api.deepai.org/deepai_code/desktop_auth/?state=…&port=…
//	  → 浏览器完成登录后回调 http://127.0.0.1:<port>/auth-callback?code=…&state=…
//	POST https://api.deepai.org/deepai_code/desktop_auth/exchange/  {"code": …}
//	  → {api_key, username, is_pro}
const loginTTL = 10 * time.Minute

type loginResult struct {
	Creds *deepai.Credentials
	Err   string
}

type loginSession struct {
	ID      string
	State   string
	Port    int
	Created time.Time
	srv     *http.Server

	mu     sync.Mutex
	result *loginResult
}

// setResult 记录回调结果。
func (s *loginSession) setResult(r *loginResult) {
	s.mu.Lock()
	s.result = r
	s.mu.Unlock()
}

// snapshotResult 返回当前结果（nil 表示仍在等待）。
func (s *loginSession) snapshotResult() *loginResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

type loginSessions struct {
	mu sync.Mutex
	m  map[string]*loginSession
}

func newLoginSessions() *loginSessions { return &loginSessions{m: map[string]*loginSession{}} }

func (ls *loginSessions) put(s *loginSession) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for k, v := range ls.m {
		if time.Since(v.Created) > loginTTL {
			delete(ls.m, k)
			go shutdownLoginServer(v.srv)
		}
	}
	ls.m[s.ID] = s
}

func (ls *loginSessions) get(id string) *loginSession {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.m[id]
}

func (ls *loginSessions) remove(id string) *loginSession {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	s := ls.m[id]
	delete(ls.m, id)
	return s
}

func shutdownLoginServer(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// handleLoginStart 启动一次浏览器登录，返回可打开的 URL。
func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}

	id := deepai.RandomHex(16)
	state := deepai.RandomToken(32)
	session := &loginSession{ID: id, State: state, Created: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth-callback", func(cw http.ResponseWriter, cr *http.Request) {
		code := cr.URL.Query().Get("code")
		if cr.Method != http.MethodGet || code == "" || len(code) > 4096 ||
			cr.URL.Query().Get("state") != state || !secureEqual(cr.URL.Query().Get("state"), state) {
			cw.WriteHeader(http.StatusBadRequest)
			_, _ = cw.Write([]byte("Invalid sign-in callback."))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		creds, err := s.Up.ExchangeCode(ctx, code)
		cancel()
		if err != nil {
			s.finishLogin(session, nil, err)
			cw.WriteHeader(http.StatusBadGateway)
			_, _ = cw.Write([]byte("Sign-in failed. Return to the console and try again."))
			return
		}
		s.finishLogin(session, creds, nil)
		cw.Header().Set("Content-Type", "text/html; charset=utf-8")
		cw.Header().Set("Cache-Control", "no-store")
		_, _ = cw.Write([]byte("<h2>Signed in to DeepAI Code</h2><p>You can close this tab and return to the console.</p>"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法启动本机回调: "+err.Error(), "server_error")
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	session.Port = port
	session.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = session.srv.Serve(ln) }()
	s.Login.put(session)

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": id,
		"open_url":   "/login/open?id=" + url.QueryEscape(id),
		"port":       port,
		"expires_in": int(loginTTL.Seconds()),
	})
}

// handleLoginOpen 跳转到上游登录页。
//
// 跳转目标完全由服务端根据会话里的 state/port 重建，并再次校验
// scheme/host/path，绝不使用请求参数拼 URL。
func (s *Server) handleLoginOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	session := s.Login.get(strings.TrimSpace(r.URL.Query().Get("id")))
	if session == nil || session.Port == 0 {
		writeErr(w, http.StatusNotFound, "登录会话不存在或已过期", "invalid_request_error")
		return
	}
	if time.Since(session.Created) > loginTTL {
		s.Login.remove(session.ID)
		shutdownLoginServer(session.srv)
		writeErr(w, http.StatusGone, "登录会话已过期", "invalid_request_error")
		return
	}
	target := s.Up.SignInURL(session.State, session.Port)
	if !isSafeLoginURL(target, s.Up.Origin) {
		writeErr(w, http.StatusInternalServerError, "登录地址校验失败", "server_error")
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

// isSafeLoginURL 校验登录跳转目标是否落在配置的上游源上。
func isSafeLoginURL(raw, origin string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != o.Scheme || !strings.EqualFold(u.Host, o.Host) {
		return false
	}
	return u.Path == "/deepai_code/desktop_auth/" || u.Path == "/deepai_code/desktop_auth"
}

// handleLoginPoll 轮询浏览器登录结果。
func (s *Server) handleLoginPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	if strings.TrimSpace(body.SessionID) == "" {
		writeErr(w, http.StatusBadRequest, "缺少 session_id", "invalid_request_error")
		return
	}

	session := s.Login.get(body.SessionID)
	if session == nil {
		writeErr(w, http.StatusNotFound, "登录会话不存在或已过期", "invalid_request_error")
		return
	}
	if time.Since(session.Created) > loginTTL {
		s.Login.remove(body.SessionID)
		shutdownLoginServer(session.srv)
		writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
		return
	}

	result := session.snapshotResult()
	if result == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	}
	if result.Err != "" || result.Creds == nil {
		s.Login.remove(body.SessionID)
		shutdownLoginServer(session.srv)
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": result.Err})
		return
	}

	acct, err := deepai.AccountFromCredentials(result.Creds, "oauth")
	if err != nil {
		s.Login.remove(body.SessionID)
		shutdownLoginServer(session.srv)
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	isNew := s.Store.UpsertAccount(acct)
	_ = s.Store.Save()
	go s.probeAndSave(acct)

	s.Login.remove(body.SessionID)
	shutdownLoginServer(session.srv)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "new": isNew, "id": acct.ID,
		"username": acct.Username, "is_pro": acct.IsPro,
	})
}

// finishLogin 记录回调结果并唤醒等待方。
func (s *Server) finishLogin(session *loginSession, creds *deepai.Credentials, err error) {
	session.setResult(&loginResult{Creds: creds, Err: errString(err)})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}

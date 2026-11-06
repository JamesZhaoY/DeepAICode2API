package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"deepaicode2api/internal/deepai"
)

// handleTranscriptions 对应 OpenAI /v1/audio/transcriptions。
//
// 上游只接受 audio/webm 与 audio/mp4，字段名为 voiceRecording；
// 这里保留客户端原始字节与 MIME，仅做字段映射。
func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart 解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少 file 字段", "invalid_request_error")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取音频失败: "+err.Error(), "invalid_request_error")
		return
	}
	if len(data) > 8<<20 {
		writeErr(w, http.StatusRequestEntityTooLarge, "音频超过 8 MB", "invalid_request_error")
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/webm"
	}
	// 上游只认真实 MIME 前缀，去掉 codecs 参数。
	if idx := strings.IndexByte(contentType, ';'); idx > 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}

	mode := strings.TrimSpace(r.FormValue("response_format"))
	key := extractKey(r)

	acct := s.nextAccount(r)
	if acct == nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号", "invalid_request_error")
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	text, err := s.clientFor(acct).Transcribe(ctx, acct, header.Filename, contentType, data)
	cancel()
	if err != nil {
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		s.record(key, acct, "whisper-1", "audio", false, statusOf(err), start, len(data), 0, Usage{}, err)
		writeUpstreamErr(w, err)
		return
	}
	s.Store.MarkAccountResult(acct.ID, true, "")
	s.record(key, acct, "whisper-1", "audio", false, http.StatusOK, start, len(data), len(text), Usage{}, nil)

	if mode == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// handleWebSearch 代理上游联网搜索能力。
func (s *Server) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	s.proxyExtension(w, r, deepai.PathWebSearch)
}

// handleFindService 代理上游服务推荐能力。
func (s *Server) handleFindService(w http.ResponseWriter, r *http.Request) {
	s.proxyExtension(w, r, deepai.PathFindService)
}

func (s *Server) proxyExtension(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	body, err := readBodyWithLimit(w, r)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大", "invalid_request_error")
		return
	}
	if !json.Valid(body) {
		writeErr(w, http.StatusBadRequest, "请求体必须是合法 JSON", "invalid_request_error")
		return
	}

	acct := s.nextAccount(r)
	if acct == nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号", "invalid_request_error")
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	var raw []byte
	switch path {
	case deepai.PathWebSearch:
		raw, err = s.clientFor(acct).WebSearch(ctx, acct, body)
	case deepai.PathFindService:
		raw, err = s.clientFor(acct).FindService(ctx, acct, body)
	default:
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		writeUpstreamErr(w, err)
		return
	}
	s.Store.MarkAccountResult(acct.ID, true, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(raw))
}

// handleTheos 返回当前账号的 Theos 配额。
func (s *Server) handleTheos(w http.ResponseWriter, r *http.Request) {
	acct := s.nextAccount(r)
	if acct == nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号", "invalid_request_error")
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	al, err := s.clientFor(acct).Theos(ctx, acct)
	if err != nil {
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		writeUpstreamErr(w, err)
		return
	}
	s.Store.SetAccountAllowance(acct.ID, al, nil)
	_ = s.Store.Save()
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  acct.ID,
		"username": acct.Username,
		"theos":    al,
	})
}

// handleUsage 返回当前账号的订阅 / 用量摘要。
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	acct := s.nextAccount(r)
	if acct == nil {
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号", "invalid_request_error")
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	usage, err := s.clientFor(acct).UsageSummary(ctx, acct)
	if err != nil {
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		writeUpstreamErr(w, err)
		return
	}
	s.Store.MarkAccountResult(acct.ID, true, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  acct.ID,
		"username": acct.Username,
		"usage":    usage,
	})
}

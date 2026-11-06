package deepai

import (
	"bufio"
	"io"
	"strings"
)

// SSEEvent 是一个 Server-Sent Events 事件。
type SSEEvent struct {
	ID    string
	Event string
	Data  string
}

// ReadSSE 按 SSE 规范解析事件流。
//
// 上游会在同一事件里发送多行 data，规范要求以 "\n" 拼接后整体分发。
func ReadSSE(r io.Reader, fn func(SSEEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		id, event string
		data      []string
		hasData   bool
	)
	dispatch := func() error {
		if !hasData && event == "" && id == "" {
			return nil
		}
		ev := SSEEvent{ID: id, Event: event, Data: strings.Join(data, "\n")}
		id, event, data, hasData = "", "", nil, false
		return fn(ev)
	}

	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释 / 心跳
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			event = value
		case "data":
			data = append(data, value)
			hasData = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}

// WriteSSE 写入一条 SSE data 事件。
func WriteSSE(w io.Writer, payload []byte) error {
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n\n"))
	return err
}

// WriteSSEEvent 写入带 event 名的 SSE 事件（Anthropic / Responses 使用）。
func WriteSSEEvent(w io.Writer, event string, payload []byte) error {
	if event != "" {
		if _, err := io.WriteString(w, "event: "+event+"\n"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

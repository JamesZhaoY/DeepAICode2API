package deepai

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// collect 跑一遍 ReadSSE 并收集事件。
func collect(t *testing.T, stream string) []SSEEvent {
	t.Helper()
	var got []SSEEvent
	if err := ReadSSE(strings.NewReader(stream), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("ReadSSE 返回错误: %v", err)
	}
	return got
}

func TestReadSSEBasic(t *testing.T) {
	got := collect(t, "data: {\"a\":1}\n\ndata: {\"b\":2}\n\n")
	if len(got) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(got))
	}
	if got[0].Data != `{"a":1}` || got[1].Data != `{"b":2}` {
		t.Fatalf("data 解析错误: %#v", got)
	}
}

func TestReadSSECRLF(t *testing.T) {
	// Windows 上游（或代理）可能用 CRLF；按规范 CR 不是数据的一部分。
	// 注意：这个保证来自 bufio.ScanLines（它本身就剥 \r\n 中的 \r），
	// ReadSSE 里的 TrimSuffix 只是防御性的二层保险。
	got := collect(t, "event: ping\r\ndata: hello\r\n\r\n")
	if len(got) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(got))
	}
	if got[0].Event != "ping" || got[0].Data != "hello" {
		t.Fatalf("CRLF 解析错误: %#v", got[0])
	}
}

func TestReadSSEMultiLineDataJoinedWithCR(t *testing.T) {
	// 规范：同一事件的多行 data 用 "\n" 拼接。
	got := collect(t, "data: line1\ndata: line2\ndata: line3\n\n")
	if len(got) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(got))
	}
	if got[0].Data != "line1\nline2\nline3" {
		t.Fatalf("多行 data 拼接错误: %q", got[0].Data)
	}
}

func TestReadSSESkipsComments(t *testing.T) {
	got := collect(t, ": keep-alive\n\n: another\ndata: x\n\n")
	if len(got) != 1 {
		t.Fatalf("事件数 = %d，期望 1（注释不应产生事件）", len(got))
	}
	if got[0].Data != "x" {
		t.Fatalf("data = %q，期望 x", got[0].Data)
	}
}

func TestReadSSETrailingEventWithoutBlankLine(t *testing.T) {
	// 流被截断时应把最后一个事件发出去，而不是丢掉。
	got := collect(t, "data: tail")
	if len(got) != 1 || got[0].Data != "tail" {
		t.Fatalf("尾部事件丢失: %#v", got)
	}
}

func TestReadSSEValueWithColonAndSpaces(t *testing.T) {
	// 只切第一个冒号，且只吃掉一个前导空格。
	got := collect(t, "data:  {\"u\":\"http://x/y\"}\n\n")
	if len(got) != 1 {
		t.Fatalf("事件数 = %d", len(got))
	}
	if got[0].Data != ` {"u":"http://x/y"}` {
		t.Fatalf("data = %q", got[0].Data)
	}
}

func TestReadSSEIDAndEvent(t *testing.T) {
	got := collect(t, "id: 7\nevent: content_block_delta\ndata: {}\n\n")
	if len(got) != 1 {
		t.Fatalf("事件数 = %d", len(got))
	}
	if got[0].ID != "7" || got[0].Event != "content_block_delta" {
		t.Fatalf("id/event 解析错误: %#v", got[0])
	}
}

func TestReadSSEEmptyStream(t *testing.T) {
	if got := collect(t, ""); len(got) != 0 {
		t.Fatalf("空流应产生 0 个事件，实际 %d", len(got))
	}
	if got := collect(t, "\n\n\n"); len(got) != 0 {
		t.Fatalf("纯空行应产生 0 个事件，实际 %d", len(got))
	}
}

func TestReadSSECallbackErrorStops(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	err := ReadSSE(strings.NewReader("data: a\n\ndata: b\n\n"), func(SSEEvent) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("回调错误应原样返回，得到 %v", err)
	}
	if calls != 1 {
		t.Fatalf("回调应只被调用一次，实际 %d", calls)
	}
}

func TestSSEWriteReadRoundTrip(t *testing.T) {
	var b strings.Builder
	if err := WriteSSE(&b, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("WriteSSE: %v", err)
	}
	if err := WriteSSEEvent(&b, "message_stop", []byte(`{}`)); err != nil {
		t.Fatalf("WriteSSEEvent: %v", err)
	}

	got := collect(t, b.String())
	if len(got) != 2 {
		t.Fatalf("事件数 = %d，期望 2（输出：%q）", len(got), b.String())
	}
	if got[0].Data != `{"x":1}` {
		t.Fatalf("data = %q", got[0].Data)
	}
	if got[1].Event != "message_stop" || got[1].Data != `{}` {
		t.Fatalf("event 事件解析错误: %#v", got[1])
	}
}

func TestReadSSELargePayload(t *testing.T) {
	// 上游单条事件可能很大（长工具调用参数）；>64KB 的默认 Scanner 缓冲会炸。
	big := strings.Repeat("x", 200*1024)
	var got []SSEEvent
	err := ReadSSE(strings.NewReader("data: "+big+"\n\n"), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("大事件解析失败: %v", err)
	}
	if len(got) != 1 || len(got[0].Data) != len(big) {
		t.Fatalf("大事件被截断: got len=%d want=%d", len(got), len(big))
	}
}

// 确保 io.Reader 接口语义被覆盖：分片读取不应影响解析。
func TestReadSSEChunkedReader(t *testing.T) {
	r := &chunkReader{data: []byte("data: hello\ndata: world\n\n"), chunk: 3}
	var got []SSEEvent
	if err := ReadSSE(r, func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("分片读取失败: %v", err)
	}
	if len(got) != 1 || got[0].Data != "hello\nworld" {
		t.Fatalf("分片解析错误: %#v", got)
	}
}

// chunkReader 每次最多吐出 chunk 字节，模拟网络分片。
type chunkReader struct {
	data  []byte
	chunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(r.data) {
		n = len(r.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

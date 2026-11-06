package register

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 台账写入策略。
//
// 每个账号会写 3 次台账（阶段失败 / 中间结果 / 最终结果），每次都 fsync
// 的话 50 个账号就是 ~150 次磁盘同步，在批量场景里是明显的 IO 放大。
// 改成缓冲写 + 按行数/时间批量 fsync：正常情况下每批 50 个账号只同步 ~5 次，
// 崩溃时最多丢 20 行（约 1 秒的进度），对 append-only 台账是可接受的。
const (
	ledgerSyncEvery = 20
	ledgerSyncAfter = 1 * time.Second
)

// Ledger 是 append-only JSONL 台账（进程内带锁）。
//
// 台账保留全部尝试（含失败），交付物只写成功的；合并规则见 Merge。
//
// 台账文件本身**不含密码**：Password / SessionKey / CSRFToken 会写进同目录的
// 旁路文件（<台账名>-credentials.jsonl，权限 0600），Load 时再按邮箱并回。
// 这样台账可以安全地贴进 issue / 发给别人排查，而补跑仍然能拿到凭据。
type Ledger struct {
	Path string

	mu   sync.Mutex
	main *lineBuf
	sec  *lineBuf
}

// NewLedger 构造台账（不立即创建文件）。
func NewLedger(path string) *Ledger { return &Ledger{Path: path} }

// SecretPath 返回凭据旁路文件路径。
func (l *Ledger) SecretPath() string {
	if l == nil || l.Path == "" {
		return ""
	}
	if p, ok := strings.CutSuffix(l.Path, ".jsonl"); ok {
		return p + "-credentials.jsonl"
	}
	return l.Path + ".credentials.jsonl"
}

// secrets 是旁路文件中的一行。
type secrets struct {
	Email      string `json:"email"`
	Password   string `json:"password,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
	CSRFToken  string `json:"csrf_token,omitempty"`
}

// redactRecord 返回一份去掉敏感字段的副本（不修改入参）。
//
// 入参是内存里的进度记录，出网时（入池 / 日志）仍然需要密码；
// 只有落盘到台账与交付物时才脱敏。
func redactRecord(rec *Record) *Record {
	if rec == nil {
		return nil
	}
	cp := cloneRecord(rec)
	cp.Password = ""
	cp.SessionKey = ""
	cp.CSRFToken = ""
	return cp
}

// Append 追加一条记录（脱敏）并把凭据写入旁路文件。
func (l *Ledger) Append(rec *Record) error {
	if l == nil || l.Path == "" || rec == nil {
		return nil
	}
	rec.Normalize()

	raw, err := json.Marshal(redactRecord(rec))
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.main == nil {
		l.main = &lineBuf{path: l.Path}
	}
	if err := l.main.write(raw); err != nil {
		return err
	}

	// 只有确实带了凭据才写旁路文件，避免生成一堆空行。
	if rec.Password != "" || rec.SessionKey != "" || rec.CSRFToken != "" {
		sec, err := json.Marshal(secrets{
			Email:      rec.Email,
			Password:   rec.Password,
			SessionKey: rec.SessionKey,
			CSRFToken:  rec.CSRFToken,
		})
		if err != nil {
			return err
		}
		if l.sec == nil {
			l.sec = &lineBuf{path: l.SecretPath()}
		}
		if err := l.sec.write(sec); err != nil {
			return err
		}
	}
	return nil
}

// Flush 把缓冲内容落盘并 fsync。
//
// 批量结束、进程退出、以及任何"要读自己刚写的内容"之前都必须调用。
func (l *Ledger) Flush() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushLocked()
}

// Close 落盘并关闭底层文件（进程退出前调用）。
func (l *Ledger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.flushLocked(); err != nil {
		return err
	}
	return l.closeLocked()
}

func (l *Ledger) flushLocked() error {
	if l.main != nil {
		if err := l.main.flush(); err != nil {
			return err
		}
	}
	if l.sec != nil {
		if err := l.sec.flush(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) closeLocked() error {
	if l.main != nil {
		if err := l.main.close(); err != nil {
			return err
		}
		l.main = nil
	}
	if l.sec != nil {
		if err := l.sec.close(); err != nil {
			return err
		}
		l.sec = nil
	}
	return nil
}

// lineBuf 是带批量 fsync 的追加写缓冲。
type lineBuf struct {
	path string

	file  *os.File
	buf   *bufio.Writer
	lines int
	since time.Time
}

// write 追加一行；达到行数或时间阈值时才真正落盘。
func (b *lineBuf) write(raw []byte) error {
	if b.file == nil {
		if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		b.file = f
		b.buf = bufio.NewWriterSize(f, 32*1024)
		b.since = time.Now()
	}
	if _, err := b.buf.Write(raw); err != nil {
		return err
	}
	if err := b.buf.WriteByte('\n'); err != nil {
		return err
	}
	b.lines++
	if b.lines >= ledgerSyncEvery || time.Since(b.since) >= ledgerSyncAfter {
		return b.flush()
	}
	return nil
}

// flush 把缓冲刷到文件并 fsync。
func (b *lineBuf) flush() error {
	if b.file == nil {
		return nil
	}
	if err := b.buf.Flush(); err != nil {
		return err
	}
	if err := b.file.Sync(); err != nil {
		return err
	}
	b.lines = 0
	b.since = time.Now()
	return nil
}

// close 刷盘并关闭。
func (b *lineBuf) close() error {
	if b.file == nil {
		return nil
	}
	if err := b.flush(); err != nil {
		return err
	}
	err := b.file.Close()
	b.file, b.buf = nil, nil
	return err
}

// Load 读取并合并台账（按邮箱并集合并，末次等级更高的记录胜出）。
func (l *Ledger) Load() ([]*Record, error) {
	if l == nil || l.Path == "" {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// 缓冲写：先落盘再读，否则会读到旧的快照。
	if err := l.flushLocked(); err != nil {
		return nil, err
	}
	out, err := readRecords(l.Path)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// 把旁路文件里的凭据并回内存记录（补跑需要密码）。
	secs, err := readSecrets(l.SecretPath())
	if err != nil {
		return nil, err
	}
	if len(secs) > 0 {
		for _, rec := range out {
			s, ok := secs[rec.Email]
			if !ok {
				continue
			}
			if rec.Password == "" {
				rec.Password = s.Password
			}
			if rec.SessionKey == "" {
				rec.SessionKey = s.SessionKey
			}
			if rec.CSRFToken == "" {
				rec.CSRFToken = s.CSRFToken
			}
		}
	}
	return out, nil
}

// readRecords 读取台账主文件的全部行并按邮箱合并。
func readRecords(path string) ([]*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	byKey := map[string]*Record{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		rec.Normalize()
		if rec.Email == "" {
			continue
		}
		if old, ok := byKey[rec.Email]; ok {
			byKey[rec.Email] = Merge(old, &rec)
		} else {
			byKey[rec.Email] = &rec
			order = append(order, rec.Email)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取台账失败: %w", err)
	}
	out := make([]*Record, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// readSecrets 读取凭据旁路文件（同邮箱取最后一条非空值）。
func readSecrets(path string) (map[string]secrets, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	out := map[string]secrets{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var s secrets
		if err := json.Unmarshal([]byte(line), &s); err != nil || s.Email == "" {
			continue
		}
		prev := out[s.Email]
		if s.Password != "" {
			prev.Password = s.Password
		}
		if s.SessionKey != "" {
			prev.SessionKey = s.SessionKey
		}
		if s.CSRFToken != "" {
			prev.CSRFToken = s.CSRFToken
		}
		prev.Email = s.Email
		out[s.Email] = prev
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取台账凭据失败: %w", err)
	}
	return out, nil
}

// Compact 把台账压缩成「每个邮箱一行」的等价形式。
//
// append-only 台账跑久了会积累大量同邮箱的历史行；Compact 用 Load 的合并
// 结果重写主文件，语义不变（Merge 规则幂等），文件体积大幅下降。
// 凭据旁路文件不动。
func (l *Ledger) Compact() error {
	if l == nil || l.Path == "" {
		return nil
	}
	recs, err := l.Load()
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}

	var buf strings.Builder
	for _, rec := range recs {
		raw, err := json.Marshal(redactRecord(rec))
		if err != nil {
			return err
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// rename 之后旧句柄指向被替换掉的 inode，必须先关掉再重写。
	if l.main != nil {
		if err := l.main.close(); err != nil {
			return err
		}
		l.main = nil
	}
	tmp := l.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.Path)
}

// Rows 返回台账行数（用于日志）。
func (l *Ledger) Rows() int {
	recs, err := l.Load()
	if err != nil {
		return 0
	}
	return len(recs)
}

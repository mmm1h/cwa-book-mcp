package files

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
)

type Manager struct {
	root         string
	max          int64
	mu           sync.Mutex
	reservations map[string]int64
	permits      chan struct{}
	buffers      chan []byte
}

func New(root string, max int64) (*Manager, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	m := &Manager{root: abs, max: max, reservations: map[string]int64{}, permits: make(chan struct{}, 2), buffers: make(chan []byte, 2)}
	for i := 0; i < 2; i++ {
		m.buffers <- make([]byte, 64<<10)
	}
	return m, nil
}
func (m *Manager) Reserve(id string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 {
		return domain.E("INVALID_ARGUMENT", "文件大小必须为正数")
	}
	if _, exists := m.reservations[id]; exists {
		return nil
	}
	total := n
	for _, v := range m.reservations {
		total += v
	}
	if total > m.max {
		return domain.Temporary("STORAGE_FULL", "暂存预留已达上限")
	}
	free, e := diskFree(m.root)
	if e != nil {
		return domain.E("STORAGE_FULL", "无法确认暂存磁盘空间")
	}
	if uint64(total)+(64<<20) > free {
		return domain.Temporary("STORAGE_FULL", "磁盘空间不足，需保留 64 MiB 余量")
	}
	m.reservations[id] = n
	return nil
}
func (m *Manager) Release(id string) { m.mu.Lock(); delete(m.reservations, id); m.mu.Unlock() }
func (m *Manager) Acquire(ctx context.Context) error {
	select {
	case m.permits <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) Done()                       { <-m.permits }
func (m *Manager) Path(id, name string) string { return filepath.Join(m.root, id, name) }
func (m *Manager) MakeDir(id string) error {
	if e := domain.CheckID(id); e != nil {
		return e
	}
	return os.MkdirAll(filepath.Join(m.root, id), 0700)
}
func (m *Manager) Cleanup(id string) error {
	if e := domain.CheckID(id); e != nil {
		return e
	}
	m.Release(id)
	m.Release(id + ":verify")
	return os.RemoveAll(filepath.Join(m.root, id))
}
func (m *Manager) RemoveParts(id string) {
	_ = os.Remove(m.Path(id, "input.part"))
	_ = os.Remove(m.Path(id, "verify.part"))
	_ = os.Remove(m.Path(id, "verify"))
}
func (m *Manager) Orphans(keep map[string]bool) error {
	d, e := os.Open(m.root)
	if e != nil {
		return e
	}
	defer d.Close()
	for {
		entries, e := d.ReadDir(64)
		for _, x := range entries {
			if x.IsDir() && !keep[x.Name()] && domain.CheckID(x.Name()) == nil {
				if e := m.Cleanup(x.Name()); e != nil {
					return e
				}
			}
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
	}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if e := c.ctx.Err(); e != nil {
		return 0, e
	}
	return c.r.Read(p)
}
func (m *Manager) Copy(ctx context.Context, dst io.Writer, src io.Reader, max int64, progress func(int64)) (int64, string, error) {
	b := <-m.buffers
	defer func() { m.buffers <- b }()
	h := sha256.New()
	out := io.MultiWriter(dst, h)
	var n int64
	for {
		nr, e := ctxReader{ctx, src}.Read(b)
		if nr > 0 {
			if int64(nr) > max-n {
				return n, "", domain.E("FILE_TOO_LARGE", "传输超过文件上限")
			}
			nw, we := out.Write(b[:nr])
			n += int64(nw)
			if we != nil {
				return n, "", we
			}
			if nw != nr {
				return n, "", io.ErrShortWrite
			}
			if progress != nil {
				progress(n)
			}
		}
		if e == io.EOF {
			return n, hex.EncodeToString(h.Sum(nil)), nil
		}
		if e != nil {
			return n, "", e
		}
	}
}
func (m *Manager) Receive(ctx context.Context, id string, r io.Reader, size, max int64, format, expectedHash string, progress func(int64)) (int64, error) {
	if e := m.MakeDir(id); e != nil {
		return 0, e
	}
	part := m.Path(id, "input.part")
	f, e := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return 0, domain.E("STORAGE_FULL", "无法创建暂存文件")
	}
	published := false
	defer func() {
		f.Close()
		if !published {
			os.Remove(part)
		}
	}()
	n, sum, e := m.Copy(ctx, f, r, max, progress)
	if e != nil {
		return n, e
	}
	if size >= 0 && n != size {
		return n, domain.E("LENGTH_MISMATCH", "文件长度与声明不符")
	}
	if expectedHash != "" && !hmac.Equal([]byte(strings.ToLower(expectedHash)), []byte(sum)) {
		return n, domain.E("CHECKSUM_MISMATCH", "SHA-256 与声明不符")
	}
	if e = f.Sync(); e != nil {
		return n, e
	}
	if e = f.Close(); e != nil {
		return n, e
	}
	if e = Validate(ctx, part, format, max); e != nil {
		return n, e
	}
	if e = os.Rename(part, m.Path(id, "input")); e != nil {
		return n, e
	}
	if e = syncDir(filepath.Dir(part)); e != nil {
		return n, e
	}
	published = true
	return n, nil
}

// Validate checks bounded container structure and streams EPUB CRC validation.
// It is a basic integrity check, not a general PDF parser or DRM converter.
func Validate(ctx context.Context, path, format string, max int64) error {
	f, e := os.Open(path)
	if e != nil {
		return domain.E("CWA_FILE_INVALID", "文件不可读")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() < 16 || st.Size() > max {
		return domain.E("CWA_FILE_INVALID", "文件长度不合理")
	}
	invalid := func() error { return domain.E("CWA_FILE_INVALID", "文件类型或基本完整性检查未通过") }
	if format == "pdf" {
		var head [8]byte
		_, e = f.ReadAt(head[:], 0)
		if e != nil || !bytes.HasPrefix(head[:], []byte("%PDF-1.")) && !bytes.HasPrefix(head[:], []byte("%PDF-2.")) {
			return invalid()
		}
		n := min(st.Size(), 4096)
		tail := make([]byte, n)
		_, e = f.ReadAt(tail, st.Size()-n)
		if e != nil || !bytes.Contains(tail, []byte("%%EOF")) {
			return invalid()
		}
		return ctx.Err()
	}
	if format != "epub" {
		return domain.E("UNSUPPORTED_FORMAT", "首版仅支持 EPUB/PDF")
	}
	n := min(st.Size(), 65557)
	tail := make([]byte, n)
	if _, e = f.ReadAt(tail, st.Size()-n); e != nil {
		return invalid()
	}
	i := bytes.LastIndex(tail, []byte{'P', 'K', 5, 6})
	if i < 0 || len(tail)-i < 22 {
		return invalid()
	}
	count := binary.LittleEndian.Uint16(tail[i+10:])
	cdSize := binary.LittleEndian.Uint32(tail[i+12:])
	cdOffset := int64(binary.LittleEndian.Uint32(tail[i+16:]))
	endOffset := st.Size() - n + int64(i)
	if count == 0 || count > 2048 || cdSize > 2<<20 || i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) != len(tail) ||
		binary.LittleEndian.Uint16(tail[i+4:]) != 0 || binary.LittleEndian.Uint16(tail[i+6:]) != 0 ||
		binary.LittleEndian.Uint16(tail[i+8:]) != count || cdOffset+int64(cdSize) != endOffset {
		return invalid()
	}
	// archive/zip reads until the directory signature ends; it deliberately
	// accepts the entry count modulo 65536 and may ignore a forged directory
	// size. Check the actual bounded directory before it allocates File records.
	position := cdOffset
	for j := 0; j < int(count); j++ {
		var header [46]byte
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if position+int64(len(header)) > endOffset {
			return invalid()
		}
		if _, err := f.ReadAt(header[:], position); err != nil || string(header[:4]) != "PK\x01\x02" {
			return invalid()
		}
		name := int64(binary.LittleEndian.Uint16(header[28:]))
		extra := int64(binary.LittleEndian.Uint16(header[30:]))
		comment := int64(binary.LittleEndian.Uint16(header[32:]))
		if name == 0 || name > 512 {
			return invalid()
		}
		position += int64(len(header)) + name + extra + comment
		if position > endOffset {
			return invalid()
		}
	}
	if position != endOffset {
		return invalid()
	}
	z, e := zip.NewReader(f, st.Size())
	if e != nil || len(z.File) > 2048 {
		return invalid()
	}
	var total uint64
	expansionLimit := uint64(max)
	if expansionLimit > (1<<63-2)/4 {
		expansionLimit = 1<<63 - 2
	} else {
		expansionLimit *= 4
	}
	seen := map[string]bool{}
	haveMime, havePackage := false, false
	var packagePath string
	buf := make([]byte, 64<<10)
	for _, entry := range z.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if seen[entry.Name] || len(entry.Name) > 512 || strings.Contains(entry.Name, "\\") || strings.HasPrefix(entry.Name, "/") || strings.Contains("/"+entry.Name+"/", "/../") {
			return invalid()
		}
		seen[entry.Name] = true
		if entry.UncompressedSize64 > expansionLimit-total {
			return invalid()
		}
		total += entry.UncompressedSize64
		r, e := entry.Open()
		if e != nil {
			return invalid()
		}
		switch entry.Name {
		case "mimetype":
			b, e := io.ReadAll(io.LimitReader(r, 64))
			if e != nil || string(b) != "application/epub+zip" || entry.Method != zip.Store {
				r.Close()
				return invalid()
			}
			haveMime = true
		case "META-INF/container.xml":
			if entry.UncompressedSize64 > 64<<10 {
				r.Close()
				return invalid()
			}
			var container struct {
				Rootfiles []struct {
					Path string `xml:"full-path,attr"`
				} `xml:"rootfiles>rootfile"`
			}
			e = xml.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&container)
			if e != nil || len(container.Rootfiles) == 0 {
				r.Close()
				return invalid()
			}
			packagePath = container.Rootfiles[0].Path
			_, e = io.CopyBuffer(io.Discard, ctxReader{ctx, r}, buf)
		default:
			_, e = io.CopyBuffer(io.Discard, ctxReader{ctx, io.LimitReader(r, int64(expansionLimit)+1)}, buf)
		}
		if closeErr := r.Close(); e != nil || closeErr != nil {
			return invalid()
		}
	}
	for _, entry := range z.File {
		if entry.Name == packagePath {
			if entry.UncompressedSize64 > 1<<20 {
				return invalid()
			}
			r, e := entry.Open()
			if e != nil {
				return invalid()
			}
			var pkg struct {
				XMLName  xml.Name
				Manifest []struct {
					ID   string `xml:"id,attr"`
					Href string `xml:"href,attr"`
				} `xml:"manifest>item"`
				Spine []struct {
					ID string `xml:"idref,attr"`
				} `xml:"spine>itemref"`
			}
			e = xml.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&pkg)
			r.Close()
			if e != nil || pkg.XMLName.Local != "package" || len(pkg.Manifest) == 0 || len(pkg.Spine) == 0 {
				return invalid()
			}
			havePackage = true
		}
	}
	if !haveMime || !havePackage {
		return invalid()
	}
	return nil
}

type Ticket struct {
	Purpose string `json:"purpose"`
	ID      string `json:"id"`
	Format  string `json:"format,omitempty"`
	Expires int64  `json:"expires"`
}

func Sign(key []byte, t Ticket) string {
	b, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func Verify(key []byte, s, purpose string) (Ticket, error) {
	var t Ticket
	if len(s) > 2048 {
		return t, domain.E("TICKET_INVALID", "票据无效")
	}
	b, sig, ok := strings.Cut(s, ".")
	if !ok {
		return t, domain.E("TICKET_INVALID", "票据无效")
	}
	data, e := base64.RawURLEncoding.DecodeString(b)
	if e != nil {
		return t, domain.E("TICKET_INVALID", "票据无效")
	}
	signature, e := base64.RawURLEncoding.DecodeString(sig)
	if e != nil {
		return t, domain.E("TICKET_INVALID", "票据无效")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(data, &t) != nil || t.Purpose != purpose || time.Now().Unix() >= t.Expires || domain.CheckID(t.ID) != nil {
		return t, domain.E("TICKET_INVALID", "票据已过期或无效")
	}
	return t, nil
}

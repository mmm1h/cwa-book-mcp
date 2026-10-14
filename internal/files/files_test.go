package files

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
)

func TestForgedZIPDirectoryRejectedBeforeAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forged.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for i := 0; i < 5000; i++ {
		if _, err = z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("entry-%05d", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	var end [22]byte
	if _, err = f.ReadAt(end[:], st.Size()-22); err != nil {
		t.Fatal(err)
	}
	// Lie about count and size while leaving the real directory reachable.
	binary.LittleEndian.PutUint16(end[8:], 1)
	binary.LittleEndian.PutUint16(end[10:], 1)
	binary.LittleEndian.PutUint32(end[12:], 1)
	if _, err = f.WriteAt(end[:], st.Size()-22); err != nil {
		t.Fatal(err)
	}
	f.Close()
	allocations := testing.AllocsPerRun(2, func() {
		if err := Validate(context.Background(), path, "epub", 4<<20); err == nil {
			t.Fatal("forged directory accepted")
		}
	})
	if allocations > 256 {
		t.Fatalf("forged directory allocated %.0f objects before rejection", allocations)
	}
}

func TestEPUBContainerChecksumIsVerified(t *testing.T) {
	var b bytes.Buffer
	if err := testfixture.EPUB(&b); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	start := bytes.Index(data, []byte("PK\x01\x02"))
	for start >= 0 && start+46 <= len(data) {
		nameLength := int(binary.LittleEndian.Uint16(data[start+28:]))
		if string(data[start+46:start+46+nameLength]) == "META-INF/container.xml" {
			data[start+16] ^= 1 // A valid XML body with a deliberately invalid CRC.
			break
		}
		start += 46 + nameLength + int(binary.LittleEndian.Uint16(data[start+30:])) + int(binary.LittleEndian.Uint16(data[start+32:]))
	}
	path := filepath.Join(t.TempDir(), "bad-crc.epub")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(context.Background(), path, "epub", 1<<20); err == nil {
		t.Fatal("container checksum was ignored")
	}
}

func TestSyntheticFormatsAndInvalidFiles(t *testing.T) {
	for _, format := range []string{"epub", "pdf"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			if format == "epub" {
				testfixture.EPUB(&b)
			} else {
				testfixture.PDF(&b, 1024)
			}
			path := filepath.Join(t.TempDir(), format)
			os.WriteFile(path, b.Bytes(), 0600)
			if e := Validate(context.Background(), path, format, 1<<20); e != nil {
				t.Fatal(e)
			}
			os.WriteFile(path, []byte("<html>login page</html>"), 0600)
			if e := Validate(context.Background(), path, format, 1<<20); e == nil {
				t.Fatal("HTML accepted")
			}
		})
	}
}
func TestReceivePublishesOnlyCompleteFile(t *testing.T) {
	m, e := New(t.TempDir(), 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	id := domain.ID()
	var b bytes.Buffer
	testfixture.PDF(&b, 1024)
	sum := sha256.Sum256(b.Bytes())
	n, e := m.Receive(context.Background(), id, bytes.NewReader(b.Bytes()), 1024, 1<<20, "pdf", hex.EncodeToString(sum[:]), nil)
	if e != nil || n != 1024 {
		t.Fatal(n, e)
	}
	os.Remove(m.Path(id, "input"))
	_, e = m.Receive(context.Background(), id, bytes.NewReader(b.Bytes()[:512]), 1024, 1<<20, "pdf", "", nil)
	if domain.AsError(e).Code != "LENGTH_MISMATCH" {
		t.Fatal(e)
	}
	if _, e = os.Stat(m.Path(id, "input.part")); !os.IsNotExist(e) {
		t.Fatal("part leaked", e)
	}
	_, e = m.Receive(context.Background(), id, bytes.NewReader(b.Bytes()), 1024, 1<<20, "pdf", hex.EncodeToString(make([]byte, 32)), nil)
	if domain.AsError(e).Code != "CHECKSUM_MISMATCH" {
		t.Fatal(e)
	}
}
func TestQuotaAndPermitCancellation(t *testing.T) {
	m, e := New(t.TempDir(), 2048)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Reserve("a", 1024); e != nil {
		t.Fatal(e)
	}
	if e = m.Reserve("b", 2048); domain.AsError(e).Code != "STORAGE_FULL" {
		t.Fatal(e)
	}
	m.Release("a")
	if e = m.Reserve("b", 2048); e != nil {
		t.Fatal(e)
	}
	m.Acquire(context.Background())
	m.Acquire(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e = m.Acquire(ctx); e == nil {
		t.Fatal("unbounded permits")
	}
	m.Done()
	m.Done()
	_, _, e = m.Copy(ctx, io.Discard, bytes.NewReader([]byte("bytes")), 32, nil)
	if e == nil {
		t.Fatal("copy ignored cancel")
	}
}
func TestTicketsBoundPurposeTamperExpiry(t *testing.T) {
	key := []byte("synthetic-32-byte-key-012345678901")
	id := domain.ID()
	s := Sign(key, Ticket{Purpose: "upload", ID: id, Expires: time.Now().Add(time.Minute).Unix()})
	if _, e := Verify(key, s, "upload"); e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct{ s, p string }{{s, "download"}, {s + "a", "upload"}, {Sign(key, Ticket{Purpose: "upload", ID: id, Expires: time.Now().Add(-time.Second).Unix()}), "upload"}} {
		if _, e := Verify(key, test.s, test.p); e == nil {
			t.Fatal("invalid ticket accepted")
		}
	}
}

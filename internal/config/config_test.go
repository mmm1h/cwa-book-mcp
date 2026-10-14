package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsMissingSecretsAndClampsMultipartLimit(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:8080")
	t.Setenv("CWA_BASE_URL", "http://localhost:8083")
	t.Setenv("MCP_TOKEN_FILE", "")
	if _, e := Load(); e == nil {
		t.Fatal("missing secrets accepted")
	}
	dir := t.TempDir()
	for key, name := range map[string]string{"MCP_TOKEN_FILE": "mcp", "TICKET_KEY_FILE": "ticket", "CWA_PASSWORD_FILE": "password"} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte("synthetic-key-32-byte-placeholder-123456"), 0600)
		t.Setenv(key, path)
	}
	t.Setenv("CWA_USERNAME", "synthetic")
	t.Setenv("MAX_FILE_BYTES", "1048576")
	t.Setenv("CWA_MAX_UPLOAD_BYTES", "1048576")
	t.Setenv("ZLIB_DOMAIN", "")
	t.Setenv("ZLIB_MODE", "eapi")
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.MaxFile != 1048576-65536 || c.CWAWrite {
		t.Fatal(c.MaxFile, c.CWAWrite)
	}
	t.Setenv("CWA_EXPECTED_FORMAT", "exe")
	if _, e = Load(); e == nil {
		t.Fatal("unsupported format accepted")
	}
}

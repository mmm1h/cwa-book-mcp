package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsMissingSecretsAndClampsMultipartLimit(t *testing.T) {
	for _, key := range []string{"MCP_TRANSPORT", "MCP_TOKEN", "TICKET_KEY", "CWA_PASSWORD"} {
		t.Setenv(key, "")
	}
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

func TestStdioEnvironmentAndPersistentTicketKey(t *testing.T) {
	for _, key := range []string{"MCP_TOKEN_FILE", "MCP_TOKEN", "TICKET_KEY_FILE", "TICKET_KEY", "CWA_PASSWORD_FILE", "ZLIB_DOMAIN", "CWA_EXPECTED_FORMAT", "CWA_INPUT_FORMATS", "CWA_OUTPUT_FORMATS", "CWA_CONFIRM_WINDOW"} {
		t.Setenv(key, "")
	}
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:8080")
	t.Setenv("CWA_BASE_URL", "http://localhost:8083")
	t.Setenv("CWA_USERNAME", "synthetic")
	t.Setenv("CWA_PASSWORD", "synthetic")
	t.Setenv("STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("MCP_TRANSPORT", "stdio")
	first, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	second, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if first.Transport != "stdio" || len(first.MCPToken) != 0 || len(first.TicketKey) != 64 || !bytes.Equal(first.TicketKey, second.TicketKey) {
		t.Fatal("stdio key did not persist without a MCP token")
	}
	if _, e = LoadForTransport("http"); e == nil {
		t.Fatal("HTTP accepted missing MCP credential")
	}
	if _, e = LoadForTransport("invalid"); e == nil {
		t.Fatal("invalid transport accepted")
	}
	// A damaged key must fail instead of invalidating existing tickets silently.
	if e = os.WriteFile(filepath.Join(first.StateDir, "ticket-key"), []byte("damaged"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(); e == nil {
		t.Fatal("damaged persistent key silently replaced")
	}
}

func TestSecretEnvironmentAndFileAreExclusiveAndBounded(t *testing.T) {
	t.Setenv("CWA_PASSWORD", "synthetic-private-value")
	t.Setenv("CWA_PASSWORD_FILE", "")
	value, e := secret("CWA_PASSWORD_FILE", 1)
	if e != nil || string(value) != "synthetic-private-value" {
		t.Fatal("env secret failed", e)
	}
	path := filepath.Join(t.TempDir(), "password")
	if e = os.WriteFile(path, []byte("synthetic-file-value\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CWA_PASSWORD_FILE", path)
	if _, e = secret("CWA_PASSWORD_FILE", 1); e == nil || strings.Contains(e.Error(), "synthetic-private-value") {
		t.Fatal("ambiguous secret accepted or disclosed", e)
	}
	t.Setenv("CWA_PASSWORD", "")
	if value, e = secret("CWA_PASSWORD_FILE", 1); e != nil || string(value) != "synthetic-file-value" {
		t.Fatal("file secret failed", e)
	}
	if e = os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = secret("CWA_PASSWORD_FILE", 1); e == nil {
		t.Fatal("oversized file secret accepted")
	}
	t.Setenv("CWA_PASSWORD_FILE", "")
	t.Setenv("CWA_PASSWORD", strings.Repeat("x", 4097))
	if _, e = secret("CWA_PASSWORD_FILE", 1); e == nil {
		t.Fatal("oversized env secret accepted")
	}
}

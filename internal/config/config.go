package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
)

type Config struct {
	Listen, PublicURL, StateDir, StagingDir        string
	MCPToken, TicketKey                            []byte
	CWABase, CWAUsername, CWAPassword, CWAExpected string
	CWAWrite                                       bool
	CWAInput, CWAOutput                            []string
	CWAMaxUpload, MaxFile, MaxStaging              int64
	ZlibDomain, ZlibSession, ZlibMode              string
	ZlibFileOrigins                                []string
	TransferTimeout, ConfirmWindow                 time.Duration
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func integer(k string, d int64) (int64, error) {
	v := os.Getenv(k)
	if v == "" {
		return d, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive byte count", k)
	}
	return n, nil
}
func secret(k string, min int) ([]byte, error) {
	path := os.Getenv(k)
	if path == "" {
		return nil, fmt.Errorf("%s is required", k)
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, fmt.Errorf("cannot open %s", k)
	}
	defer f.Close()
	b := make([]byte, 4097)
	n, e := f.Read(b)
	if e != nil && n == 0 {
		return nil, fmt.Errorf("cannot read %s", k)
	}
	b = []byte(strings.TrimSpace(string(b[:n])))
	if len(b) < min || n > 4096 {
		return nil, fmt.Errorf("%s must contain %d..4096 bytes", k, min)
	}
	return b, nil
}
func URL(value string) error {
	u, e := url.Parse(value)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid HTTP base URL")
	}
	return nil
}
func Load() (Config, error) {
	c := Config{Listen: env("LISTEN_ADDR", ":8080"), PublicURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"), StateDir: env("STATE_DIR", "state"), StagingDir: env("STAGING_DIR", "staging"), CWABase: strings.TrimRight(os.Getenv("CWA_BASE_URL"), "/"), CWAUsername: os.Getenv("CWA_USERNAME"), CWAExpected: domain.Format(env("CWA_EXPECTED_FORMAT", "epub")), ZlibDomain: strings.TrimRight(os.Getenv("ZLIB_DOMAIN"), "/"), ZlibSession: os.Getenv("ZLIB_SESSION_FILE"), ZlibMode: env("ZLIB_MODE", "eapi"), TransferTimeout: 30 * time.Minute, ConfirmWindow: 15 * time.Minute}
	for _, v := range []string{c.PublicURL, c.CWABase} {
		if e := URL(v); e != nil {
			return c, fmt.Errorf("PUBLIC_BASE_URL/CWA_BASE_URL: %w", e)
		}
	}
	var e error
	c.MCPToken, e = secret("MCP_TOKEN_FILE", 32)
	if e != nil {
		return c, e
	}
	c.TicketKey, e = secret("TICKET_KEY_FILE", 32)
	if e != nil {
		return c, e
	}
	p, e := secret("CWA_PASSWORD_FILE", 1)
	if e != nil {
		return c, e
	}
	c.CWAPassword = string(p)
	if c.CWAUsername == "" {
		return c, fmt.Errorf("CWA_USERNAME is required")
	}
	c.CWAWrite, e = strconv.ParseBool(env("CWA_WRITE_ENABLED", "false"))
	if e != nil {
		return c, fmt.Errorf("CWA_WRITE_ENABLED must be true or false")
	}
	c.MaxFile, e = integer("MAX_FILE_BYTES", 512<<20)
	if e != nil {
		return c, e
	}
	c.MaxStaging, e = integer("MAX_STAGING_BYTES", 4<<30)
	if e != nil {
		return c, e
	}
	c.CWAMaxUpload, e = integer("CWA_MAX_UPLOAD_BYTES", c.MaxFile+65536)
	if e != nil {
		return c, e
	}
	if c.CWAMaxUpload <= 65536 {
		return c, fmt.Errorf("CWA_MAX_UPLOAD_BYTES must include at least 64 KiB multipart allowance")
	}
	if c.MaxFile > c.CWAMaxUpload-65536 {
		c.MaxFile = c.CWAMaxUpload - 65536
	}
	if c.MaxStaging < c.MaxFile {
		return c, fmt.Errorf("MAX_STAGING_BYTES must be >= MAX_FILE_BYTES")
	}
	for key, dst := range map[string]*[]string{"CWA_INPUT_FORMATS": &c.CWAInput, "CWA_OUTPUT_FORMATS": &c.CWAOutput} {
		for _, v := range strings.Split(env(key, "epub,pdf"), ",") {
			f := domain.Format(v)
			if !domain.Supported(f) {
				return c, fmt.Errorf("%s supports only epub,pdf", key)
			}
			*dst = append(*dst, f)
		}
	}
	if !domain.Supported(c.CWAExpected) || !contains(c.CWAOutput, c.CWAExpected) {
		return c, fmt.Errorf("CWA_EXPECTED_FORMAT must be in CWA_OUTPUT_FORMATS")
	}
	if c.ZlibDomain != "" {
		if e := URL(c.ZlibDomain); e != nil {
			return c, fmt.Errorf("ZLIB_DOMAIN: %w", e)
		}
		c.ZlibFileOrigins = []string{c.ZlibDomain}
		for _, v := range strings.Split(os.Getenv("ZLIB_FILE_ORIGINS"), ",") {
			if v == "" {
				continue
			}
			if e := URL(v); e != nil {
				return c, fmt.Errorf("ZLIB_FILE_ORIGINS: %w", e)
			}
			c.ZlibFileOrigins = append(c.ZlibFileOrigins, v)
		}
	}
	if c.ZlibMode != "eapi" {
		return c, fmt.Errorf("first release supports ZLIB_MODE=eapi only")
	}
	if v := os.Getenv("CWA_CONFIRM_WINDOW"); v != "" {
		c.ConfirmWindow, e = time.ParseDuration(v)
		if e != nil || c.ConfirmWindow <= 0 || c.ConfirmWindow > time.Hour {
			return c, fmt.Errorf("CWA_CONFIRM_WINDOW must be >0 and <=1h")
		}
	}
	return c, nil
}
func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func (c Config) CheckFormat(input, output string) error {
	if !contains(c.CWAInput, input) || !contains(c.CWAOutput, output) {
		return domain.E("UNSUPPORTED_FORMAT", "格式不在操作者声明的 CWA 输入/输出规则中")
	}
	return nil
}

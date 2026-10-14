package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	RetryAfter int    `json:"retry_after_seconds,omitempty"`
}

func (e *Error) Error() string      { return e.Code + ": " + e.Message }
func E(code, message string) *Error { return &Error{Code: code, Message: message} }
func Temporary(code, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: true, RetryAfter: 2}
}
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return E("INTERNAL_ERROR", "服务内部操作失败")
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFC.String(s)), " "))
}
func Language(s string) string {
	s = Normalize(strings.ReplaceAll(s, "_", "-"))
	switch s {
	case "zh", "zh-hans", "zh-hant", "zh-cn", "zh-tw", "zh-hk", "zho", "chi", "chinese", "chinese (simplified)", "chinese (traditional)", "中文", "简体中文", "繁體中文", "汉语", "漢語":
		return "zh"
	case "en", "eng", "english", "英语":
		return "en"
	case "de", "deu", "ger", "german":
		return "de"
	case "fr", "fra", "fre", "french":
		return "fr"
	case "ja", "jpn", "japanese":
		return "ja"
	case "ko", "kor", "korean":
		return "ko"
	case "es", "spa", "spanish":
		return "es"
	case "ru", "rus", "russian":
		return "ru"
	case "", "unknown", "und":
		return "unknown"
	}
	return s
}
func Format(s string) string  { return strings.TrimPrefix(Normalize(s), ".") }
func Supported(s string) bool { return s == "epub" || s == "pdf" }
func HasFormat(b Book, f string) bool {
	for _, v := range b.Formats {
		if Format(v) == f {
			return true
		}
	}
	return false
}

type Book struct {
	CandidateID    string   `json:"candidate_id,omitempty"`
	Origin         string   `json:"origin"`
	ID             string   `json:"source_id"`
	UUID           string   `json:"uuid,omitempty"`
	Title          string   `json:"title"`
	Authors        []string `json:"authors"`
	Language       string   `json:"language"`
	Formats        []string `json:"formats"`
	Size           *int64   `json:"size_bytes"`
	AvailableInCWA bool     `json:"available_in_cwa"`
	ISBN           string   `json:"isbn,omitempty"`
	// Ref is an upstream ID/hash, never a URL or credential.
	Ref string `json:"reference,omitempty"`
}

func CleanBook(b Book) (Book, error) {
	if len(b.Title) > 1024 || len(b.Authors) > 16 || len(b.Formats) > 16 || len(b.ID) > 128 || len(b.UUID) > 128 || len(b.Ref) > 256 || len(b.ISBN) > 64 {
		return b, E("METADATA_TOO_LARGE", "书籍元数据超过上限")
	}
	for _, a := range b.Authors {
		if len(a) > 256 {
			return b, E("METADATA_TOO_LARGE", "作者字段超过上限")
		}
	}
	b.Language = Language(b.Language)
	if len(b.Language) > 64 {
		return b, E("METADATA_TOO_LARGE", "语言字段超过上限")
	}
	for i, f := range b.Formats {
		b.Formats[i] = Format(f)
		if len(f) > 32 {
			return b, E("METADATA_TOO_LARGE", "格式字段超过上限")
		}
	}
	if b.Authors == nil {
		b.Authors = []string{}
	}
	if b.Formats == nil {
		b.Formats = []string{}
	}
	if encoded, _ := json.Marshal(b); len(encoded) > 2048 {
		return b, E("METADATA_TOO_LARGE", "单条候选元数据超过 2 KiB")
	}
	return b, nil
}

type Query struct {
	Query    string   `json:"query"`
	Author   string   `json:"author,omitempty"`
	Language string   `json:"language,omitempty"`
	Formats  []string `json:"formats,omitempty"`
}

func (q Query) Validate() (Query, error) {
	q.Query = Normalize(q.Query)
	q.Author = Normalize(q.Author)
	if q.Query == "" || len(q.Query) > 512 || len(q.Author) > 256 || len(q.Formats) > 2 || len(q.Language) > 64 {
		return q, E("INVALID_ARGUMENT", "请提供有效书名或关键词，查询至多 512 字节")
	}
	if q.Language == "" {
		q.Language = "zh_preferred"
	} else if q.Language != "zh_preferred" && q.Language != "any" {
		q.Language = Language(q.Language)
	}
	for i, f := range q.Formats {
		q.Formats[i] = Format(f)
		if !Supported(q.Formats[i]) {
			return q, E("UNSUPPORTED_FORMAT", "首版仅支持 epub 和 pdf")
		}
	}
	return q, nil
}
func Filter(b Book, q Query) bool {
	if q.Author != "" {
		found := false
		for _, a := range b.Authors {
			if strings.Contains(Normalize(a), q.Author) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if q.Language != "any" && q.Language != "zh_preferred" && Language(b.Language) != q.Language {
		return false
	}
	if len(q.Formats) > 0 {
		found := false
		for _, f := range q.Formats {
			if HasFormat(b, f) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func Match(want, got Book, resolved bool) bool {
	if want.Language != "" && Language(want.Language) != "unknown" && Language(want.Language) != Language(got.Language) {
		return false
	}
	if resolved {
		return true
	}
	if Normalize(want.Title) != Normalize(got.Title) {
		return false
	}
	for _, a := range want.Authors {
		found := false
		for _, v := range got.Authors {
			if Normalize(a) == Normalize(v) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type Page struct {
	Books      []Book
	Next       string
	Incomplete bool
}
type Task struct {
	ID                  string    `json:"task_id"`
	Kind                string    `json:"kind"`
	State               string    `json:"state"`
	Book                Book      `json:"book"`
	RequiredFormat      string    `json:"required_format"`
	Filename            string    `json:"filename"`
	Size                int64     `json:"size_bytes"`
	SHA256              string    `json:"sha256,omitempty"`
	Created             time.Time `json:"created_at"`
	Updated             time.Time `json:"updated_at"`
	Expires             time.Time `json:"expires_at"`
	NextPoll            time.Time `json:"next_poll_at,omitempty"`
	ReconcileDeadline   time.Time `json:"reconcile_deadline,omitempty"`
	ResumeStep          string    `json:"resume_step,omitempty"`
	SubmissionAttempted bool      `json:"submission_attempted"`
	InputComplete       bool      `json:"input_complete"`
	Received            int64     `json:"received_bytes"`
	Attempts            int       `json:"attempts"`
	Polls               int       `json:"polls"`
	CWA                 *Book     `json:"cwa_book,omitempty"`
	Candidates          []Book    `json:"cwa_candidates,omitempty"`
	VerificationBasis   string    `json:"verification_basis,omitempty"`
	VerifiedAt          time.Time `json:"verified_at,omitempty"`
	Error               *Error    `json:"error,omitempty"`
	ExactKey            string    `json:"exact_key,omitempty"`
}

func Terminal(s string) bool      { return s == "ready" || s == "failed" || s == "cancelled" }
func (t Task) MaySubmitted() bool { return t.SubmissionAttempted }
func Filename(name string) error {
	if name == "" || len(name) > 240 || strings.ContainsAny(name, "/\\\x00\r\n:\"<>|?*") || strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name {
		return E("INVALID_ARGUMENT", "文件名含路径或非法字符")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return E("INVALID_ARGUMENT", "文件名含控制字符")
		}
	}
	if !Supported(Format(name[strings.LastIndex(name, ".")+1:])) {
		return E("UNSUPPORTED_FORMAT", "首版仅支持 EPUB/PDF")
	}
	return nil
}
func ContentType(f string) string {
	if f == "epub" {
		return "application/epub+zip"
	}
	if f == "pdf" {
		return "application/pdf"
	}
	return "application/octet-stream"
}
func CheckID(s string) error {
	if len(s) == 0 || len(s) > 128 {
		return E("INVALID_ARGUMENT", "标识符无效")
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return E("INVALID_ARGUMENT", "标识符无效")
		}
	}
	return nil
}
func Status(e *Error) int {
	switch e.Code {
	case "AUTH_REQUIRED", "AUTH_EXPIRED", "TICKET_INVALID":
		return 401
	case "TASK_NOT_FOUND", "CANDIDATE_EXPIRED":
		return 404
	case "BUSY", "STORAGE_FULL", "CWA_UNAVAILABLE", "SOURCE_UNAVAILABLE":
		return 503
	case "FILE_TOO_LARGE", "METADATA_TOO_LARGE":
		return 413
	case "UPLOAD_IN_PROGRESS", "IDEMPOTENCY_CONFLICT":
		return 409
	default:
		return 400
	}
}
func RequireID(s string) error {
	if strings.TrimSpace(s) == "" || len(s) > 128 {
		return E("INVALID_ARGUMENT", fmt.Sprintf("request_id 必填且至多 %d 字节", 128))
	}
	return nil
}

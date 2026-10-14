// bench drives a separate release-built service with original synthetic books.
// Run it on Linux with -docker-image to exercise the 128 MiB cgroup limit.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/diagnostics"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/server"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type sample struct {
	At           time.Time `json:"at"`
	Phase        string    `json:"phase"`
	RSS          uint64    `json:"rss_bytes"`
	HWM          uint64    `json:"hwm_bytes"`
	FDOrHandles  int       `json:"fd_or_handles"`
	StateDBBytes int64     `json:"state_db_bytes"`
	diagnostics.Metrics
	Cgroup map[string]uint64 `json:"cgroup"`
}
type phaseResult struct {
	Name    string  `json:"name"`
	Bytes   int64   `json:"file_bytes"`
	Peak    uint64  `json:"peak_rss_bytes"`
	Elapsed float64 `json:"elapsed_seconds"`
	SHA256  string  `json:"sha256,omitempty"`
}
type report struct {
	OS, Arch, GoVersion, Service, ContainerImage string
	Commit, Platform                             string
	HostCPUs                                     int
	Started, Finished                            time.Time
	IdleSeconds, StableSeconds                   float64
	HistoryTasks, SequentialTasks                int
	BackpressureResponses                        int
	FaultChecks                                  map[string]bool
	Phases                                       []phaseResult
	Samples                                      []sample
	OOMKilled                                    bool
	Gates                                        map[string]bool
	Limitations                                  []string
}
type auth struct{ token string }

var errBusy = errors.New("bounded control backpressure")

func (a auth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		return resp, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var result struct {
		Error domain.Error `json:"error"`
	}
	if len(data) <= 4096 && json.Unmarshal(data, &result) == nil && result.Error.Code == "BUSY" {
		return nil, errBusy
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

type bench struct {
	report                report
	mu                    sync.Mutex
	phase                 string
	pid                   int
	metrics               string
	sess                  *sdk.ClientSession
	base, work, container string
	app                   *exec.Cmd
	binary, token         string
	log                   *os.File
	wait                  chan error
	fixture               *testfixture.CWA
	source                *testfixture.Zlib
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	binary := flag.String("service", "dist/cwa-book-mcp", "release service binary")
	image := flag.String("docker-image", "", "optional local release image (Linux only)")
	work := flag.String("work", ".work/bench", "private, NEW benchmark directory")
	out := flag.String("report", ".work/bench-report.json", "JSON report")
	idle := flag.Duration("idle", 5*time.Minute, "idle warm-up")
	stable := flag.Duration("stable", 30*time.Minute, "stability duration")
	sequential := flag.Int("sequential", 100, "sequential completed jobs")
	history := flag.Int("history", 1000, "retained ready history tasks")
	sizes := flag.String("sizes", "10485760,104857600,1073741824", "comma-separated exact PDF sizes")
	flag.Parse()
	if *image != "" && runtime.GOOS != "linux" {
		return fmt.Errorf("container benchmark requires Linux")
	}
	if *sequential < 0 || *history < 0 || *history > 1000 {
		return fmt.Errorf("invalid task counts")
	}
	abs, e := filepath.Abs(*work)
	if e != nil {
		return e
	}
	if _, e = os.Stat(abs); e == nil {
		return fmt.Errorf("use a new -work directory; never overwrite previous state")
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return e
	}
	b := &bench{work: abs, metrics: filepath.Join(abs, "metrics.json"), phase: "startup", wait: make(chan error, 1)}
	b.report = report{OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), Service: *binary, ContainerImage: *image, Started: time.Now().UTC(), HistoryTasks: *history, FaultChecks: map[string]bool{}, Gates: map[string]bool{}, Limitations: []string{}}
	b.report.HostCPUs = runtime.NumCPU()
	if revision, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		b.report.Commit = strings.TrimSpace(string(revision))
	}
	if runtime.GOOS == "linux" {
		if platform, err := exec.Command("uname", "-sr").Output(); err == nil {
			b.report.Platform = strings.TrimSpace(string(platform))
		}
	}
	defer func() {
		b.report.Finished = time.Now().UTC()
		b.mu.Lock()
		data, _ := json.MarshalIndent(b.report, "", "  ")
		b.mu.Unlock()
		_ = os.MkdirAll(filepath.Dir(*out), 0700)
		_ = os.WriteFile(*out, data, 0600)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer b.stop()
	if e = b.start(ctx, *binary, *image, *history); e != nil {
		return e
	}
	go b.sample(ctx)
	fmt.Println("idle phase started")
	started := time.Now()
	b.setPhase("idle")
	if e = waitContext(ctx, *idle); e != nil {
		return e
	}
	b.report.IdleSeconds = time.Since(started).Seconds()
	b.capture()
	b.setPhase("lifecycle-faults")
	fmt.Println("128 MiB lifecycle faults started")
	if e = b.faults(ctx); e != nil {
		return e
	}
	for _, part := range strings.Split(*sizes, ",") {
		size, e := strconv.ParseInt(part, 10, 64)
		if e != nil || size < 1024 || size > 1<<30 {
			return fmt.Errorf("invalid synthetic size")
		}
		if e = b.mixed(ctx, size); e != nil {
			return e
		}
	}
	b.setPhase("sequential")
	fmt.Println("sequential jobs started")
	if e = b.configureFile(1024); e != nil {
		return e
	}
	for i := 0; i < *sequential; i++ {
		if e = b.one(ctx, false); e != nil {
			return e
		}
		b.report.SequentialTasks++
		if (i+1)%20 == 0 {
			fmt.Println("completed jobs", i+1)
		}
	}
	b.setPhase("stability")
	fmt.Println("stability phase started")
	started = time.Now()
	deadline := started.Add(*stable)
	for time.Now().Before(deadline) {
		var r catalog.Result
		if e = b.call(ctx, "search_books", map[string]any{"query": "合成测试书"}, &r); e != nil {
			return e
		}
		if e = waitContext(ctx, time.Second); e != nil {
			return e
		}
	}
	b.report.StableSeconds = time.Since(started).Seconds()
	b.capture()
	b.evaluate()
	return nil
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (b *bench) start(ctx context.Context, binary, image string, history int) error {
	file := filepath.Join(b.work, "book.pdf")
	f, e := os.Create(file)
	if e != nil {
		return e
	}
	e = testfixture.PDF(f, 1024)
	f.Close()
	if e != nil {
		return e
	}
	b.fixture = testfixture.NewCWA(nil)
	b.fixture.OutputPath = file
	b.fixture.OutputBytes = 1024
	b.source = testfixture.NewZlib(domain.Book{Title: "合成测试书", Language: "zh", Formats: []string{"pdf"}}, nil)
	b.source.DataPath = file
	b.source.DataBytes = 1024
	b.fixture.Add(domain.Book{ID: "1", Title: "客户端下载", Language: "zh", Formats: []string{"pdf"}})
	state := filepath.Join(b.work, "state")
	db, e := store.Open(state)
	if e != nil {
		return e
	}
	for i := 0; i < history; i++ {
		now := time.Now().UTC()
		t := domain.Task{ID: domain.ID(), Kind: "acquire", State: "ready", Book: domain.Book{Title: "合成历史记录", Language: "unknown"}, RequiredFormat: "pdf", Created: now, Updated: now, Expires: now.Add(24 * time.Hour)}
		if _, e = db.Create("seed-"+strconv.Itoa(i), "seed", t, func(t domain.Task) any { return jobs.TaskResult{ID: t.ID, State: t.State} }); e != nil {
			db.Close()
			return e
		}
	}
	if e = db.Close(); e != nil {
		return e
	}
	token, key := domain.ID()+domain.ID(), domain.ID()+domain.ID()
	b.token = token
	for name, content := range map[string]string{"mcp-token": token, "ticket-key": key, "cwa-password": "synthetic", "session.json": `{"cookies":{"remix_userid":"synthetic","remix_userkey":"synthetic"}}`} {
		if e = os.WriteFile(filepath.Join(b.work, name), []byte(content), 0600); e != nil {
			return e
		}
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	address := listener.Addr().String()
	listener.Close()
	b.base = "http://" + address
	env := []string{"LISTEN_ADDR=" + address, "PUBLIC_BASE_URL=" + b.base, "STATE_DIR=" + state, "STAGING_DIR=" + filepath.Join(b.work, "staging"), "MCP_TOKEN_FILE=" + filepath.Join(b.work, "mcp-token"), "TICKET_KEY_FILE=" + filepath.Join(b.work, "ticket-key"), "CWA_BASE_URL=" + b.fixture.Server.URL, "CWA_USERNAME=synthetic", "CWA_PASSWORD_FILE=" + filepath.Join(b.work, "cwa-password"), "CWA_EXPECTED_FORMAT=pdf", "CWA_WRITE_ENABLED=true", "CWA_INPUT_FORMATS=epub,pdf", "CWA_OUTPUT_FORMATS=epub,pdf", "ZLIB_DOMAIN=" + b.source.Server.URL, "ZLIB_SESSION_FILE=" + filepath.Join(b.work, "session.json"), "MAX_FILE_BYTES=1073741824", "MAX_STAGING_BYTES=4294967296", "CWA_MAX_UPLOAD_BYTES=1073807360", "GOMEMLIMIT=64MiB", "CWA_DEBUG_METRICS_FILE=" + b.metrics}
	b.log, e = os.Create(filepath.Join(b.work, "service.log"))
	if e != nil {
		return e
	}
	if image == "" {
		bin, e := filepath.Abs(binary)
		if e != nil {
			return e
		}
		b.binary = bin
		b.app = exec.Command(bin)
		b.app.Env = append(os.Environ(), env...)
	} else {
		b.container = "cwa-book-bench-" + domain.ID()
		args := []string{"run", "--name", b.container, "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--memory", "128m", "--memory-swap", "128m", "--volume", b.work + ":" + b.work}
		for _, v := range env {
			args = append(args, "--env", v)
		}
		args = append(args, image)
		b.app = exec.Command("docker", args...)
	}
	b.app.Stdout = b.log
	b.app.Stderr = b.log
	if e = b.app.Start(); e != nil {
		return e
	}
	go func() { b.wait <- b.app.Wait() }()
	b.pid = b.app.Process.Pid
	ready := time.Now().Add(30 * time.Second)
	for {
		r, e := http.Get(b.base + "/readyz")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		select {
		case e := <-b.wait:
			return fmt.Errorf("service exited: %v; inspect private service.log", e)
		default:
		}
		if time.Now().After(ready) {
			return fmt.Errorf("service did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if image != "" {
		data, e := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", b.container).Output()
		if e != nil {
			return e
		}
		b.pid, e = strconv.Atoi(strings.TrimSpace(string(data)))
		if e != nil {
			return e
		}
	}
	return b.connect(ctx)
}
func (b *bench) connect(ctx context.Context) error {
	c := sdk.NewClient(&sdk.Implementation{Name: "synthetic-benchmark", Version: "1"}, nil)
	var err error
	b.sess, err = c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: b.base + "/mcp", HTTPClient: &http.Client{Transport: auth{b.token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	return err
}
func (b *bench) stop() {
	if b.sess != nil {
		b.sess.Close()
	}
	if b.app != nil && b.app.Process != nil {
		if b.container != "" {
			_ = exec.Command("docker", "stop", "--time", "15", b.container).Run()
			data, _ := exec.Command("docker", "inspect", "--format", "{{.State.OOMKilled}}", b.container).Output()
			b.report.OOMKilled = b.report.OOMKilled || strings.TrimSpace(string(data)) == "true"
			_ = exec.Command("docker", "rm", b.container).Run()
		} else {
			_ = b.app.Process.Kill()
		}
		select {
		case <-b.wait:
		case <-time.After(20 * time.Second):
		}
	}
	if b.log != nil {
		b.log.Close()
	}
	if b.fixture != nil {
		b.fixture.Server.Close()
	}
	if b.source != nil {
		b.source.Server.Close()
	}
}
func (b *bench) setPhase(s string) { b.mu.Lock(); b.phase = s; b.mu.Unlock() }
func (b *bench) capture() {
	b.mu.Lock()
	pid, phase := b.pid, b.phase
	b.mu.Unlock()
	rss, hwm, fd, e := processStats(pid)
	if e != nil {
		return
	}
	var metrics diagnostics.Metrics
	data, e := os.ReadFile(b.metrics)
	if e == nil {
		_ = json.Unmarshal(data, &metrics)
	}
	s := sample{At: time.Now().UTC(), Phase: phase, RSS: rss, HWM: hwm, FDOrHandles: fd, Metrics: metrics, Cgroup: cgroupStats(pid)}
	if st, err := os.Stat(filepath.Join(b.work, "state", "state.db")); err == nil {
		s.StateDBBytes = st.Size()
	}
	b.mu.Lock()
	if len(b.report.Samples) < 8192 {
		b.report.Samples = append(b.report.Samples, s)
	}
	b.mu.Unlock()
}
func (b *bench) sample(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := time.Time{}
	for {
		b.mu.Lock()
		fast := strings.HasPrefix(b.phase, "mixed-")
		b.mu.Unlock()
		if fast || time.Since(last) >= time.Second {
			b.capture()
			last = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (b *bench) call(ctx context.Context, name string, args any, out any) error {
	var r *sdk.CallToolResult
	var e error
	for attempt := 0; ; attempt++ {
		r, e = b.sess.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if !errors.Is(e, errBusy) {
			break
		}
		b.mu.Lock()
		b.report.BackpressureResponses++
		b.mu.Unlock()
		if attempt == 3 {
			return e
		}
		if e = waitContext(ctx, time.Duration(1<<attempt)*100*time.Millisecond); e != nil {
			return e
		}
	}
	if e != nil {
		return e
	}
	if r.IsError {
		return fmt.Errorf("%s returned an error: %v", name, r.Content)
	}
	data, e := json.Marshal(r.StructuredContent)
	if e != nil {
		return e
	}
	return json.Unmarshal(data, out)
}
func (b *bench) configureFile(size int64) error {
	file := filepath.Join(b.work, "book.pdf")
	f, e := os.Create(file)
	if e != nil {
		return e
	}
	e = testfixture.PDF(f, size)
	f.Close()
	if e != nil {
		return e
	}
	b.fixture.OutputBytes = size
	b.source.DataBytes = size
	b.fixture.Mu.Lock()
	keep := []domain.Book{}
	for _, v := range b.fixture.Books {
		if v.ID == "1" {
			keep = append(keep, v)
		}
	}
	b.fixture.Books = keep
	b.fixture.Mu.Unlock()
	return nil
}
func (b *bench) one(ctx context.Context, concurrent bool) error {
	var r catalog.Result
	if e := b.call(ctx, "search_books", map[string]any{"query": "合成测试书"}, &r); e != nil {
		return e
	}
	if len(r.Candidates) == 0 {
		return fmt.Errorf("synthetic candidate missing")
	}
	var task jobs.TaskResult
	if e := b.call(ctx, "acquire_book", map[string]any{"action": "start", "request_id": domain.ID(), "candidate_id": r.Candidates[0].CandidateID}, &task); e != nil {
		return e
	}
	other := make(chan error, 1)
	if concurrent {
		go func() {
			var library catalog.Result
			if e := b.call(ctx, "search_books", map[string]any{"query": "客户端下载"}, &library); e != nil {
				other <- e
				return
			}
			var link server.DownloadResult
			if e := b.call(ctx, "get_download_url", map[string]any{"cwa_book_id": "1", "format": "pdf"}, &link); e != nil {
				other <- e
				return
			}
			other <- b.download(ctx, link.URL, true)
		}()
	}
	var wg sync.WaitGroup
	var burstErr error
	var burstMu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out any
			e := b.call(ctx, "get_task", map[string]any{"task_id": task.ID}, &out)
			if e != nil {
				burstMu.Lock()
				burstErr = e
				burstMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if burstErr != nil {
		return burstErr
	}
	deadline := time.Now().Add(5 * time.Minute)
	var final domain.Task
	for {
		if e := b.call(ctx, "get_task", map[string]any{"task_id": task.ID}, &final); e != nil {
			return e
		}
		if final.State == "ready" {
			break
		}
		if final.State == "failed" || final.State == "blocked" || final.State == "needs_review" || time.Now().After(deadline) {
			return fmt.Errorf("synthetic task did not complete: state=%s error=%v", final.State, final.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if concurrent {
		if e := <-other; e != nil {
			return e
		}
	}
	var link server.DownloadResult
	if e := b.call(ctx, "get_download_url", map[string]any{"cwa_book_id": final.CWA.ID, "format": "pdf", "task_id": task.ID}, &link); e != nil {
		return e
	}
	return b.download(ctx, link.URL, false)
}
func (b *bench) download(ctx context.Context, address string, slow bool) error {
	r, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return e
	}
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	var n int64
	for {
		count, e := resp.Body.Read(buffer)
		if count > 0 {
			h.Write(buffer[:count])
			n += int64(count)
			if slow && n < 512<<10 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	expected := sha256.New()
	f, e := os.Open(filepath.Join(b.work, "book.pdf"))
	if e != nil {
		return e
	}
	_, e = io.CopyBuffer(expected, f, buffer)
	f.Close()
	if e != nil {
		return e
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(expected.Sum(nil))) {
		return fmt.Errorf("download hash mismatch")
	}
	return nil
}
func (b *bench) mixed(ctx context.Context, size int64) error {
	if e := b.configureFile(size); e != nil {
		return e
	}
	name := "mixed-" + strconv.FormatInt(size, 10)
	b.setPhase(name)
	fmt.Println("mixed file bytes", size)
	started := time.Now()
	if e := b.one(ctx, true); e != nil {
		return e
	}
	b.capture()
	peak := uint64(0)
	b.mu.Lock()
	for _, s := range b.report.Samples {
		if s.Phase == name {
			peak = max(peak, s.RSS)
		}
	}
	b.mu.Unlock()
	h := sha256.New()
	f, e := os.Open(filepath.Join(b.work, "book.pdf"))
	if e != nil {
		return e
	}
	_, e = io.CopyBuffer(h, f, make([]byte, 64<<10))
	f.Close()
	if e != nil {
		return e
	}
	b.report.Phases = append(b.report.Phases, phaseResult{name, size, peak, time.Since(started).Seconds(), hex.EncodeToString(h.Sum(nil))})
	return nil
}
func (b *bench) evaluate() {
	b.mu.Lock()
	defer b.mu.Unlock()
	var idle, peak uint64
	var first, last *sample
	containerSeen := false
	containerOK := true
	for i := range b.report.Samples {
		s := &b.report.Samples[i]
		if s.Phase == "idle" {
			idle = max(idle, s.RSS)
		}
		peak = max(peak, s.RSS, s.HWM)
		if s.Phase == "stability" {
			if first == nil {
				first = s
			}
			last = s
		}
		if limit := s.Cgroup["memory.max"]; limit > 0 {
			containerSeen = true
			if limit != 128<<20 || s.Cgroup["memory.current"] > limit {
				containerOK = false
			}
		}
	}
	b.report.Gates["linux_amd64_environment"] = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"
	b.report.Gates["idle_5_minutes_rss_32_mib"] = b.report.IdleSeconds >= 300 && idle <= 32<<20
	b.report.Gates["peak_rss_96_mib"] = peak <= 96<<20
	b.report.Gates["cgroup_128_mib"] = b.container != "" && containerSeen && containerOK && !b.report.OOMKilled
	b.report.Gates["stability_30_minutes"] = b.report.StableSeconds >= 1800 && first != nil && last != nil && last.Goroutines <= first.Goroutines+4 && last.FDOrHandles <= first.FDOrHandles+4
	var small, large uint64
	for _, p := range b.report.Phases {
		if p.Bytes == 10<<20 {
			small = p.Peak
		}
		if p.Bytes == 1<<30 {
			large = p.Peak
		}
	}
	b.report.Gates["file_size_rss_delta_16_mib"] = small > 0 && large > 0 && (large <= small || large-small <= 16<<20)
	b.report.Gates["100_sequential_jobs"] = b.report.SequentialTasks >= 100
	b.report.Gates["1000_history_tasks"] = b.report.HistoryTasks == 1000
	b.report.Gates["lifecycle_faults"] = len(b.report.FaultChecks) == 5
	for _, pass := range b.report.FaultChecks {
		b.report.Gates["lifecycle_faults"] = b.report.Gates["lifecycle_faults"] && pass
	}
	if runtime.GOOS != "linux" {
		b.report.Limitations = append(b.report.Limitations, "Windows WorkingSet/PeakWorkingSet and handle count are not Linux RSS/cgroup/FD acceptance evidence")
	}
	if b.container == "" {
		b.report.Limitations = append(b.report.Limitations, "128 MiB container load was not exercised")
	}
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/cwa"
	"github.com/mmm1h/cwa-book-mcp/internal/diagnostics"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/mcp"
	"github.com/mmm1h/cwa-book-mcp/internal/server"
	zlib "github.com/mmm1h/cwa-book-mcp/internal/source/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	if e := run(os.Args[1:]); e != nil && !errors.Is(e, flag.ErrHelp) {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("cwa-book-mcp", flag.ContinueOnError)
	defaultTransport := os.Getenv("MCP_TRANSPORT")
	if defaultTransport == "" {
		defaultTransport = "http"
	}
	transport := flags.String("transport", defaultTransport, "MCP transport: stdio (MCPHub command) or http")
	showVersion := flags.Bool("version", false, "print version and exit")
	healthcheck := flags.Bool("healthcheck", false, "check the local HTTP file listener")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *healthcheck {
		address := os.Getenv("LISTEN_ADDR")
		if address == "" {
			address = ":8080"
		}
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return fmt.Errorf("invalid LISTEN_ADDR")
		}
		client := http.Client{Timeout: 4 * time.Second}
		response, e := client.Get("http://127.0.0.1:" + port + "/healthz")
		if e != nil {
			return fmt.Errorf("healthcheck failed")
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("healthcheck failed")
		}
		return nil
	}
	c, e := config.LoadForTransport(*transport)
	if e != nil {
		return e
	}
	db, e := store.Open(c.StateDir)
	if e != nil {
		return e
	}
	defer db.Close()
	f, e := files.New(c.StagingDir, c.MaxStaging)
	if e != nil {
		return e
	}
	lib := cwa.New(c.CWABase, c.CWAUsername, c.CWAPassword)
	defer lib.Close()
	source := zlib.New(c.ZlibDomain, c.ZlibSession, c.ZlibMode, c.ZlibFileOrigins)
	defer source.Close()
	service := jobs.New(c, db, f, lib, source)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if path := os.Getenv("CWA_DEBUG_METRICS_FILE"); path != "" {
		go diagnostics.Monitor(ctx, path)
	}
	if e = service.Recover(ctx); e != nil {
		return e
	}
	app := server.New(service)
	handler := app.FileHandler()
	if c.Transport == "http" {
		handler = app.Handler(version)
	}
	httpServer := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	// Bind before answering initialize, so MCPHub cannot see a connected service
	// whose file listener immediately fails (for example because the port is busy).
	listener, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return fmt.Errorf("cannot listen on LISTEN_ADDR")
	}
	defer listener.Close()
	worker := make(chan error, 1)
	go func() { worker <- service.Run(ctx) }()
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	var stdio chan error
	if c.Transport == "stdio" {
		stdio = make(chan error, 1)
		go func() { stdio <- mcp.NewServer(app, version).Run(ctx, &sdk.StdioTransport{MaxLineLength: 256 << 10}) }()
	}
	slog.Info("service started", "version", version, "transport", c.Transport, "cwa_write_enabled", c.CWAWrite)
	workerDone := false
	stdioDone := false
	select {
	case <-ctx.Done():
	case e = <-worker:
		workerDone = true
	case e = <-serving:
	case e = <-stdio:
		stdioDone = true
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		_ = httpServer.Close()
	}
	if !workerDone {
		select {
		case workerErr := <-worker:
			if e == nil {
				e = workerErr
			}
		case <-shutdown.Done():
			return fmt.Errorf("worker shutdown deadline exceeded")
		}
	}
	if stdio != nil && !stdioDone {
		select {
		case stdioErr := <-stdio:
			if e == nil {
				e = stdioErr
			}
		case <-shutdown.Done():
			return fmt.Errorf("stdio shutdown deadline exceeded")
		}
	}
	if errors.Is(e, http.ErrServerClosed) || errors.Is(e, context.Canceled) {
		return nil
	}
	return e
}

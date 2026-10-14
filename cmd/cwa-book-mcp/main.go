package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/cwa"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/server"
	zlib "github.com/mmm1h/cwa-book-mcp/internal/source/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if e := run(); e != nil {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	c, e := config.Load()
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
	if e = service.Recover(ctx); e != nil {
		return e
	}
	app := server.New(service)
	httpServer := &http.Server{Addr: c.Listen, Handler: app.Handler(version), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	worker := make(chan error, 1)
	go func() { worker <- service.Run(ctx) }()
	serving := make(chan error, 1)
	go func() { serving <- httpServer.ListenAndServe() }()
	slog.Info("service started", "version", version, "cwa_write_enabled", c.CWAWrite)
	workerDone := false
	select {
	case <-ctx.Done():
	case e = <-worker:
		workerDone = true
		stop()
	case e = <-serving:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		_ = httpServer.Close()
	}
	if workerDone {
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	}
	select {
	case workerErr := <-worker:
		if e == nil {
			e = workerErr
		}
	case <-shutdown.Done():
		return fmt.Errorf("worker shutdown deadline exceeded")
	}
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}

// Package diagnostics writes explicit development metrics to a private local
// file. It exposes no HTTP or pprof endpoint and is disabled by default.
package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"time"
)

type Metrics struct {
	HeapAlloc  uint64    `json:"heap_alloc"`
	HeapSys    uint64    `json:"heap_sys"`
	Goroutines int       `json:"goroutines"`
	Timestamp  time.Time `json:"timestamp"`
}

func Monitor(ctx context.Context, path string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		data, _ := json.Marshal(Metrics{m.HeapAlloc, m.HeapSys, runtime.NumGoroutine(), time.Now().UTC()})
		_ = os.WriteFile(path+".tmp", data, 0600)
		_ = os.Rename(path+".tmp", path)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackpressureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		busy       bool
	}{
		{"busy", `{"error":{"code":"BUSY"}}`, 503, true},
		{"upstream failure", `{"error":{"code":"CWA_UNAVAILABLE"}}`, 503, false},
		{"proxy failure", "unavailable", 503, false},
		{"authentication", `{"error":{"code":"AUTH_EXPIRED"}}`, 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("missing bearer")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			resp, err := (auth{"synthetic"}).RoundTrip(req)
			if errors.Is(err, errBusy) != tc.busy {
				t.Fatal(err)
			}
			if !tc.busy {
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, _ := io.ReadAll(resp.Body)
				if string(data) != tc.body || resp.StatusCode != tc.status {
					t.Fatal("non-BUSY failure was changed")
				}
			}
		})
	}
}

func TestContainerGateUsesHardLimitAndOOM(t *testing.T) {
	for _, tc := range []struct {
		name           string
		limit, current uint64
		oom, killed    uint64
		dockerOOM      bool
		pass           bool
	}{
		{"within limit", 128 << 20, 100 << 20, 0, 0, false, true},
		{"temporary charge overshoot", 128 << 20, 128<<20 + 65536, 0, 0, false, true},
		{"raised limit", 256 << 20, 100 << 20, 0, 0, false, false},
		{"missing limit", 0, 100 << 20, 0, 0, false, false},
		{"OOM allocation failure", 128 << 20, 100 << 20, 1, 0, false, false},
		{"OOM kill", 128 << 20, 100 << 20, 0, 1, false, false},
		{"Docker OOM", 128 << 20, 100 << 20, 0, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := bench{container: "synthetic", report: report{
				OOMKilled: tc.dockerOOM, Gates: map[string]bool{},
				Samples: []sample{{Cgroup: map[string]uint64{
					"memory.max": tc.limit, "memory.current": tc.current,
					"memory.events.oom": tc.oom, "memory.events.oom_kill": tc.killed,
					"memory.events.max": 100,
				}}},
			}}
			b.evaluate()
			if got := b.report.Gates["cgroup_128_mib"]; got != tc.pass {
				t.Fatalf("cgroup gate = %v, want %v", got, tc.pass)
			}
		})
	}
}

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

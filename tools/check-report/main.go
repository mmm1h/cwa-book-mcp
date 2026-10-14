package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: check-report report.json")
	}
	b, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	var r struct {
		OS, Arch  string
		OOMKilled bool
		Gates     map[string]bool
	}
	if e = json.Unmarshal(b, &r); e != nil {
		panic(e)
	}
	if r.OS != "linux" || r.OOMKilled {
		panic("Linux load did not complete without OOM")
	}
	failed := false
	for _, key := range []string{"idle_5_minutes_rss_32_mib", "peak_rss_96_mib", "cgroup_128_mib", "stability_30_minutes", "file_size_rss_delta_16_mib", "100_sequential_jobs", "1000_history_tasks"} {
		pass := r.Gates[key]
		fmt.Printf("%s=%v\n", key, pass)
		if !pass {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

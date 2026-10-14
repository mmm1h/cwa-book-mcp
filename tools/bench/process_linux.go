//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processStats(pid int) (uint64, uint64, int, error) {
	f, e := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if e != nil {
		return 0, 0, 0, e
	}
	defer f.Close()
	var rss, hwm uint64
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			rss = n * 1024
		case "VmHWM:":
			hwm = n * 1024
		}
	}
	fd, e := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
	return rss, hwm, len(fd), e
}
func cgroupStats(pid int) map[string]uint64 {
	out := map[string]uint64{}
	data, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if e != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		dir := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
		for _, name := range []string{"memory.current", "memory.peak", "memory.max"} {
			b, e := os.ReadFile(filepath.Join(dir, name))
			if e == nil {
				out[name], _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			}
		}
		b, e := os.ReadFile(filepath.Join(dir, "memory.stat"))
		if e == nil {
			fields := strings.Fields(string(b))
			for i := 0; i+1 < len(fields); i += 2 {
				if fields[i] == "anon" || fields[i] == "file" {
					out[fields[i]], _ = strconv.ParseUint(fields[i+1], 10, 64)
				}
			}
		}
	}
	return out
}

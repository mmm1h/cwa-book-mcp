// package builds reproducible Linux binaries and archives with their licenses.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	version := flag.String("version", "dev", "version stamped into binary and archive")
	out := flag.String("out", "dist", "output directory")
	flag.Parse()
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`).MatchString(*version) {
		return fmt.Errorf("invalid version")
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	goexe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goexe += ".exe"
	}
	checksums := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		binary := filepath.Join(*out, "cwa-book-mcp-linux-"+arch)
		build := exec.Command(goexe, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -X main.version="+*version, "-o", binary, "./cmd/cwa-book-mcp")
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			return err
		}
		archive := filepath.Join(*out, "cwa-book-mcp-"+*version+"-linux-"+arch+".tar.gz")
		if err := pack(archive, binary); err != nil {
			return err
		}
		for _, path := range []string{binary, archive} {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			checksums[filepath.Base(path)] = hex.EncodeToString(h.Sum(nil))
		}
		fmt.Println(filepath.Base(archive))
	}
	manifest := struct {
		Version, GoVersion string
		SHA256             map[string]string
	}{*version, runtime.Version(), checksums}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "checksums-"+*version+".json"), append(data, '\n'), 0644)
}

func pack(path, binary string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	g := gzip.NewWriter(f)
	t := tar.NewWriter(g)
	add := func(path, name string, mode int64) error {
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		st, err := input.Stat()
		if err != nil {
			return err
		}
		if err := t.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: st.Size(), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = io.Copy(t, input)
		return err
	}
	if err = add(binary, "cwa-book-mcp", 0755); err != nil {
		return err
	}
	if err = add("LICENSE", "LICENSE", 0644); err != nil {
		return err
	}
	if err = filepath.WalkDir("third_party/licenses", func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || filepath.Ext(path) != ".txt" {
			return nil
		}
		return add(path, "licenses/"+filepath.Base(path), 0644)
	}); err != nil {
		return err
	}
	if err = t.Close(); err != nil {
		return err
	}
	if err = g.Close(); err != nil {
		return err
	}
	return f.Close()
}

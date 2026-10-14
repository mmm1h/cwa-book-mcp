// notices copies the exact licenses of runtime modules selected by go list.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Replace            *module
}

func main() {
	out := flag.String("out", "third_party/licenses", "license directory")
	flag.Parse()
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/cwa-book-mcp")
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		panic(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		panic(e)
	}
	modules := map[string]module{}
	d := json.NewDecoder(pipe)
	for {
		var p struct{ Module *module }
		e = d.Decode(&p)
		if e == io.EOF {
			break
		}
		if e != nil {
			panic(e)
		}
		if p.Module != nil && p.Module.Path != "github.com/mmm1h/cwa-book-mcp" {
			modules[p.Module.Path] = *p.Module
		}
	}
	if e = cmd.Wait(); e != nil {
		panic(e)
	}
	if e = os.MkdirAll(*out, 0755); e != nil {
		panic(e)
	}
	keys := make([]string, 0, len(modules))
	for k := range modules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var index strings.Builder
	index.WriteString("# 第三方运行依赖许可证\n\n由 `go run ./tools/notices` 按实际 runtime dependency tree 生成。\n\n| 模块 | 固定版本 | 许可证原文 |\n| --- | --- | --- |\n")
	for _, key := range keys {
		m := modules[key]
		dir := m.Dir
		if m.Replace != nil {
			dir = m.Replace.Dir
		}
		name := strings.ReplaceAll(m.Path, "/", "_") + ".txt"
		var content []byte
		for _, candidate := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "LICENCE"} {
			b, e := os.ReadFile(filepath.Join(dir, candidate))
			if e == nil {
				content = b
				break
			}
		}
		if len(content) == 0 {
			panic("license missing for " + key)
		}
		if e = os.WriteFile(filepath.Join(*out, name), content, 0644); e != nil {
			panic(e)
		}
		fmt.Fprintf(&index, "| %s | %s | [%s](%s) |\n", m.Path, m.Version, name, name)
	}
	goLicense, e := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if e != nil {
		panic(e)
	}
	os.WriteFile(filepath.Join(*out, "Go-LICENSE.txt"), goLicense, 0644)
	index.WriteString("\nGo 标准库与运行时：[BSD 许可证](Go-LICENSE.txt)。\n书源本地补丁：[说明](../zlib/PATCHES.md)，原始许可证仍完整保留。\n")
	if e = os.WriteFile(filepath.Join(*out, "README.md"), []byte(index.String()), 0644); e != nil {
		panic(e)
	}
}

// Run from the application module root:
//
//	go run ./patches/verify-official.go [/path/to/built/application]
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	core         = "github.com/DataDog/dd-trace-go/v2"
	saramaModule = "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2"
	patchSHA256  = "d7d7be709a97dec05ff67640aac7465520b15f4854788671ba70c24b95a9b9f5"
)

type module struct {
	Path, Version, Dir string
	Replace            *module
}

// Go reports the selected requirement separately from its replacement. Check
// the implementation actually compiled, not only the nominal selected version.
func effectiveModule(m module) (string, string) {
	if m.Replace != nil {
		return m.Replace.Path, m.Replace.Version
	}
	return m.Path, m.Version
}

func validatedSarama(m module) bool {
	path, version := effectiveModule(m)
	return m.Path == "github.com/Shopify/sarama" && path == m.Path && version == "v1.23.0"
}

// Add only the target integration after pin; do not overwrite the customer's
// existing integration imports or rules. This also handles pin pruning its
// aggregate all/v2 import when upstream package-relative replacements fail.
func enableSaramaToolFile(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = []byte("//go:build tools\n\npackage tools\n\nimport _ \"github.com/DataDog/orchestrion\" // integration\n")
	} else if err != nil {
		return err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return err
	}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return err
		}
		if value == saramaModule {
			if spec.Name == nil || spec.Name.Name != "_" {
				return fmt.Errorf("target integration must be a blank import")
			}
			return nil
		}
	}
	at := fset.Position(file.Name.End()).Offset
	updated := append([]byte{}, data[:at]...)
	updated = append(updated, []byte("\n\nimport _ "+strconv.Quote(saramaModule)+" // integration\n")...)
	updated = append(updated, data[at:]...)
	updated, err = format.Source(updated)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0o644)
}

func hasExplicitSaramaIntegration(path string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return false
	}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err == nil && value == saramaModule && spec.Name != nil && spec.Name.Name == "_" {
			return true
		}
	}
	return false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "PATCH_CHECK_FAILED: "+format+"\n", args...)
	os.Exit(1)
}

func forbidden(path string) bool {
	return path == "github.com/GuanceCloud/orchestrion" ||
		strings.HasPrefix(path, "github.com/GuanceCloud/orchestrion/") ||
		path == "github.com/GuanceCloud/dd-trace-go" ||
		strings.HasPrefix(path, "github.com/GuanceCloud/dd-trace-go/")
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--enable-sarama" {
		if err := enableSaramaToolFile("orchestrion.tool.go"); err != nil {
			fail("cannot enable official Shopify integration: %v", err)
		}
		fmt.Println("PATCH_CONFIG_OK explicit-official-Shopify-integration=enabled")
		return
	}
	if len(os.Args) > 2 {
		fail("usage: verify-official.go [--enable-sarama | application-binary]")
	}
	out, err := exec.Command("go", "list", "-m", "-json", "all").Output()
	if err != nil {
		fail("go list -m failed; resolve module errors before building")
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	mods := make(map[string]module)
	for {
		var m module
		if err := decoder.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			fail("invalid module JSON: %v", err)
		}
		if forbidden(m.Path) || (m.Replace != nil && forbidden(m.Replace.Path)) {
			fail("fork tracer/tool dependency remains: %s; reconcile it explicitly", m.Path)
		}
		mods[m.Path] = m
	}
	c := mods[core]
	if c.Version != "v2.10.1" || (c.Replace != nil && (c.Replace.Path != core || c.Replace.Version != "v2.10.1")) {
		fail("this patch was validated with official core v2.10.1; do not silently downgrade or substitute another core")
	}
	if v1, ok := mods["gopkg.in/DataDog/dd-trace-go.v1"]; ok && (v1.Version != "v1.74.8" || v1.Replace != nil) {
		fail("manual v1 compatibility was validated with unmodified v1.74.8; review other versions explicitly")
	}
	raw := mods["github.com/Shopify/sarama"]
	if !validatedSarama(raw) {
		path, version := effectiveModule(raw)
		fail("this release requires effective Shopify/sarama v1.23.0; selected=%s effective=%s@%s; preserve the official v1.23.0 replacement", raw.Version, path, version)
	}
	s := mods[saramaModule]
	patchDir := os.Getenv("PATCH_SOURCE_DIR")
	if patchDir == "" {
		patchDir = "third_party/ddtrace-sarama"
	}
	want, err := filepath.Abs(patchDir)
	if err != nil {
		fail("resolve patch directory: %v", err)
	}
	if s.Version != "v2.10.1" || s.Replace == nil {
		fail("Shopify integration must be v2.10.1 with the local patch replacement")
	}
	got, err := filepath.Abs(s.Replace.Dir)
	if err != nil || got != want {
		fail("Shopify replacement does not resolve to third_party/ddtrace-sarama")
	}
	source, err := os.ReadFile(filepath.Join(want, "sarama.go"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(source)) != patchSHA256 {
		fail("patched sarama.go is missing or changed; revalidate the patch")
	}
	for _, name := range []string{"orchestrion.tool.go", "orchestrion.yml"} {
		data, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			fail("cannot inspect %s", name)
		}
		if strings.Contains(string(data), "github.com/GuanceCloud/") {
			fail("fork instrumentation reference remains in %s", name)
		}
	}
	if !hasExplicitSaramaIntegration("orchestrion.tool.go") {
		fail("explicit official Shopify integration missing; run verify-official.go --enable-sarama after pin")
	}
	tool, err := exec.LookPath("orchestrion")
	if err != nil {
		fail("official orchestrion executable is not on PATH")
	}
	info, err := buildinfo.ReadFile(tool)
	if err != nil || info.Main.Path != "github.com/DataDog/orchestrion" || info.Main.Version != "v1.12.1" || info.Main.Replace != nil {
		fail("orchestrion must be the official go install ...@v1.12.1 executable")
	}
	if len(os.Args) == 2 {
		info, err := buildinfo.ReadFile(os.Args[1])
		if err != nil {
			fail("cannot inspect built application: %v", err)
		}
		seenCore, seenSarama, seenRawSarama := false, false, false
		for _, dep := range info.Deps {
			if forbidden(dep.Path) || (dep.Replace != nil && forbidden(dep.Replace.Path)) {
				fail("built application contains fork tracer/tool: %s", dep.Path)
			}
			if dep.Path == core {
				seenCore = true
				if dep.Version != "v2.10.1" || (dep.Replace != nil && (dep.Replace.Path != core || dep.Replace.Version != "v2.10.1")) {
					fail("built application core differs from validated version")
				}
			}
			if dep.Path == saramaModule {
				seenSarama = true
				if dep.Version != "v2.10.1" || dep.Replace == nil || dep.Replace.Path != s.Replace.Path || (dep.Replace.Version != "" && dep.Replace.Version != "(devel)") {
					fail("built application did not use the local Shopify patch")
				}
			}
			if dep.Path == "github.com/Shopify/sarama" {
				seenRawSarama = true
				rawDep := module{Path: dep.Path, Version: dep.Version}
				if dep.Replace != nil {
					rawDep.Replace = &module{Path: dep.Replace.Path, Version: dep.Replace.Version}
				}
				if !validatedSarama(rawDep) {
					fail("built application did not use official Shopify/sarama v1.23.0")
				}
			}
		}
		if !seenCore || !seenSarama || !seenRawSarama {
			fail("built application lacks the expected official core/Shopify modules")
		}
	}
	fmt.Println("PATCH_CHECK_OK official-orchestrion=v1.12.1 official-core=v2.10.1 effective-Shopify-sarama=v1.23.0 local-Shopify-patch=verified no-fork-tracer=true")
}

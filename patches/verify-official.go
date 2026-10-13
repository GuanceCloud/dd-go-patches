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
	rawSarama    = "github.com/Shopify/sarama"
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
	return m.Path == rawSarama && path == m.Path &&
		(version == "v1.23.0" || version == "v1.38.1")
}

type plan struct {
	Profile      string `json:"profile"`
	Sarama       string `json:"effective_sarama"`
	Core         string `json:"effective_core"`
	Integration  string `json:"effective_integration"`
	ManualTracer string `json:"effective_manual_tracer,omitempty"`
	GRPC         string `json:"effective_grpc,omitempty"`
	Scope        string `json:"scope"`
}

// An explicit Dockerfile bootstrap may add the tested official integration,
// but must not overwrite another already selected tracer/integration version.
func checkBaseline(mods map[string]module) error {
	for _, m := range mods {
		if forbidden(m.Path) || (m.Replace != nil && forbidden(m.Replace.Path)) {
			return fmt.Errorf("fork tracer/tool remains; reconcile explicitly before bootstrap")
		}
	}
	for _, path := range []string{core, saramaModule} {
		m, exists := mods[path]
		if !exists {
			continue
		}
		if m.Version != "v2.10.1" || (m.Replace != nil && (m.Replace.Path != path || m.Replace.Version != "v2.10.1")) {
			return fmt.Errorf("existing %s is not unmodified official v2.10.1; refusing to overwrite its selected version/replacement", path)
		}
	}
	return nil
}

// Module presence alone is not evidence that application code uses an
// integration: all/v2 and manual v1 can bring unused integrations into the graph.
func selectPlan(mods map[string]module, imports []string) (plan, error) {
	p := plan{Scope: "Shopify/v2 Kafka patch only; not full application compatibility"}
	for _, m := range mods {
		if forbidden(m.Path) || (m.Replace != nil && forbidden(m.Replace.Path)) {
			return p, fmt.Errorf("fork tracer/tool remains; reconcile explicitly before applying")
		}
	}
	shopify := false
	for _, path := range imports {
		if forbidden(path) {
			return p, fmt.Errorf("fork tracer/tool package remains")
		}
		if path == "github.com/IBM/sarama" || strings.HasPrefix(path, "github.com/DataDog/dd-trace-go/contrib/IBM/sarama/") {
			return p, fmt.Errorf("application uses IBM/Sarama; this patch does not cover it")
		}
		if path == "gopkg.in/DataDog/dd-trace-go.v1/contrib/Shopify/sarama" ||
			path == "gopkg.in/DataDog/dd-trace-go.v1/contrib/IBM/sarama" {
			return p, fmt.Errorf("application uses a legacy v1 Sarama wrapper; this v2 patch does not cover it")
		}
		shopify = shopify || path == rawSarama
	}
	if !shopify {
		return p, fmt.Errorf("no Shopify/Sarama package in application dependencies; refusing to inject an unused integration")
	}
	raw := mods[rawSarama]
	_, p.Sarama = effectiveModule(raw)
	if !validatedSarama(raw) {
		return p, fmt.Errorf("unsupported effective Shopify/Sarama %s; tested versions: v1.23.0, v1.38.1; do not change business dependencies to force a match", p.Sarama)
	}
	c := mods[core]
	_, p.Core = effectiveModule(c)
	if c.Version != "v2.10.1" || (c.Replace != nil && (c.Replace.Path != core || c.Replace.Version != "v2.10.1")) {
		return p, fmt.Errorf("official core v2.10.1 required; this tool will not upgrade or downgrade it")
	}
	s := mods[saramaModule]
	p.Integration = s.Version
	if s.Version != "v2.10.1" {
		return p, fmt.Errorf("official Shopify integration v2.10.1 required; this tool will not change its selected version")
	}
	if v1, ok := mods["gopkg.in/DataDog/dd-trace-go.v1"]; ok {
		_, p.ManualTracer = effectiveModule(v1)
		if v1.Version != "v1.74.8" || v1.Replace != nil {
			return p, fmt.Errorf("manual v1 compatibility only tested at unmodified v1.74.8; review pin results explicitly")
		}
	}
	if grpc, ok := mods["google.golang.org/grpc"]; ok {
		path, version := effectiveModule(grpc)
		p.GRPC = path + "@" + version
	}
	p.Profile = "shopify-v2.10.1-sarama-" + p.Sarama
	return p, nil
}

func loadModules() (map[string]module, error) {
	out, err := exec.Command("go", "list", "-mod=readonly", "-m", "-json", "all").Output()
	if err != nil {
		return nil, fmt.Errorf("read-only module resolution failed; resolve dependencies in the full project first")
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	mods := make(map[string]module)
	for {
		var m module
		if err := decoder.Decode(&m); err == io.EOF {
			return mods, nil
		} else if err != nil {
			return nil, fmt.Errorf("invalid module JSON: %w", err)
		}
		mods[m.Path] = m
	}
}

func applicationImports() ([]string, error) {
	out, err := exec.Command("go", "list", "-mod=readonly", "-deps", "-f", "{{.ImportPath}}", "./...").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect actual application packages read-only; run in the full project with its build tags and dependencies")
	}
	return strings.Fields(string(out)), nil
}

func checkToolAndSource(want string) error {
	source, err := os.ReadFile(filepath.Join(want, "sarama.go"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(source)) != patchSHA256 {
		return fmt.Errorf("patched sarama.go is missing or changed; revalidate the patch")
	}
	for _, name := range []string{"orchestrion.tool.go", "orchestrion.yml"} {
		data, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect %s", name)
		}
		if strings.Contains(string(data), "github.com/GuanceCloud/") {
			return fmt.Errorf("fork instrumentation reference remains in %s", name)
		}
	}
	tool, err := exec.LookPath("orchestrion")
	if err != nil {
		return fmt.Errorf("official orchestrion executable is not on PATH")
	}
	info, err := buildinfo.ReadFile(tool)
	if err != nil || info.Main.Path != "github.com/DataDog/orchestrion" || info.Main.Version != "v1.12.1" || info.Main.Replace != nil {
		return fmt.Errorf("orchestrion must be the official go install ...@v1.12.1 executable")
	}
	return nil
}

// Apply only the integration replacement and explicit tool import. Never edit
// a business module requirement/replacement. On failure restore touched files.
func applyPlan(mods map[string]module, want string) error {
	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	cwd, cwdErr := os.Getwd()
	if err != nil || cwdErr != nil || strings.TrimSpace(string(gomod)) != filepath.Join(cwd, "go.mod") {
		return fmt.Errorf("run --apply from the intended application's module root")
	}
	workspace, err := exec.Command("go", "env", "GOWORK").Output()
	if err != nil || (strings.TrimSpace(string(workspace)) != "" && strings.TrimSpace(string(workspace)) != "off") {
		return fmt.Errorf("workspace mode is not supported; use the intended application module with GOWORK=off")
	}
	if err := checkToolAndSource(want); err != nil {
		return err
	}
	if s := mods[saramaModule]; s.Replace != nil {
		existing, _ := filepath.Abs(s.Replace.Dir)
		if existing != want {
			return fmt.Errorf("a different Shopify integration replacement exists; refusing to overwrite it")
		}
	}
	return applyFiles(mods, want, loadModules, func(dir string) error {
		return exec.Command("go", "mod", "edit", "-replace="+saramaModule+"="+dir).Run()
	})
}

func applyFiles(mods map[string]module, want string, resolve func() (map[string]module, error), edit func(string) error) (retErr error) {
	type snapshot struct {
		data   []byte
		mode   os.FileMode
		exists bool
	}
	saved := make(map[string]snapshot)
	for _, path := range []string{"go.mod", "go.sum", "orchestrion.tool.go"} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if path == "go.mod" {
				return fmt.Errorf("go.mod is required in the application root")
			}
			saved[path] = snapshot{}
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to edit non-regular file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		saved[path] = snapshot{data, info.Mode().Perm(), true}
	}
	// Validate config before touching go.mod.
	if s := saved["orchestrion.tool.go"]; s.exists {
		if _, err := parser.ParseFile(token.NewFileSet(), "orchestrion.tool.go", s.data, parser.ParseComments); err != nil {
			return err
		}
	}
	defer func() {
		if retErr == nil {
			return
		}
		for path, s := range saved {
			var err error
			if s.exists {
				err = os.WriteFile(path, s.data, s.mode)
			} else {
				err = os.Remove(path)
			}
			if err != nil && !os.IsNotExist(err) {
				retErr = fmt.Errorf("%w; rollback failed for %s: %v", retErr, path, err)
			}
		}
	}()
	if err := edit(want); err != nil {
		return fmt.Errorf("cannot apply local integration replacement")
	}
	if err := enableSaramaToolFile("orchestrion.tool.go"); err != nil {
		return err
	}
	after, err := resolve()
	if err != nil {
		return err
	}
	if s := after[saramaModule]; s.Replace == nil || s.Replace.Dir != want || s.Version != mods[saramaModule].Version {
		return fmt.Errorf("resolved integration does not use the requested local patch; rolled back")
	}
	// A patch must not silently change the selected/effective application graph.
	for path, before := range mods {
		if path == saramaModule || before.Version == "" {
			continue
		}
		bPath, bVersion := effectiveModule(before)
		aPath, aVersion := effectiveModule(after[path])
		if before.Version != after[path].Version || bPath != aPath || bVersion != aVersion {
			return fmt.Errorf("patch would change dependency %s; rolled back; review the graph explicitly", path)
		}
	}
	for path, afterMod := range after {
		if forbidden(path) || (afterMod.Replace != nil && forbidden(afterMod.Replace.Path)) {
			return fmt.Errorf("patch would introduce a fork tracer/tool; rolled back")
		}
	}
	added := 0
	for path := range after {
		if _, exists := mods[path]; !exists {
			added++
		}
	}
	if added > 0 {
		fmt.Printf("PATCH_GRAPH_NOTE added-module-metadata=%d review-final-module-graph=true\n", added)
	}
	return nil
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
		fail("usage: verify-official.go [--check-baseline | --inspect | --apply | --enable-sarama | application-binary]")
	}
	if len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "--") && os.Args[1] != "--inspect" && os.Args[1] != "--apply" && os.Args[1] != "--check-baseline" {
		fail("unknown option")
	}
	mods, err := loadModules()
	if err != nil {
		fail("%v", err)
	}
	if len(os.Args) == 2 && os.Args[1] == "--check-baseline" {
		if err := checkBaseline(mods); err != nil {
			fail("%v", err)
		}
		fmt.Println("PATCH_BASELINE_OK existing-official-tracer-versions=not-overwritten bootstrap=explicit-v2.10.1-only")
		return
	}
	imports, err := applicationImports()
	if err != nil {
		fail("%v", err)
	}
	p, err := selectPlan(mods, imports)
	if len(os.Args) == 2 && os.Args[1] == "--inspect" {
		_ = json.NewEncoder(os.Stdout).Encode(p)
		if err != nil {
			fail("%v", err)
		}
		return
	}
	if err != nil {
		fail("%v", err)
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
	if len(os.Args) == 2 && os.Args[1] == "--apply" {
		if err := applyPlan(mods, want); err != nil {
			fail("%v", err)
		}
		fmt.Printf("PATCH_APPLY_OK profile=%s business-dependencies=preserved run-verifier-and-build-next=true\n", p.Profile)
		return
	}
	if s.Version != "v2.10.1" || s.Replace == nil {
		fail("Shopify integration must be v2.10.1 with the local patch replacement")
	}
	got, err := filepath.Abs(s.Replace.Dir)
	if err != nil || got != want {
		fail("Shopify replacement does not resolve to third_party/ddtrace-sarama")
	}
	if err := checkToolAndSource(want); err != nil {
		fail("%v", err)
	}
	if !hasExplicitSaramaIntegration("orchestrion.tool.go") {
		fail("explicit official Shopify integration missing; run verify-official.go --enable-sarama after pin")
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
			if dep.Path == "github.com/IBM/sarama" || strings.HasPrefix(dep.Path, "github.com/DataDog/dd-trace-go/contrib/IBM/sarama/") {
				fail("built application contains IBM/Sarama; this patch does not cover that path")
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
				_, version := effectiveModule(rawDep)
				if !validatedSarama(rawDep) || version != p.Sarama {
					fail("built application Shopify/Sarama differs from selected profile %s", p.Profile)
				}
			}
		}
		if !seenCore || !seenSarama || !seenRawSarama {
			fail("built application lacks the expected official core/Shopify modules")
		}
	}
	fmt.Printf("PATCH_CHECK_OK profile=%s official-orchestrion=v1.12.1 official-core=v2.10.1 effective-Shopify-sarama=%s local-Shopify-patch=verified no-fork-tracer=true application-compatibility=not-certified\n", p.Profile, p.Sarama)
	if p.GRPC != "" {
		fmt.Printf("PATCH_SCOPE_NOTE effective-grpc=%s full-business-regression=required\n", p.GRPC)
	}
}

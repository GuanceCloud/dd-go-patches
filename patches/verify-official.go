// Run from the application module root:
//
//	go run ./patches/verify-official.go [/path/to/built/application]
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/gosym"
	"encoding/json"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	core               = "github.com/DataDog/dd-trace-go/v2"
	saramaModule       = "github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2"
	patchSHA256        = "d7d7be709a97dec05ff67640aac7465520b15f4854788671ba70c24b95a9b9f5"
	rawSarama          = "github.com/Shopify/sarama"
	ibmModule          = "github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2"
	rawIBM             = "github.com/IBM/sarama"
	httpModule         = "github.com/DataDog/dd-trace-go/contrib/net/http/v2"
	manualTracerModule = "gopkg.in/DataDog/dd-trace-go.v1"
)

type patchTarget struct {
	Name       string `json:"name"`
	Module     string `json:"module"`
	Version    string `json:"version"`
	RawModule  string `json:"raw_module"`
	Sarama     string `json:"effective_sarama"`
	Directory  string `json:"source_directory"`
	SourceFile string `json:"source_file"`
	SHA256     string `json:"source_sha256"`
}

var shopifyTarget = patchTarget{"shopify", saramaModule, "v2.10.1", rawSarama, "", "third_party/ddtrace-sarama", "sarama.go", patchSHA256}
var ibmTargetV23 = patchTarget{"ibm", ibmModule, "v2.3.0", rawIBM, "", "third_party/ddtrace-ibm-sarama", "producer.go", "1ef23f6872eb0e582fb8e3b91611603206e6ba18c74123ef69f81c88aaca866a"}
var ibmTargetV2101 = patchTarget{"ibm", ibmModule, "v2.10.1", rawIBM, "", "third_party/ddtrace-ibm-sarama-v2.10.1", "producer.go", "9c576ae38be49c58a0bda54da2261ed5eaed431654e536b5727b3bf8c13b8a0f"}

func validatedTarget(t patchTarget, m module) bool {
	path, version := effectiveModule(m)
	if m.Path != t.RawModule || path != t.RawModule {
		return false
	}
	if t.Name == "shopify" {
		return validatedSarama(m)
	}
	if t.Name != "ibm" {
		return false
	}
	switch t.Version {
	case "v2.3.0":
		return version == "v1.43.2" || version == "v1.44.0"
	case "v2.10.1":
		return version == "v1.44.0"
	default:
		return false
	}
}

func targetsOrDefault(targets []patchTarget) []patchTarget {
	if len(targets) == 0 {
		return []patchTarget{shopifyTarget}
	}
	return targets
}

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
	Profile              string        `json:"profile"`
	Sarama               string        `json:"effective_sarama"`
	Core                 string        `json:"effective_core"`
	Integration          string        `json:"effective_integration"`
	ManualTracer         string        `json:"effective_manual_tracer,omitempty"`
	ManualTracerSelected string        `json:"selected_manual_tracer,omitempty"`
	ManualTracerUsed     bool          `json:"manual_tracer_used"`
	GRPC                 string        `json:"effective_grpc,omitempty"`
	Scope                string        `json:"scope"`
	Targets              []patchTarget `json:"targets,omitempty"`
}

// An explicit Dockerfile bootstrap may add the tested official integration,
// but must not overwrite another already selected tracer/integration version.
func checkBaseline(mods map[string]module) error {
	for _, m := range mods {
		if forbidden(m.Path) || (m.Replace != nil && forbidden(m.Replace.Path)) {
			return fmt.Errorf("fork tracer/tool remains; reconcile explicitly before bootstrap")
		}
	}
	for _, expected := range []module{{Path: core, Version: "v2.10.1"}, {Path: saramaModule, Version: "v2.10.1"}} {
		path := expected.Path
		m, exists := mods[path]
		if !exists {
			continue
		}
		if m.Version != expected.Version || (m.Replace != nil && (m.Replace.Path != path || m.Replace.Version != expected.Version)) {
			return fmt.Errorf("existing %s is not unmodified official %s; refusing to overwrite its selected version/replacement", path, expected.Version)
		}
	}
	return nil
}

// Module presence alone is not evidence that application code uses an
// integration: all/v2 and manual v1 can bring unused integrations into the graph.
func selectPlan(mods map[string]module, imports []string) (plan, error) {
	p := plan{Scope: "selected Sarama/v2 Kafka patches only; not full application compatibility"}
	for _, m := range mods {
		if forbidden(m.Path) || (m.Replace != nil && forbidden(m.Replace.Path)) {
			return p, fmt.Errorf("fork tracer/tool remains; reconcile explicitly before applying")
		}
	}
	used := make(map[string]bool)
	for _, path := range imports {
		if forbidden(path) {
			return p, fmt.Errorf("fork tracer/tool package remains")
		}
		if path == "gopkg.in/DataDog/dd-trace-go.v1/contrib/Shopify/sarama" ||
			path == "gopkg.in/DataDog/dd-trace-go.v1/contrib/IBM/sarama" {
			return p, fmt.Errorf("application uses a legacy v1 Sarama wrapper; this v2 patch does not cover it")
		}
		used[path] = true
		if path == manualTracerModule || strings.HasPrefix(path, manualTracerModule+"/") {
			p.ManualTracerUsed = true
		}
	}
	profiles := []string{}
	selectedTargets := []patchTarget{shopifyTarget}
	if used[rawIBM] {
		integration := mods[ibmModule]
		switch integration.Version {
		case "v2.3.0":
			selectedTargets = append(selectedTargets, ibmTargetV23)
		case "v2.10.1":
			selectedTargets = append(selectedTargets, ibmTargetV2101)
		default:
			return p, fmt.Errorf("unsupported official IBM/Sarama integration %s; supported local patch profiles are v2.3.0 and v2.10.1", integration.Version)
		}
	}
	for _, target := range selectedTargets {
		if !used[target.RawModule] {
			continue
		}
		raw := mods[target.RawModule]
		_, target.Sarama = effectiveModule(raw)
		if !validatedTarget(target, raw) {
			return p, fmt.Errorf("unsupported effective %s %s; do not change business dependencies to force a match", target.RawModule, target.Sarama)
		}
		s := mods[target.Module]
		if s.Version != target.Version {
			return p, fmt.Errorf("official %s %s required; this tool will not change its selected version", target.Module, target.Version)
		}
		p.Targets = append(p.Targets, target)
		profiles = append(profiles, target.Name+"-"+target.Version+"-sarama-"+target.Sarama)
	}
	if len(p.Targets) == 0 {
		return p, fmt.Errorf("no Shopify/IBM Sarama package in application dependencies; refusing to inject unused integrations")
	}
	p.Sarama, p.Integration = p.Targets[0].Sarama, p.Targets[0].Version
	c := mods[core]
	_, p.Core = effectiveModule(c)
	if c.Version != "v2.10.1" || (c.Replace != nil && (c.Replace.Path != core || c.Replace.Version != "v2.10.1")) {
		return p, fmt.Errorf("official core v2.10.1 required; this tool will not upgrade or downgrade it")
	}
	// This exact old HTTP integration calls the pre-v2.10 httpsec API. Do not
	// silently change it in --apply, and do not mistake unused graph metadata
	// for an actual application dependency.
	if h, exists := mods[httpModule]; exists && used[httpModule] {
		path, version := effectiveModule(h)
		if path == httpModule && version == "v2.3.0" {
			return p, fmt.Errorf("actual official net/http integration v2.3.0 is incompatible with core v2.10.1 (ProtectRoundTrip API); explicitly align the HTTP integration to v2.10.1 before applying; business dependencies must be reviewed separately")
		}
	}
	if v1, ok := mods[manualTracerModule]; ok {
		_, p.ManualTracer = effectiveModule(v1)
		p.ManualTracerSelected = v1.Version
		// Old requirements in a dependency's go.mod are not necessarily linked.
		// tidy may remove an unused explicit v1.74.8 requirement and reveal such
		// legacy metadata. Require the shim only when application packages use v1.
		if p.ManualTracerUsed && (v1.Version != "v1.74.8" || v1.Replace != nil) {
			return p, fmt.Errorf("application actually uses manual v1 instrumentation; compatibility requires unmodified v1.74.8 (selected=%s effective=%s replacement=%t); inspect the active module/workspace before changing dependencies", v1.Version, p.ManualTracer, v1.Replace != nil)
		}
	} else if p.ManualTracerUsed {
		return p, fmt.Errorf("application uses manual v1 instrumentation but its module cannot be resolved")
	}
	if grpc, ok := mods["google.golang.org/grpc"]; ok {
		path, version := effectiveModule(grpc)
		p.GRPC = path + "@" + version
	}
	p.Profile = strings.Join(profiles, "+")
	return p, nil
}

func loadModules() (map[string]module, error) {
	out, err := goOutput("list", "-mod=readonly", "-m", "-json", "all")
	if err != nil {
		return nil, fmt.Errorf("read-only module resolution failed: %w", err)
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

var diagnosticURL = regexp.MustCompile(`(?i)(?:https?|ssh)://[^\s<>"']+`)

func sanitizeGoDiagnostic(detail string) string {
	return diagnosticURL.ReplaceAllStringFunc(detail, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[redacted URL]"
		}
		if u.User != nil {
			u.User = url.User("REDACTED")
		}
		if u.RawQuery != "" {
			u.RawQuery = "REDACTED"
		}
		if u.Fragment != "" {
			u.Fragment = "REDACTED"
		}
		return u.String()
	})
}

// Preserve the real Go diagnostic: callers must be able to distinguish an
// incomplete module graph, missing go.sum entries, and repository failures.
func goOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	detail := strings.TrimSpace(sanitizeGoDiagnostic(stderr.String()))
	if len(detail) > 8192 {
		detail = detail[:8192] + "\n[diagnostic truncated]"
	}
	if detail == "" {
		return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, detail)
}

func applicationImports() ([]string, error) {
	target := os.Getenv("PATCH_APP_TARGET")
	if target == "" {
		target = "."
	}
	if strings.HasPrefix(target, "-") {
		return nil, fmt.Errorf("PATCH_APP_TARGET must be a Go package target, not a flag")
	}
	out, err := goOutput("list", "-mod=readonly", "-buildvcs=false", "-deps", "-f", "{{.ImportPath}}", target)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect actual application packages read-only: %w", err)
	}
	return strings.Fields(string(out)), nil
}

func checkToolAndSource(want string, targets ...patchTarget) error {
	for _, target := range targetsOrDefault(targets) {
		dir := target.Directory
		if len(targets) == 0 {
			dir = want
		}
		source, err := os.ReadFile(filepath.Join(dir, target.SourceFile))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(source)) != target.SHA256 {
			return fmt.Errorf("patched %s/%s is missing or changed; revalidate the patch", target.Name, target.SourceFile)
		}
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
func applyPlan(mods map[string]module, want string, targets ...patchTarget) error {
	selected := targetsOrDefault(targets)
	if len(targets) == 0 {
		selected[0].Directory = want
	}
	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	cwd, cwdErr := os.Getwd()
	if err != nil || cwdErr != nil || strings.TrimSpace(string(gomod)) != filepath.Join(cwd, "go.mod") {
		return fmt.Errorf("run --apply from the intended application's module root")
	}
	workspace, err := exec.Command("go", "env", "GOWORK").Output()
	if err != nil || (strings.TrimSpace(string(workspace)) != "" && strings.TrimSpace(string(workspace)) != "off") {
		return fmt.Errorf("workspace mode is not supported; use the intended application module with GOWORK=off")
	}
	if err := checkToolAndSource(want, selected...); err != nil {
		return err
	}
	for _, target := range selected {
		if s := mods[target.Module]; s.Replace != nil {
			existing, _ := filepath.Abs(s.Replace.Dir)
			if existing != target.Directory {
				return fmt.Errorf("a different %s integration replacement exists; refusing to overwrite it", target.Name)
			}
		}
	}
	return applyFiles(mods, want, loadModules, func(dir string) error {
		args := []string{"mod", "edit"}
		for _, target := range selected {
			args = append(args, "-replace="+target.Module+"="+target.Directory)
		}
		return exec.Command("go", args...).Run()
	}, selected...)
}

func applyFiles(mods map[string]module, want string, resolve func() (map[string]module, error), edit func(string) error, targets ...patchTarget) (retErr error) {
	selected := targetsOrDefault(targets)
	if len(targets) == 0 {
		selected[0].Directory = want
	}
	targetModules := make(map[string]bool)
	for _, target := range selected {
		targetModules[target.Module] = true
	}
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
	for _, target := range selected {
		if err := enableSaramaToolFile("orchestrion.tool.go", target.Module); err != nil {
			return err
		}
	}
	after, err := resolve()
	if err != nil {
		return err
	}
	for _, target := range selected {
		if s := after[target.Module]; s.Replace == nil || s.Replace.Dir != target.Directory || s.Version != mods[target.Module].Version {
			return fmt.Errorf("resolved %s integration does not use the requested local patch; rolled back", target.Name)
		}
	}
	// A patch must not silently change the selected/effective application graph.
	for path, before := range mods {
		if targetModules[path] || before.Version == "" {
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
func enableSaramaToolFile(path string, modules ...string) error {
	targetModule := saramaModule
	if len(modules) > 0 {
		targetModule = modules[0]
	}
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
		if value == targetModule {
			if spec.Name == nil || spec.Name.Name != "_" {
				return fmt.Errorf("target integration must be a blank import")
			}
			return nil
		}
	}
	at := fset.Position(file.Name.End()).Offset
	updated := append([]byte{}, data[:at]...)
	updated = append(updated, []byte("\n\nimport _ "+strconv.Quote(targetModule)+" // integration\n")...)
	updated = append(updated, data[at:]...)
	updated, err = format.Source(updated)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0o644)
}

func hasExplicitSaramaIntegration(path string, modules ...string) bool {
	targetModule := saramaModule
	if len(modules) > 0 {
		targetModule = modules[0]
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return false
	}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err == nil && value == targetModule && spec.Name != nil && spec.Name.Name == "_" {
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

func validateBinary(info *debug.BuildInfo, mods map[string]module, p plan) error {
	return validateBinarySources(info, mods, p, nil)
}

// Orchestrion adds imports after cmd/go computes module build information.
// Consequently an automatically woven integration can be linked but absent
// from BuildInfo.Deps. Only accept that case with compiled function/source
// evidence from the executable's Go PC/line table, never from go.mod alone.
func validateBinarySources(info *debug.BuildInfo, mods map[string]module, p plan, sources map[string]string) error {
	seen := make(map[string]bool)
	selectedRaw := make(map[string]bool)
	for _, target := range p.Targets {
		selectedRaw[target.RawModule] = true
	}
	for _, dep := range info.Deps {
		if forbidden(dep.Path) || (dep.Replace != nil && forbidden(dep.Replace.Path)) {
			return fmt.Errorf("built application contains fork tracer/tool: %s", dep.Path)
		}
		if (dep.Path == rawIBM || dep.Path == rawSarama) && !selectedRaw[dep.Path] {
			return fmt.Errorf("built application contains unselected Sarama target %s; inspect the actual build entrypoint", dep.Path)
		}
		if dep.Path == core {
			seen[core] = true
			if dep.Version != "v2.10.1" || (dep.Replace != nil && (dep.Replace.Path != core || dep.Replace.Version != "v2.10.1")) {
				return fmt.Errorf("built application core differs from validated version")
			}
		}
		for _, target := range p.Targets {
			if dep.Path == target.Module {
				seen[target.Module] = true
				s := mods[target.Module]
				if dep.Version != target.Version || s.Replace == nil || dep.Replace == nil || dep.Replace.Path != s.Replace.Path || (dep.Replace.Version != "" && dep.Replace.Version != "(devel)") {
					return fmt.Errorf("built application did not use the local %s patch", target.Name)
				}
			}
			if dep.Path == target.RawModule {
				seen[target.RawModule] = true
				raw := module{Path: dep.Path, Version: dep.Version}
				if dep.Replace != nil {
					raw.Replace = &module{Path: dep.Replace.Path, Version: dep.Replace.Version}
				}
				_, version := effectiveModule(raw)
				if !validatedTarget(target, raw) || version != target.Sarama {
					return fmt.Errorf("built application %s differs from selected profile", target.RawModule)
				}
			}
		}
	}
	if !seen[core] {
		coreDir := mods[core].Dir
		if replacement := mods[core].Replace; replacement != nil && replacement.Dir != "" {
			coreDir = replacement.Dir
		}
		for name, source := range sources {
			if strings.HasPrefix(name, core+"/") && sourceWithin(source, coreDir) {
				seen[core] = true
				break
			}
		}
		if !seen[core] {
			return fmt.Errorf("built application lacks verified official core metadata or compiled source evidence")
		}
	}
	for _, target := range p.Targets {
		if !seen[target.RawModule] {
			return fmt.Errorf("built application lacks the expected %s Sarama library", target.Name)
		}
		if !seen[target.Module] {
			want := filepath.Join(target.Directory, target.SourceFile)
			for _, suffix := range []string{".WrapAsyncProducer", ".WrapAsyncProducer.func1"} {
				name := target.Module + suffix
				if !sameSourcePath(sources[name], want) {
					return fmt.Errorf("%s integration absent from build metadata and patched %s source cannot be verified; module metadata alone cannot determine automatic weaving (retain source paths; -trimpath needs separate provenance)", target.Name, name)
				}
			}
		}
	}
	return nil
}

func sameSourcePath(got, want string) bool {
	if got == "" || want == "" || !filepath.IsAbs(got) || !filepath.IsAbs(want) {
		return false
	}
	return filepath.Clean(got) == filepath.Clean(want)
}

func sourceWithin(source, dir string) bool {
	if source == "" || dir == "" || !filepath.IsAbs(source) || !filepath.IsAbs(dir) {
		return false
	}
	rel, err := filepath.Rel(dir, source)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// .gopclntab is retained in stripped Linux Go executables, unlike ELF symbols.
func binaryFunctionSources(path string) (map[string]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Linux ELF executable for automatic-weaving evidence: %w", err)
	}
	defer f.Close()
	text, pcln := f.Section(".text"), f.Section(".gopclntab")
	if text == nil || pcln == nil {
		return nil, fmt.Errorf("Go executable has no readable text/PC-line sections")
	}
	data, err := pcln.Data()
	if err != nil {
		return nil, err
	}
	table, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return nil, err
	}
	sources := make(map[string]string)
	for _, fn := range table.Funcs {
		if strings.Contains(fn.Name, "github.com/GuanceCloud/dd-trace-go") || strings.Contains(fn.Name, "github.com/GuanceCloud/orchestrion") {
			return nil, fmt.Errorf("built application contains fork function %s", fn.Name)
		}
		file, _, _ := table.PCToLine(fn.Entry)
		sources[fn.Name] = file
	}
	return sources, nil
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
	for i := range p.Targets {
		target := &p.Targets[i]
		env := "PATCH_SOURCE_DIR"
		if target.Name == "ibm" {
			env = "PATCH_IBM_SOURCE_DIR"
		}
		if dir := os.Getenv(env); dir != "" {
			target.Directory = dir
		}
		target.Directory, err = filepath.Abs(target.Directory)
		if err != nil {
			fail("resolve %s patch directory: %v", target.Name, err)
		}
	}
	want := p.Targets[0].Directory
	if len(os.Args) == 2 && os.Args[1] == "--apply" {
		if err := applyPlan(mods, want, p.Targets...); err != nil {
			fail("%v", err)
		}
		fmt.Printf("PATCH_APPLY_OK profile=%s business-dependencies=preserved run-verifier-and-build-next=true\n", p.Profile)
		return
	}
	for _, target := range p.Targets {
		s := mods[target.Module]
		if s.Version != target.Version || s.Replace == nil {
			fail("%s integration must be %s with the local patch replacement", target.Name, target.Version)
		}
		got, err := filepath.Abs(s.Replace.Dir)
		if err != nil || got != target.Directory {
			fail("%s replacement does not resolve to its patch source directory", target.Name)
		}
		if !hasExplicitSaramaIntegration("orchestrion.tool.go", target.Module) {
			fail("explicit official %s integration missing; run --apply after pin", target.Name)
		}
	}
	if err := checkToolAndSource(want, p.Targets...); err != nil {
		fail("%v", err)
	}
	if len(os.Args) == 2 {
		info, err := buildinfo.ReadFile(os.Args[1])
		if err != nil {
			fail("cannot inspect built application: %v", err)
		}
		if metadataErr := validateBinary(info, mods, p); metadataErr != nil {
			sources, err := binaryFunctionSources(os.Args[1])
			if err != nil {
				fail("%v; automatic-weaving evidence unavailable: %v", metadataErr, err)
			}
			if err := validateBinarySources(info, mods, p, sources); err != nil {
				fail("%v", err)
			}
			fmt.Println("PATCH_BINARY_EVIDENCE_OK mode=compiled-function-source automatic-module-metadata=omitted")
		}
	}
	fmt.Printf("PATCH_CHECK_OK profile=%s official-orchestrion=v1.12.1 official-core=v2.10.1 local-Sarama-patches=verified no-fork-tracer=true application-compatibility=not-certified\n", p.Profile)
	for _, target := range p.Targets {
		fmt.Printf("PATCH_TARGET_OK target=%s integration=%s@%s effective-sarama=%s@%s\n", target.Name, target.Module, target.Version, target.RawModule, target.Sarama)
	}
	if p.ManualTracer != "" && !p.ManualTracerUsed {
		fmt.Println("PATCH_SCOPE_NOTE manual-v1-module=unused-by-application runtime-version-check=not-applicable")
	}
	if p.GRPC != "" {
		fmt.Printf("PATCH_SCOPE_NOTE effective-grpc=%s full-business-regression=required\n", p.GRPC)
	}
}

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestValidatedSaramaUsesEffectiveVersion(t *testing.T) {
	const path = "github.com/Shopify/sarama"
	cases := []struct {
		name string
		mod  module
		want bool
	}{
		{"direct legacy", module{Path: path, Version: "v1.23.0"}, true},
		{"newer nominal legacy replacement", module{Path: path, Version: "v1.38.1", Replace: &module{Path: path, Version: "v1.23.0"}}, true},
		{"legacy self replacement", module{Path: path, Version: "v1.23.0", Replace: &module{Path: path, Version: "v1.23.0"}}, true},
		{"newer tested implementation", module{Path: path, Version: "v1.38.1"}, true},
		{"tested replacement", module{Path: path, Version: "v1.23.0", Replace: &module{Path: path, Version: "v1.38.1"}}, true},
		{"untested implementation", module{Path: path, Version: "v1.30.0"}, false},
		{"untested replacement", module{Path: path, Version: "v1.38.1", Replace: &module{Path: path, Version: "v1.30.0"}}, false},
		{"unverified local copy", module{Path: path, Version: "v1.23.0", Replace: &module{Path: "./sarama"}}, false},
		{"different implementation", module{Path: path, Version: "v1.23.0", Replace: &module{Path: "github.com/IBM/sarama", Version: "v1.23.0"}}, false},
		{"missing module", module{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatedSarama(tc.mod); got != tc.want {
				t.Fatalf("validatedSarama(%+v)=%t, want %t", tc.mod, got, tc.want)
			}
		})
	}
}

func supportedModules(version string) map[string]module {
	return map[string]module{
		core:                              {Path: core, Version: "v2.10.1"},
		saramaModule:                      {Path: saramaModule, Version: "v2.10.1"},
		rawSarama:                         {Path: rawSarama, Version: "v1.38.1", Replace: &module{Path: rawSarama, Version: version}},
		"gopkg.in/DataDog/dd-trace-go.v1": {Path: "gopkg.in/DataDog/dd-trace-go.v1", Version: "v1.74.8"},
		"google.golang.org/grpc":          {Path: "google.golang.org/grpc", Version: "v1.70.0", Replace: &module{Path: "google.golang.org/grpc", Version: "v1.57.2"}},
	}
}

func TestCheckBaseline(t *testing.T) {
	if err := checkBaseline(map[string]module{}); err != nil {
		t.Fatal(err)
	}
	if err := checkBaseline(supportedModules("v1.23.0")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{core, saramaModule} {
		for _, version := range []string{"v2.3.0", "v2.11.0"} {
			mods := supportedModules("v1.38.1")
			mods[path] = module{Path: path, Version: version}
			if err := checkBaseline(mods); err == nil {
				t.Fatalf("would overwrite %s@%s", path, version)
			}
		}
		mods := supportedModules("v1.23.0")
		mods[path] = module{Path: path, Version: "v2.10.1", Replace: &module{Path: "./custom"}}
		if err := checkBaseline(mods); err == nil {
			t.Fatal("custom replacement accepted")
		}
	}
	fork := "github.com/GuanceCloud/dd-trace-go/v2"
	if err := checkBaseline(map[string]module{fork: {Path: fork, Version: "v2.10.1"}}); err == nil {
		t.Fatal("fork accepted")
	}
}

func TestSelectPlan(t *testing.T) {
	for _, version := range []string{"v1.23.0", "v1.38.1"} {
		mods := supportedModules(version)
		// An unused IBM module in all/v2's graph is not an IBM application path.
		mods["github.com/IBM/sarama"] = module{Path: "github.com/IBM/sarama", Version: "v1.43.2"}
		p, err := selectPlan(mods, []string{rawSarama, "gopkg.in/DataDog/dd-trace-go.v1/contrib/google.golang.org/grpc"})
		if err != nil || p.Sarama != version || p.Profile != "shopify-v2.10.1-sarama-"+version {
			t.Fatalf("profile selection failed: %+v %v", p, err)
		}
		if p.GRPC != "google.golang.org/grpc@v1.57.2" || !strings.Contains(p.Scope, "not full application") {
			t.Fatalf("effective dependency/scope not reported: %+v", p)
		}
	}
}

func ibmModules() map[string]module {
	return ibmModulesAt("v2.3.0", "v1.43.2")
}

func ibmModulesAt(integration, sarama string) map[string]module {
	mods := supportedModules("v1.23.0")
	mods[rawIBM] = module{Path: rawIBM, Version: sarama}
	mods[ibmModule] = module{Path: ibmModule, Version: integration}
	return mods
}

func TestSelectPlanActualIBMAndMixedTargets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		imports []string
		want    int
	}{
		{"IBM only with unused Shopify modules and replace", []string{rawIBM}, 1},
		{"mixed actual packages", []string{rawSarama, rawIBM}, 2},
		{"unused IBM metadata", []string{rawSarama}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := selectPlan(ibmModules(), tc.imports)
			if err != nil || len(p.Targets) != tc.want {
				t.Fatalf("unexpected targets: %+v %v", p, err)
			}
			if tc.imports[0] == rawIBM && (p.Targets[0].Module != ibmModule || p.Profile != "ibm-v2.3.0-sarama-v1.43.2") {
				t.Fatalf("IBM application selected wrong patch: %+v", p)
			}
		})
	}
	for _, tc := range []struct{ path, version string }{{rawIBM, "v1.45.0"}, {ibmModule, "v2.11.0"}} {
		mods := ibmModules()
		mods[tc.path] = module{Path: tc.path, Version: tc.version}
		if _, err := selectPlan(mods, []string{rawIBM}); err == nil {
			t.Fatalf("unsupported IBM combination accepted: %+v", tc)
		}
	}
	mods := ibmModules()
	mods[rawIBM] = module{Path: rawIBM, Version: "v1.44.0"}
	if p, err := selectPlan(mods, []string{rawIBM}); err != nil || p.Sarama != "v1.44.0" {
		t.Fatal("pin-selected IBM v1.44.0 rejected:", p, err)
	}
	mods[rawIBM] = module{Path: rawIBM, Version: "v1.44.0", Replace: &module{Path: rawIBM, Version: "v1.43.2"}}
	if _, err := selectPlan(mods, []string{rawIBM}); err != nil {
		t.Fatal("effective IBM replacement not recognized:", err)
	}
	mods = ibmModulesAt("v2.10.1", "v1.44.0")
	p, err := selectPlan(mods, []string{rawIBM})
	if err != nil || p.Profile != "ibm-v2.10.1-sarama-v1.44.0" || p.Targets[0].Directory != ibmTargetV2101.Directory {
		t.Fatalf("IBM v2.10.1 profile not selected: %+v %v", p, err)
	}
	mods[rawIBM] = module{Path: rawIBM, Version: "v1.43.2"}
	if _, err := selectPlan(mods, []string{rawIBM}); err == nil {
		t.Fatal("IBM v2.10.1 with unsupported IBM/Sarama v1.43.2 was accepted")
	}
}

func TestSelectPlanHTTPCompatibilityIsActualDependencyScoped(t *testing.T) {
	mods := ibmModules()
	mods[httpModule] = module{Path: httpModule, Version: "v2.3.0"}
	if _, err := selectPlan(mods, []string{rawIBM}); err != nil {
		t.Fatal("unused old HTTP metadata caused rejection:", err)
	}
	if _, err := selectPlan(mods, []string{rawIBM, httpModule}); err == nil || !strings.Contains(err.Error(), "ProtectRoundTrip") {
		t.Fatal("known-incompatible active HTTP integration was not diagnosed")
	}
	mods[httpModule] = module{Path: httpModule, Version: "v2.10.1"}
	if _, err := selectPlan(mods, []string{rawIBM, httpModule}); err != nil {
		t.Fatal("aligned HTTP integration rejected:", err)
	}
}

func TestValidateBinaryRejectsAnUnselectedSarama(t *testing.T) {
	mods := ibmModules()
	p, err := selectPlan(mods, []string{rawSarama})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(&debug.BuildInfo{Deps: []*debug.Module{{Path: rawIBM, Version: "v1.43.2"}}}, mods, p); err == nil || !strings.Contains(err.Error(), "unselected") {
		t.Fatal("binary target mismatch accepted")
	}
}

func TestApplicationImportsMatchesConfiguredBuildEntrypoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nfor arg do printf '%s\\n' \"$arg\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, target := range []string{"", "./cmd/smoke", "-tags=unsafe"} {
		t.Setenv("PATCH_APP_TARGET", target)
		args, err := applicationImports()
		if strings.HasPrefix(target, "-") {
			if err == nil {
				t.Fatal("flag accepted as target")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := target
		if want == "" {
			want = "."
		}
		if args[len(args)-1] != want {
			t.Fatalf("wrong entrypoint: %v", args)
		}
	}
}

func TestValidateBinaryTargets(t *testing.T) {
	for _, imports := range [][]string{{rawSarama}, {rawIBM}, {rawSarama, rawIBM}} {
		mods := ibmModules()
		p, err := selectPlan(mods, imports)
		if err != nil {
			t.Fatal(err)
		}
		makeInfo := func() *debug.BuildInfo {
			info := &debug.BuildInfo{Deps: []*debug.Module{{Path: core, Version: "v2.10.1"}}}
			for _, target := range p.Targets {
				dir := "/patch/" + target.Name
				s := mods[target.Module]
				s.Replace = &module{Path: dir, Dir: dir}
				mods[target.Module] = s
				info.Deps = append(info.Deps,
					&debug.Module{Path: target.Module, Version: target.Version, Replace: &debug.Module{Path: dir}},
					&debug.Module{Path: target.RawModule, Version: target.Sarama})
			}
			return info
		}
		if err := validateBinary(makeInfo(), mods, p); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"missing patch", "wrong patch path", "wrong integration version", "raw drift", "core drift", "fork", "missing core", "missing raw"} {
			t.Run(strings.Join(imports, "+")+"/"+kind, func(t *testing.T) {
				info := makeInfo()
				switch kind {
				case "missing patch":
					info.Deps[1].Replace = nil
				case "wrong patch path":
					info.Deps[1].Replace.Path = "/wrong"
				case "wrong integration version":
					info.Deps[1].Version = "v2.99.0"
				case "raw drift":
					info.Deps[2].Replace = &debug.Module{Path: info.Deps[2].Path, Version: "v1.99.0"}
				case "core drift":
					info.Deps[0].Version = "v2.3.0"
				case "fork":
					info.Deps = append(info.Deps, &debug.Module{Path: "github.com/GuanceCloud/dd-trace-go/v2"})
				case "missing core":
					info.Deps = info.Deps[1:]
				case "missing raw":
					info.Deps = append(info.Deps[:2], info.Deps[3:]...)
				}
				if err := validateBinary(info, mods, p); err == nil {
					t.Fatal("invalid binary accepted")
				}
			})
		}
	}
}

func TestApplyMixedTargetsAtomicAndPreservesIBM(t *testing.T) {
	for _, failIBM := range []bool{false, true} {
		t.Run(fmt.Sprint(failIBM), func(t *testing.T) {
			t.Chdir(t.TempDir())
			original := []byte("module example.com/mixed\n\ngo 1.26\n")
			if err := os.WriteFile("go.mod", original, 0o644); err != nil {
				t.Fatal(err)
			}
			mods := ibmModules()
			p, err := selectPlan(mods, []string{rawIBM, rawSarama})
			if err != nil {
				t.Fatal(err)
			}
			for i := range p.Targets {
				p.Targets[i].Directory = "/patch/" + p.Targets[i].Name
			}
			resolve := func() (map[string]module, error) {
				after := ibmModules()
				for _, target := range p.Targets {
					s := after[target.Module]
					s.Replace = &module{Path: target.Directory, Dir: target.Directory}
					after[target.Module] = s
				}
				if failIBM {
					r := after[rawIBM]
					r.Version = "v1.44.0"
					after[rawIBM] = r
				}
				return after, nil
			}
			err = applyFiles(mods, "unused", resolve, func(string) error {
				return os.WriteFile("go.mod", append(append([]byte{}, original...), []byte("// edited\n")...), 0o644)
			}, p.Targets...)
			if failIBM {
				got, _ := os.ReadFile("go.mod")
				if err == nil || !bytes.Equal(got, original) {
					t.Fatal("IBM drift not rolled back")
				}
				if _, err := os.Stat("orchestrion.tool.go"); !os.IsNotExist(err) {
					t.Fatal("mixed config not rolled back")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for _, target := range p.Targets {
					if !hasExplicitSaramaIntegration("orchestrion.tool.go", target.Module) {
						t.Fatal("missing target import")
					}
				}
			}
		})
	}
}

func TestSelectPlanRejectsUncoveredPathsAndVersions(t *testing.T) {
	cases := []struct {
		name    string
		imports []string
		mutate  func(map[string]module)
	}{
		{"unused Shopify replace", []string{"net/http"}, nil},
		{"IBM application", []string{"github.com/IBM/sarama"}, nil},
		{"mixed Shopify IBM", []string{rawSarama, "github.com/IBM/sarama"}, nil},
		{"legacy v1 Shopify", []string{rawSarama, "gopkg.in/DataDog/dd-trace-go.v1/contrib/Shopify/sarama"}, nil},
		{"legacy v1 IBM", []string{rawSarama, "gopkg.in/DataDog/dd-trace-go.v1/contrib/IBM/sarama"}, nil},
		{"unknown Sarama", []string{rawSarama}, func(m map[string]module) { r := m[rawSarama]; r.Replace.Version = "v1.30.0" }},
		{"different core", []string{rawSarama}, func(m map[string]module) { m[core] = module{Path: core, Version: "v2.11.0"} }},
		{"different integration", []string{rawSarama}, func(m map[string]module) { m[saramaModule] = module{Path: saramaModule, Version: "v2.3.0"} }},
		{"old manual v1", []string{rawSarama}, func(m map[string]module) {
			m["gopkg.in/DataDog/dd-trace-go.v1"] = module{Path: "gopkg.in/DataDog/dd-trace-go.v1", Version: "v1.65.1"}
		}},
		{"fork module", []string{rawSarama}, func(m map[string]module) {
			path := "github.com/GuanceCloud/dd-trace-go/v2"
			m[path] = module{Path: path, Version: "v2.10.1"}
		}},
		{"fork replacement", []string{rawSarama}, func(m map[string]module) {
			c := m[core]
			c.Replace = &module{Path: "github.com/GuanceCloud/dd-trace-go/v2", Version: "v2.10.1"}
			m[core] = c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mods := supportedModules("v1.23.0")
			if tc.mutate != nil {
				tc.mutate(mods)
			}
			if p, err := selectPlan(mods, tc.imports); err == nil {
				t.Fatalf("uncovered combination accepted: %+v", p)
			}
		})
	}
}

func TestApplyFilesPreservesBusinessDependenciesAndIsIdempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	original := []byte("module example.com/business\n\ngo 1.26\n\nreplace github.com/Shopify/sarama => github.com/Shopify/sarama v1.23.0\nreplace google.golang.org/grpc => google.golang.org/grpc v1.57.2\n")
	if err := os.WriteFile("go.mod", original, 0o644); err != nil {
		t.Fatal(err)
	}
	mods := supportedModules("v1.23.0")
	resolve := func() (map[string]module, error) {
		after := supportedModules("v1.23.0")
		s := after[saramaModule]
		s.Replace = &module{Path: "/patch/source", Dir: "/patch/source"}
		after[saramaModule] = s
		return after, nil
	}
	edit := func(dir string) error {
		return os.WriteFile("go.mod", append(append([]byte{}, original...), []byte("replace "+saramaModule+" => "+dir+"\n")...), 0o644)
	}
	for i := 0; i < 2; i++ {
		if err := applyFiles(mods, "/patch/source", resolve, edit); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile("go.mod")
		if !bytes.HasPrefix(got, original) || bytes.Count(got, []byte("/patch/source")) != 1 {
			t.Fatal("business config changed or duplicated replacement")
		}
		if !hasExplicitSaramaIntegration("orchestrion.tool.go") {
			t.Fatal("tool config not applied")
		}
	}
}

func TestApplyFilesRollsBackOnResolutionErrorAndGraphDrift(t *testing.T) {
	for _, kind := range []string{"resolution error", "Sarama drift", "grpc drift", "newly added fork"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			original := []byte("module example.com/business\n\ngo 1.26\n")
			sum := []byte("preserved checksum fixture\n")
			_ = os.WriteFile("go.mod", original, 0o644)
			_ = os.WriteFile("go.sum", sum, 0o644)
			mods := supportedModules("v1.23.0")
			resolve := func() (map[string]module, error) {
				if kind == "resolution error" {
					return nil, fmt.Errorf("test failure")
				}
				after := supportedModules("v1.23.0")
				s := after[saramaModule]
				s.Replace = &module{Path: "/patch/source", Dir: "/patch/source"}
				after[saramaModule] = s
				switch kind {
				case "Sarama drift":
					r := after[rawSarama]
					r.Replace.Version = "v1.38.1"
				case "grpc drift":
					r := after["google.golang.org/grpc"]
					r.Replace.Version = "v1.82.1"
				case "newly added fork":
					path := "github.com/GuanceCloud/dd-trace-go/v2"
					after[path] = module{Path: path, Version: "v2.10.1"}
				}
				return after, nil
			}
			edit := func(string) error { return os.WriteFile("go.mod", []byte("changed"), 0o644) }
			if err := applyFiles(mods, "/patch/source", resolve, edit); err == nil {
				t.Fatal("graph drift accepted")
			}
			got, _ := os.ReadFile("go.mod")
			if !bytes.Equal(got, original) {
				t.Fatal("go.mod not restored")
			}
			got, _ = os.ReadFile("go.sum")
			if !bytes.Equal(got, sum) {
				t.Fatal("go.sum not restored")
			}
			if _, err := os.Stat("orchestrion.tool.go"); !os.IsNotExist(err) {
				t.Fatal("new config not removed on rollback")
			}
		})
	}
}

func TestApplyFilesAllowsAdditionalModuleMetadataWithoutVersionDrift(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("module example.com/business\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := supportedModules("v1.38.1")
	resolve := func() (map[string]module, error) {
		after := supportedModules("v1.38.1")
		s := after[saramaModule]
		s.Replace = &module{Dir: "/patch/source"}
		after[saramaModule] = s
		after["example.com/metadata"] = module{Path: "example.com/metadata", Version: "v1.0.0"}
		return after, nil
	}
	if err := applyFiles(before, "/patch/source", resolve, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFilesRejectsSymlinksWithoutWriting(t *testing.T) {
	for _, path := range []string{"go.mod", "go.sum", "orchestrion.tool.go"} {
		t.Run(path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if path != "go.mod" {
				if err := os.WriteFile("go.mod", []byte("module example.com/test\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("missing-target", path); err != nil {
				t.Fatal(err)
			}
			edited := false
			err := applyFiles(supportedModules("v1.23.0"), "/patch/source", nil, func(string) error { edited = true; return nil })
			if err == nil || edited {
				t.Fatal("symlink accepted or edits started")
			}
			if _, err := os.Stat("missing-target"); !os.IsNotExist(err) {
				t.Fatal("symlink target written")
			}
		})
	}
}

func TestEnableSaramaPreservesExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrion.tool.go")
	original := "// Customer configuration.\n//go:build tools\n\npackage tools\n\nimport _ \"github.com/DataDog/orchestrion\" // integration\nimport _ \"github.com/DataDog/dd-trace-go/contrib/net/http/v2\" // integration\n\nconst keep = 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableSaramaToolFile(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"//go:build tools", "// Customer configuration.", "github.com/DataDog/dd-trace-go/contrib/net/http/v2", "const keep = 1"} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("existing configuration lost: %s", want)
		}
	}
	if !hasExplicitSaramaIntegration(path) {
		t.Fatal("target integration missing")
	}
	if err := enableSaramaToolFile(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("configuration update is not idempotent")
	}
}

func TestEnableSaramaCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrion.tool.go")
	if err := enableSaramaToolFile(path); err != nil {
		t.Fatal(err)
	}
	if !hasExplicitSaramaIntegration(path) {
		t.Fatal("new configuration lacks target integration")
	}
}

func TestEnableSaramaRejectsInvalidConfigWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrion.tool.go")
	original := []byte("not valid Go source")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableSaramaToolFile(path); err == nil {
		t.Fatal("invalid config accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatal("invalid existing config was overwritten")
	}
}

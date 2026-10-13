package main

import (
	"bytes"
	"os"
	"path/filepath"
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
		{"newer implementation", module{Path: path, Version: "v1.38.1"}, false},
		{"replacement drift", module{Path: path, Version: "v1.23.0", Replace: &module{Path: path, Version: "v1.38.1"}}, false},
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

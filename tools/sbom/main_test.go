package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

func TestAssembleIsDeterministicAndComplete(t *testing.T) {
	directory := t.TempDir()
	packageJSON := filepath.Join(directory, "package.json")
	payload := `{"dependencies":{"react":"18.3.1","@tanstack/react-query":"^5.59.20","i18next":"^23.16.8"}}`
	if err := os.WriteFile(packageJSON, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	document, err := assemble(fakeBuildInfo(), "vertex-pro", packageJSON, "gcr.io/distroless/static-debian12:nonroot")
	if err != nil {
		t.Fatal(err)
	}
	if document.Schema != sbomSchema {
		t.Fatalf("schema=%s", document.Schema)
	}
	if document.Artifact.GoVersion != "go1.26.6" || document.Artifact.MainModule != "github.com/vertex-pro/vertex-pro/cmd/vertex-pro" {
		t.Fatalf("artifact=%+v", document.Artifact)
	}
	if document.BaseImage != "gcr.io/distroless/static-debian12:nonroot" {
		t.Fatalf("base image=%s", document.BaseImage)
	}
	wantModules := []moduleEntry{
		{Path: "github.com/dustin/go-humanize", Version: "v1.0.1"},
		{Path: "modernc.org/sqlite", Version: "v1.34.4"},
	}
	if !reflect.DeepEqual(document.GoModules, wantModules) {
		t.Fatalf("go modules=%+v", document.GoModules)
	}
	wantWeb := []webDependency{
		{Name: "@tanstack/react-query", Version: "^5.59.20"},
		{Name: "i18next", Version: "^23.16.8"},
		{Name: "react", Version: "18.3.1"},
	}
	if !reflect.DeepEqual(document.WebDependencies, wantWeb) {
		t.Fatalf("web dependencies=%+v", document.WebDependencies)
	}
	again, err := assemble(fakeBuildInfo(), "vertex-pro", packageJSON, "gcr.io/distroless/static-debian12:nonroot")
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("SBOM is not deterministic")
	}
}

func TestAssembleDropsDevelopmentEntriesAndRejectsIncompleteInputs(t *testing.T) {
	directory := t.TempDir()
	packageJSON := filepath.Join(directory, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"dependencies":{"react":"18.3.1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	info := fakeBuildInfo()
	info.Deps = append(info.Deps, &debug.Module{Path: "github.com/vertex-pro/vertex-pro/cmd/vertex-pro", Version: "(devel)"}, &debug.Module{Path: "github.com/empty/version", Version: ""})
	document, err := assemble(info, "vertex-pro", packageJSON, "base")
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range document.GoModules {
		if module.Version == "(devel)" || module.Version == "" {
			t.Fatalf("development entry leaked: %+v", module)
		}
	}

	if _, err := assemble(&buildinfo.BuildInfo{}, "vertex-pro", packageJSON, "base"); err == nil || !strings.Contains(err.Error(), "lacks go version") {
		t.Fatalf("error=%v", err)
	}
	depsLess := fakeBuildInfo()
	depsLess.Deps = nil
	if _, err := assemble(depsLess, "vertex-pro", packageJSON, "base"); err == nil || !strings.Contains(err.Error(), "no module dependencies") {
		t.Fatalf("error=%v", err)
	}
	noWeb := filepath.Join(directory, "empty.json")
	if err := os.WriteFile(noWeb, []byte(`{"dependencies":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := assemble(fakeBuildInfo(), "vertex-pro", noWeb, "base"); err == nil || !strings.Contains(err.Error(), "no dependencies") {
		t.Fatalf("error=%v", err)
	}
}

func TestReadsRealTestBinaryBuildInfo(t *testing.T) {
	info, err := buildinfo.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read own build info: %v", err)
	}
	if info.GoVersion == "" || info.Main.Path == "" {
		t.Fatalf("build info incomplete: go=%q main=%q", info.GoVersion, info.Main.Path)
	}
}

func fakeBuildInfo() *debug.BuildInfo {
	return &buildinfo.BuildInfo{
		GoVersion: "go1.26.6",
		Main:      debug.Module{Path: "github.com/vertex-pro/vertex-pro/cmd/vertex-pro"},
		Deps: []*debug.Module{
			{Path: "modernc.org/sqlite", Version: "v1.34.4"},
			{Path: "github.com/dustin/go-humanize", Version: "v1.0.1"},
		},
	}
}

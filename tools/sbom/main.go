// Package main generates a deterministic software bill of materials for the
// Vertex Pro release artifact: the Go module inventory read from the compiled
// binary, the declared web dependencies, and the container base image. The
// output is content-derived and sorted, so rebuilding the same source produces
// an identical SBOM.
package main

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const sbomSchema = "vertex-pro-sbom-v1"

type moduleEntry struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type webDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type sbom struct {
	Schema          string          `json:"schema"`
	Artifact        artifactFacts   `json:"artifact"`
	GoModules       []moduleEntry   `json:"go_modules"`
	WebDependencies []webDependency `json:"web_dependencies"`
	BaseImage       string          `json:"base_image"`
}

type artifactFacts struct {
	Path       string `json:"path"`
	GoVersion  string `json:"go_version"`
	MainModule string `json:"main_module"`
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(arguments []string) error {
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binaryPath := flags.String("binary", "", "compiled Vertex Pro binary built with the release flags")
	webPackageJSON := flags.String("web-package-json", "", "web/package.json of the embedded frontend")
	baseImage := flags.String("base-image", "", "container base image reference from the release Dockerfile")
	outPath := flags.String("out", "", "optional SBOM output path (stdout when empty)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *binaryPath == "" || *webPackageJSON == "" || *baseImage == "" {
		return errors.New("-binary, -web-package-json, and -base-image are required")
	}
	document, err := buildSBOM(*binaryPath, *webPackageJSON, strings.TrimSpace(*baseImage))
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if *outPath == "" {
		_, err = os.Stdout.Write(payload)
		return err
	}
	return os.WriteFile(*outPath, payload, 0o644)
}

func buildSBOM(binaryPath, webPackageJSON, baseImage string) (sbom, error) {
	info, err := buildinfo.ReadFile(binaryPath)
	if err != nil {
		return sbom{}, fmt.Errorf("read binary build info: %w", err)
	}
	return assemble(info, binaryPath, webPackageJSON, baseImage)
}

func assemble(info *buildinfo.BuildInfo, binaryPath, webPackageJSON, baseImage string) (sbom, error) {
	if info.GoVersion == "" || info.Main.Path == "" {
		return sbom{}, errors.New("binary build info lacks go version or main module")
	}
	modules := make([]moduleEntry, 0, len(info.Deps))
	for _, dep := range info.Deps {
		if dep == nil || dep.Path == "" || dep.Version == "" || dep.Version == "(devel)" {
			continue
		}
		modules = append(modules, moduleEntry{Path: dep.Path, Version: dep.Version})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	web, err := readWebDependencies(webPackageJSON)
	if err != nil {
		return sbom{}, err
	}
	if len(modules) == 0 {
		return sbom{}, errors.New("binary lists no module dependencies")
	}
	if len(web) == 0 {
		return sbom{}, errors.New("frontend declares no dependencies")
	}
	return sbom{
		Schema: sbomSchema,
		Artifact: artifactFacts{
			Path:       binaryPath,
			GoVersion:  info.GoVersion,
			MainModule: info.Main.Path,
		},
		GoModules:       modules,
		WebDependencies: web,
		BaseImage:       baseImage,
	}, nil
}

func readWebDependencies(packageJSONPath string) ([]webDependency, error) {
	payload, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return nil, fmt.Errorf("read web package.json: %w", err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return nil, fmt.Errorf("parse web package.json: %w", err)
	}
	dependencies := make([]webDependency, 0, len(manifest.Dependencies))
	for name, version := range manifest.Dependencies {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			return nil, fmt.Errorf("web dependency %q has an empty name or version", name)
		}
		dependencies = append(dependencies, webDependency{Name: name, Version: version})
	}
	sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].Name < dependencies[j].Name })
	return dependencies, nil
}

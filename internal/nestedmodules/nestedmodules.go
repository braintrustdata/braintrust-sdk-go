// Package nestedmodules provides helpers for working with nested Go modules in this repository.
package nestedmodules

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type goModJSON struct {
	Module struct {
		Path string
	}
	Require []struct {
		Path string
	}
}

type moduleInfo struct {
	relPath     string
	modulePath  string
	dependents  []string
	dependencyN int
	order       int
}

// ReadManifest returns repo-relative nested module paths from the manifest file.
func ReadManifest(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var modules []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		modules = append(modules, line)
	}
	return modules, nil
}

// DependencyOrder returns the nested modules in topological order so that a
// module always appears after any nested modules it directly requires.
func DependencyOrder(repoRoot string, relPaths []string) ([]string, error) {
	modulesByPath := make(map[string]*moduleInfo, len(relPaths))
	modulePathToRel := make(map[string]string, len(relPaths))
	parsedMods := make(map[string]goModJSON, len(relPaths))

	for i, relPath := range relPaths {
		goModPath := filepath.Join(repoRoot, relPath, "go.mod")
		modJSON, err := readGoModJSON(goModPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", goModPath, err)
		}

		parsedMods[relPath] = modJSON
		info := &moduleInfo{
			relPath:    relPath,
			modulePath: modJSON.Module.Path,
			order:      i,
		}
		modulesByPath[relPath] = info
		modulePathToRel[info.modulePath] = relPath
	}

	for _, relPath := range relPaths {
		modJSON := parsedMods[relPath]
		seen := map[string]bool{}
		for _, req := range modJSON.Require {
			dependencyRel, ok := modulePathToRel[req.Path]
			if !ok || dependencyRel == relPath || seen[dependencyRel] {
				continue
			}
			seen[dependencyRel] = true
			modulesByPath[dependencyRel].dependents = append(modulesByPath[dependencyRel].dependents, relPath)
			modulesByPath[relPath].dependencyN++
		}
	}

	var ready []*moduleInfo
	for _, relPath := range relPaths {
		info := modulesByPath[relPath]
		if info.dependencyN == 0 {
			ready = append(ready, info)
		}
	}
	sortByManifestOrder(ready)

	var ordered []string
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		ordered = append(ordered, current.relPath)

		for _, dependentRel := range current.dependents {
			dependent := modulesByPath[dependentRel]
			dependent.dependencyN--
			if dependent.dependencyN == 0 {
				ready = append(ready, dependent)
			}
		}
		sortByManifestOrder(ready)
	}

	if len(ordered) != len(relPaths) {
		var unresolved []string
		for _, relPath := range relPaths {
			if modulesByPath[relPath].dependencyN > 0 {
				unresolved = append(unresolved, relPath)
			}
		}
		return nil, fmt.Errorf("dependency cycle detected among nested modules: %s", strings.Join(unresolved, ", "))
	}

	return ordered, nil
}

func sortByManifestOrder(modules []*moduleInfo) {
	slices.SortFunc(modules, func(a, b *moduleInfo) int {
		return a.order - b.order
	})
}

func readGoModJSON(path string) (goModJSON, error) {
	cmd := exec.Command("go", "mod", "edit", "-json", path)
	output, err := cmd.Output()
	if err != nil {
		return goModJSON{}, err
	}

	var mod goModJSON
	if err := json.Unmarshal(output, &mod); err != nil {
		return goModJSON{}, err
	}
	return mod, nil
}

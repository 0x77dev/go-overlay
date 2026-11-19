package resolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/purpleclay/go-overlay/internal/mod"
	"golang.org/x/mod/modfile"
)

// localModule is one local replace directive to resolve, paired with the
// directory its relative path is resolved against. go.mod replacements
// resolve against the member's own directory; go.work replacements resolve
// against the workspace root, which is why the base travels with the
// directive rather than being inferred later.
type localModule struct {
	repl    mod.Replacement
	baseDir string
	// version is the module version to record. Callers pick it: go.mod
	// resolution reads the member's own require list, workspace resolution
	// prefers the MVS-resolved build list.
	version string
}

// resolveLocalModule hashes one local replacement and builds its
// ModuleConfig. Both the per-module and workspace-level local passes go
// through this, so the git-tracked filter, the go-directive lookup and the
// v0.0.0 fallback exist once rather than in two copies that can drift.
// manifestDir is the directory the manifest is written to, which for a
// workspace is the workspace root rather than any member's directory.
func (r *Resolver) resolveLocalModule(ctx context.Context, lm localModule, manifestDir string, pkgsByMod map[string][]string) (mod.ModuleConfig, error) {
	localDir := lm.repl.LocalPath
	if !filepath.IsAbs(localDir) {
		localDir = filepath.Join(lm.baseDir, localDir)
	}
	localDir, err := filepath.Abs(localDir)
	if err != nil {
		return mod.ModuleConfig{}, fmt.Errorf("failed to resolve local module path %s: %w", lm.repl.LocalPath, err)
	}

	manifestAbs, err := filepath.Abs(manifestDir)
	if err != nil {
		return mod.ModuleConfig{}, fmt.Errorf("failed to resolve manifest directory %s: %w", manifestDir, err)
	}

	// The hash covers every git-tracked file under localDir. When localDir
	// contains the manifest govendor is writing, that includes the manifest
	// itself, so a recorded hash changes the tree it describes and can never
	// match it once the manifest is tracked. Builders read local modules from
	// source and use the hash only for drift detection, so the entry is
	// recorded without one.
	var hash string
	if !encloses(localDir, manifestAbs) {
		tracked, err := GitTrackedFiles(ctx, r.exec, localDir)
		if err != nil {
			return mod.ModuleConfig{}, fmt.Errorf("failed to list git tracked files for local module %s: %w", lm.repl.LocalPath, err)
		}

		hash, err = r.hasher.HashGitTracked(localDir, tracked)
		if err != nil {
			return mod.ModuleConfig{}, fmt.Errorf("failed to hash local module %s: %w", lm.repl.LocalPath, err)
		}
	}

	version := lm.version
	if version == "" {
		version = "v0.0.0"
	}

	return mod.ModuleConfig{
		Path:      lm.repl.OldPath,
		Version:   version,
		Packages:  pkgsByMod[lm.repl.OldPath],
		Hash:      hash,
		GoVersion: localGoDirective(localDir),
		Local:     lm.repl.LocalPath,
	}, nil
}

// encloses reports whether path is dir or lies inside it. Both must be
// absolute. The comparison is lexical: symlinks are not resolved.
func encloses(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// localGoDirective reads the go directive from a local module's go.mod.
// A missing or unparsable go.mod is not an error: the directive is
// advisory metadata, and the module is still hashed and recorded.
func localGoDirective(dir string) string {
	path := filepath.Join(dir, mod.GoModFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	mf, err := modfile.Parse(path, data, nil)
	if err != nil || mf.Go == nil {
		return ""
	}
	return mf.Go.Version
}

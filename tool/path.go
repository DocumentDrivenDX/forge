package tool

import "path/filepath"

// resolvePath resolves a path relative to workDir, or returns it as-is if absolute.
func resolvePath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

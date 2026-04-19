package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// resolvePath resolves a path relative to workDir and follows symlink chains
// for any existing path prefix. If the final path does not yet exist, the
// longest existing prefix is still resolved so writes land on the real target.
func resolvePath(workDir, p string) string {
	if p == "" {
		return workDir
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}

	resolved, err := resolveSymlinkPath(p)
	if err != nil {
		return p
	}
	return resolved
}

func resolveSymlinkPath(p string) (string, error) {
	clean := filepath.Clean(p)
	if clean == "." || clean == string(filepath.Separator) {
		return clean, nil
	}

	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved, nil
	}

	parts := strings.Split(clean, string(filepath.Separator))
	resolved := ""
	start := 0
	if filepath.IsAbs(clean) {
		resolved = string(filepath.Separator)
		if len(parts) > 0 && parts[0] == "" {
			start = 1
		}
	}

	for i := start; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}

		next := filepath.Join(resolved, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				remainder := filepath.Join(parts[i:]...)
				if remainder == "." {
					return next, nil
				}
				if resolved == "" {
					return remainder, nil
				}
				return filepath.Join(resolved, remainder), nil
			}
			return "", err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", err
			}
			resolved = target
			continue
		}

		resolved = next
	}

	return resolved, nil
}

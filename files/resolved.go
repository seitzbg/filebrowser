package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// ResolvedPath returns the virtual target of a symlink, including paths that
// will be created below an existing symlinked directory.
func ResolvedPath(fsys afero.Fs, name string) (string, error) {
	base := BasePath(fsys)
	if base == nil {
		return name, nil
	}
	// Afero filesystems without symlink support (including MemMapFs behind
	// BasePathFs) have no aliases to resolve. Never consult the host filesystem
	// for paths that exist only in those filesystems.
	if _, err := base.ReadlinkIfPossible("."); errors.Is(err, afero.ErrNoReadlink) {
		return name, nil
	}
	root, err := filepath.EvalSymlinks(afero.FullBaseFsPath(base, "/"))
	if err != nil {
		return "", err
	}
	target, err := resolveMissing(afero.FullBaseFsPath(base, name), 0)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// External targets remain subject to the configured filesystem policy.
		return name, nil
	}
	return "/" + filepath.ToSlash(rel), nil
}

func resolveMissing(target string, hops int) (string, error) {
	if hops > maxSymlinkHops {
		return "", os.ErrPermission
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err == nil || !os.IsNotExist(err) {
		return resolved, err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		dest, err := os.Readlink(target)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			parent, err := filepath.EvalSymlinks(filepath.Dir(target))
			if err != nil {
				return "", err
			}
			dest = filepath.Join(parent, dest)
		}
		return resolveMissing(dest, hops+1)
	}
	parent := filepath.Dir(target)
	if parent == target {
		return "", err
	}
	resolved, err = resolveMissing(parent, hops+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(target)), nil
}

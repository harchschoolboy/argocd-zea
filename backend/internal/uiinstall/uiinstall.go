// Package uiinstall copies the UI extension bundled in the backend image into
// the Argo CD extensions directory, so argocd-server can load it without
// downloading anything.
package uiinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Install copies every file under src into dst, keeping the layout
// (resources/zea/extension-zea.js). Existing files are replaced. It returns
// the copied paths relative to dst.
func Install(src, dst string) ([]string, error) {
	var copied []string
	err := fs.WalkDir(os.DirFS(src), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		copied = append(copied, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("install UI from %s to %s: %w", src, dst, err)
	}
	if len(copied) == 0 {
		return nil, errors.New("no UI files in " + src)
	}
	return copied, nil
}

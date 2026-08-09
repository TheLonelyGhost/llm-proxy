package cli

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractTarGz extracts a .tar.gz archive from r into destDir.
// Only regular files are extracted; directories, symlinks, and other
// special files are skipped for security.
func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Sanitise the path: strip leading ../ traversal.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") {
			continue
		}
		dest := filepath.Join(destDir, filepath.Base(clean)) //nolint:gosec // sanitised above

		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode()) //nolint:gosec // operator-controlled dest
		if err != nil {
			return fmt.Errorf("create %q: %w", dest, err)
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // operator-controlled archive
			_ = f.Close()
			return fmt.Errorf("write %q: %w", dest, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %q: %w", dest, err)
		}
	}
	return nil
}

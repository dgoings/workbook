package release

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteExecutableArchive writes a reproducible gzip-compressed ustar archive
// containing binaryPath as the sole executable member named workbook.
func WriteExecutableArchive(binaryPath, archivePath string) (returnErr error) {
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read workbook binary: %w", err)
	}

	output, err := os.CreateTemp(filepath.Dir(archivePath), ".workbook-archive-*")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryPath := output.Name()
	defer func() {
		_ = output.Close()
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:     "workbook",
		Mode:     0o755,
		Uid:      0,
		Gid:      0,
		Size:     int64(len(binary)),
		ModTime:  time.Unix(0, 0).UTC(),
		Typeflag: tar.TypeReg,
		Uname:    "root",
		Gname:    "root",
		Format:   tar.FormatUSTAR,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("write archive header: %w", err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		return fmt.Errorf("write workbook archive member: %w", err)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("close gzip archive: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close release archive: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("set release archive permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		return fmt.Errorf("publish release archive: %w", err)
	}
	return nil
}

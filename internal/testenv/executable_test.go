package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Mutation witness: this is the shape of the ETXTBSY flake seen on Linux CI.
// Every worker writes a fresh script and runs it at once while the others fork
// to run theirs, so a write descriptor that is still open when another worker
// forks travels into that child, and exec of the script fails with "text file
// busy" until the child reaches its own exec. With os.WriteFile in place of
// WriteFile, or with the rename into place but without the fork lock, this
// fails on Linux on nearly every run. macOS does not refuse to exec a file
// open for writing, so it passes there either way.
func TestWriteExecutableCanRunWhileOtherGoroutinesFork(t *testing.T) {
	const workers, runs = 8, 20
	directory := t.TempDir()

	var wait sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for run := range runs {
				path := filepath.Join(directory, fmt.Sprintf("helper-%d-%d", worker, run))
				if err := WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					failures <- fmt.Errorf("write %s: %w", path, err)
					return
				}
				if output, err := exec.Command(path).CombinedOutput(); err != nil {
					failures <- fmt.Errorf("run %s right after writing it: %v\n%s", path, err, output)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestWriteFileReplacesContentsAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("an older, longer body that must not survive\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}

	WriteExecutable(t, path, []byte("#!/bin/sh\nprintf 'ran\\n'\n"))

	output, err := exec.Command(path).Output()
	if err != nil {
		t.Fatalf("run %s: %v", path, err)
	}
	if string(output) != "ran\n" {
		t.Errorf("output = %q, want %q", output, "ran\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %v, want 0755 whatever the file held before", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Dir(path), err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the helper and no leftover temporary file", len(entries))
	}
}

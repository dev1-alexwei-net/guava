package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootedFileReaderReadsIndexedContent(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("current file contents\n")
	if err := os.WriteFile(filepath.Join(rootPath, "docs", "readme.md"), want, 0o600); err != nil {
		t.Fatal(err)
	}

	reader, err := openRootedFileReader(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()

	got, err := reader.read(indexedFile{path: "docs/readme.md", name: "readme.md"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("read() = %q, want %q", got, want)
	}
}

func TestRootedFileReaderRejectsUnsafeAndNonFilePaths(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "escape.txt")); err != nil {
		t.Skipf("cannot create symlink for escape test: %v", err)
	}

	reader, err := openRootedFileReader(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()

	for _, name := range []string{"../outside.txt", "/etc/passwd", ".", "escape.txt", "directory"} {
		t.Run(name, func(t *testing.T) {
			if _, err := reader.read(indexedFile{path: name}); err == nil {
				t.Fatalf("read(%q) succeeded, want an error", name)
			}
		})
	}
}

func TestOpenRootedFileReaderRequiresDirectory(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRootedFileReader(filePath); err == nil {
		t.Fatal("openRootedFileReader() succeeded for a regular file")
	}
}

func TestValidateIndexedPathRejectsTraversalAndAbsoluteNames(t *testing.T) {
	for _, name := range []string{"", ".", "../x", "a/../x", "/etc/passwd", "a\x00b"} {
		if err := validateIndexedPath(name); err == nil {
			t.Errorf("validateIndexedPath(%q) succeeded, want an error", name)
		}
	}
	if err := validateIndexedPath("nested/current.txt"); err != nil {
		t.Fatalf("validateIndexedPath() error = %v", err)
	}
}

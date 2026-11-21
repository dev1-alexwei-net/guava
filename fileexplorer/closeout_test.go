package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryPageReflectsExternalFilesystemChanges(t *testing.T) {
	root := t.TempDir()
	h := makeTestHandler(t, root)

	request := func() string {
		t.Helper()
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET / status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}

	if body := request(); strings.Contains(body, "outside.txt") {
		t.Fatalf("unexpected entry before file creation: %s", body)
	}
	file := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(file, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body := request(); !strings.Contains(body, "outside.txt") {
		t.Fatalf("new file is absent from refreshed page: %s", body)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if body := request(); strings.Contains(body, "outside.txt") {
		t.Fatalf("removed file remains on refreshed page: %s", body)
	}
}

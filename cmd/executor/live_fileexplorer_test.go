package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type executorResponsesRequest struct {
	Input string `json:"input"`
}

// TestExecutorAgainstLiveFileexplorerManifest exercises the command-line
// executor against a real fileexplorer process and its SQLite manifest. The
// gateway is stubbed so the test remains deterministic and does not need
// external credentials or services.
func TestExecutorAgainstLiveFileexplorerManifest(t *testing.T) {
	repoRoot := executorRepoRoot(t)
	binDir := t.TempDir()
	explorerBinary := filepath.Join(binDir, "fileexplorer")
	executorBinary := filepath.Join(binDir, "executor")
	buildTestBinary(t, repoRoot, explorerBinary, "./fileexplorer")
	buildTestBinary(t, repoRoot, executorBinary, "./cmd/executor")

	root := t.TempDir()
	files := map[string]string{
		"a.txt":                    "root-level work",
		"docs/nested/inside.txt":   "nested selected work",
		"docs-archive/outside.txt": "prefix-collision sibling work",
		"instructions.md":          "indexed instructions from the root",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fallback := filepath.Join(t.TempDir(), "fallback-instructions.md")
	const fallbackInstructions = "fallback instructions"
	if err := os.WriteFile(fallback, []byte(fallbackInstructions), 0o600); err != nil {
		t.Fatal(err)
	}

	database := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	explorerURL, stopExplorer := startTestFileexplorer(t, explorerBinary, root, database)
	t.Cleanup(stopExplorer)

	requests := make(chan executorResponsesRequest, 64)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var request executorResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<complete />"}]}]}`)
	}))
	defer gateway.Close()

	run := func(subdirectory string) []string {
		t.Helper()
		args := []string{
			"-database", database,
			"-root", root,
			"-instructions", fallback,
			"-gateway-url", gateway.URL,
			"-fileexplorer-url", explorerURL,
			"-agent-id", "e2e-test",
		}
		if subdirectory != "" {
			args = append(args, "-subdirectory", subdirectory)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, executorBinary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("executor -subdirectory %q failed: %v\n%s", subdirectory, err, output)
		}

		var inputs []string
		for {
			select {
			case request := <-requests:
				inputs = append(inputs, request.Input)
			default:
				return inputs
			}
		}
	}

	wholeRoot := run("")
	if len(wholeRoot) != 3 {
		t.Fatalf("whole-root run processed %d files, want 3: %#v", len(wholeRoot), wholeRoot)
	}
	for _, name := range []string{"a.txt", "docs/nested/inside.txt", "docs-archive/outside.txt"} {
		if !containsPromptPath(wholeRoot, name) {
			t.Errorf("whole-root run did not process %q: %#v", name, wholeRoot)
		}
	}
	assertPromptContains(t, wholeRoot, "indexed instructions from the root")
	for _, input := range wholeRoot {
		if strings.Contains(input, `Current file path: "instructions.md"`) {
			t.Errorf("instruction file was treated as work: %q", input)
		}
	}

	scoped := run("docs")
	if len(scoped) != 1 || !containsPromptPath(scoped, "docs/nested/inside.txt") {
		t.Fatalf("scoped run inputs = %#v; want only docs/nested/inside.txt", scoped)
	}
	if strings.Contains(scoped[0], "docs-archive/") || strings.Contains(scoped[0], "root-level work") {
		t.Errorf("scoped run escaped its selected folder: %q", scoped[0])
	}
	if !strings.Contains(scoped[0], "indexed instructions from the root") {
		t.Errorf("scoped run did not source indexed root instructions: %q", scoped[0])
	}
	if strings.Contains(scoped[0], fallbackInstructions) {
		t.Errorf("scoped run used fallback despite indexed root instructions: %q", scoped[0])
	}

	if err := os.Remove(filepath.Join(root, "instructions.md")); err != nil {
		t.Fatal(err)
	}
	fallbackRun := run("docs")
	if len(fallbackRun) != 1 || !containsPromptPath(fallbackRun, "docs/nested/inside.txt") {
		t.Fatalf("fallback run inputs = %#v; want only docs/nested/inside.txt", fallbackRun)
	}
	if !strings.Contains(fallbackRun[0], fallbackInstructions) {
		t.Errorf("run without indexed instructions did not use fallback: %q", fallbackRun[0])
	}

	// The CLI must convert both supported OS signals into context
	// cancellation, rather than terminating while an HTTP request is active.
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(fmt.Sprintf("signal_%v", signal), func(t *testing.T) {
			started := make(chan struct{}, 1)
			releaseHandler := make(chan struct{})
			blockingGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-releaseHandler
			}))
			defer blockingGateway.Close()
			defer close(releaseHandler)

			args := []string{
				"-database", database,
				"-root", root,
				"-instructions", fallback,
				"-gateway-url", blockingGateway.URL,
				"-fileexplorer-url", explorerURL,
				"-agent-id", "e2e-signal-test",
			}
			cmd := exec.Command(executorBinary, args...)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatalf("start executor: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case <-started:
			case err := <-done:
				t.Fatalf("executor exited before gateway request: %v\n%s", err, output.String())
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("executor did not reach the gateway")
			}

			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatalf("send %v to executor: %v", signal, err)
			}
			select {
			case err := <-done:
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("executor exit after %v = %v, want graceful run-error exit 1\n%s", signal, err, output.String())
				}
				if !strings.Contains(output.String(), "executor: run failed:") {
					t.Errorf("executor output after %v does not report a handled run cancellation: %s", signal, output.String())
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatalf("executor did not stop after %v", signal)
			}

		})
	}
}

func executorRepoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(source)))
}

func buildTestBinary(t *testing.T, repoRoot, output, packagePath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", output, packagePath)
	command.Dir = repoRoot
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, result)
	}
}

func startTestFileexplorer(t *testing.T, binary, root, database string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)

	command := exec.Command(binary,
		"-host", "127.0.0.1",
		"-port", strconv.Itoa(port),
		"-root", root,
		"-db", database,
	)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatalf("start fileexplorer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if command.Process == nil || command.ProcessState != nil {
				return
			}
			select {
			case <-done:
				return
			default:
			}
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		})
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("fileexplorer exited before becoming ready: %v\n%s", err, logs.String())
		default:
		}
		response, requestErr := http.Get(baseURL + "/")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return baseURL, stop
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("fileexplorer did not become ready\n%s", logs.String())
	return "", nil
}

func containsPromptPath(inputs []string, path string) bool {
	needle := fmt.Sprintf(`Current file path: "%s"`, path)
	for _, input := range inputs {
		if strings.Contains(input, needle) {
			return true
		}
	}
	return false
}

func assertPromptContains(t *testing.T, inputs []string, text string) {
	t.Helper()
	for _, input := range inputs {
		if !strings.Contains(input, text) {
			t.Errorf("prompt does not contain %q: %q", text, input)
		}
	}
}

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var (
	errRefresh = errors.New("refresh failed")
	errList    = errors.New("listing failed")
	errProcess = errors.New("processing failed")
)

// fakeRefresher counts Refresh calls and can fail on one specific call.
type fakeRefresher struct {
	calls   int
	failOn  int // 1-based call number to fail; 0 = never fail
	failErr error
}

func (f *fakeRefresher) Refresh(context.Context) error {
	f.calls++
	if f.failOn > 0 && f.calls == f.failOn {
		return f.failErr
	}
	return nil
}

// fakeLister returns successive snapshots of the indexed path listing,
// one per FilePaths call. Calls beyond the last snapshot repeat it.
type fakeLister struct {
	snapshots [][]string
	failOn    int // 1-based call number to fail; 0 = never fail
	failErr   error
	calls     int
}

func (f *fakeLister) FilePaths(context.Context) ([]string, error) {
	f.calls++
	if f.failOn > 0 && f.calls == f.failOn {
		return nil, f.failErr
	}
	snapshot := f.snapshots[len(f.snapshots)-1]
	if f.calls <= len(f.snapshots) {
		snapshot = f.snapshots[f.calls-1]
	}
	return snapshot, nil
}

// recordedProcessFile records processed paths and can fail on, or cancel
// the context after, one specific path.
type recordedProcessFile struct {
	processed []string
	failOn    string
	failErr   error
	cancelOn  string
	cancel    context.CancelFunc
}

func (p *recordedProcessFile) process(_ context.Context, path string) error {
	p.processed = append(p.processed, path)
	if p.cancelOn == path && p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.failOn == path {
		return p.failErr
	}
	return nil
}

func wantPaths(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("processed %d paths %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("processed path %d = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

func TestCoordinatorProcessesFilesInListingOrder(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{"folder/alpha.txt", "zeta.txt"}}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantPaths(t, processor.processed, []string{"folder/alpha.txt", "zeta.txt"})
	if refresher.calls != 3 {
		t.Errorf("Refresh calls = %d, want one before the first selection plus one after each completion", refresher.calls)
	}
	if lister.calls != 3 {
		t.Errorf("FilePaths calls = %d, want one per refresh", lister.calls)
	}
}

func TestCoordinatorAppendsNewPathsAfterQueuedWork(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{
		{"b.txt", "c.txt"},
		{"a.txt", "b.txt", "c.txt"},
	}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// a.txt sorts before b.txt but was discovered in the second batch,
	// so it is appended after the already queued c.txt.
	wantPaths(t, processor.processed, []string{"b.txt", "c.txt", "a.txt"})
}

func TestCoordinatorProcessesPathOncePerRun(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{
		{"x.txt"},
		{"x.txt", "y.txt"},
		{"x.txt", "y.txt"},
	}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// x.txt appears in every snapshot but is processed exactly once.
	wantPaths(t, processor.processed, []string{"x.txt", "y.txt"})
}

func TestCoordinatorReturnsForEmptyIndex(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{}}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(processor.processed) != 0 {
		t.Errorf("processed %v, want no paths", processor.processed)
	}
	if refresher.calls != 1 || lister.calls != 1 {
		t.Errorf("Refresh/FilePaths calls = %d/%d, want 1/1", refresher.calls, lister.calls)
	}
}

func TestCoordinatorStopsOnRefreshFailure(t *testing.T) {
	refresher := &fakeRefresher{failOn: 2, failErr: errRefresh}
	lister := &fakeLister{snapshots: [][]string{{"a.txt", "b.txt"}}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); !errors.Is(err, errRefresh) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, errRefresh)
	}
	wantPaths(t, processor.processed, []string{"a.txt"})
	if refresher.calls != 2 {
		t.Errorf("Refresh calls = %d, want the second refresh to fail the run", refresher.calls)
	}
	if got := coordinator.queue.Pending(); len(got) != 1 || got[0] != "b.txt" {
		t.Errorf("pending after refresh failure = %v, want unprocessed item retained", got)
	}
}

func TestCoordinatorStopsOnListingFailure(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{"a.txt"}}, failOn: 1, failErr: errList}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); !errors.Is(err, errList) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, errList)
	}
	if len(processor.processed) != 0 {
		t.Errorf("processed %v, want no paths", processor.processed)
	}
}

func TestCoordinatorStopsOnProcessingFailure(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{"a.txt", "b.txt"}}}
	processor := &recordedProcessFile{failOn: "b.txt", failErr: errProcess}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); !errors.Is(err, errProcess) || !strings.Contains(err.Error(), `"b.txt"`) {
		t.Fatalf("Run() error = %v, want wrapped %v with file path", err, errProcess)
	}
	wantPaths(t, processor.processed, []string{"a.txt", "b.txt"})
	if got := coordinator.queue.Pending(); len(got) != 1 || got[0] != "b.txt" {
		t.Errorf("pending after processing failure = %v, want failed item retained", got)
	}
}

func TestCoordinatorDoesNotStartForCanceledContext(t *testing.T) {
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{"a.txt"}}}
	processor := &recordedProcessFile{}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := coordinator.Run(ctx); err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if refresher.calls != 0 || lister.calls != 0 {
		t.Errorf("Refresh/FilePaths calls = %d/%d, want 0/0 for a canceled context", refresher.calls, lister.calls)
	}
	if len(processor.processed) != 0 {
		t.Errorf("processed %v, want no paths", processor.processed)
	}
}

func TestCoordinatorStopsWhenContextCancelsMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	refresher := &fakeRefresher{}
	lister := &fakeLister{snapshots: [][]string{{"a.txt", "b.txt"}}}
	processor := &recordedProcessFile{cancelOn: "a.txt", cancel: cancel}

	coordinator, err := NewCoordinator(refresher, lister, processor.process)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(ctx); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), `"a.txt"`) {
		t.Fatalf("Run() error = %v, want context.Canceled wrapped with active path", err)
	}
	// b.txt must not start, and cancellation must not mark a.txt complete.
	wantPaths(t, processor.processed, []string{"a.txt"})
	if got := coordinator.queue.Pending(); len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
		t.Errorf("pending after cancellation = %v, want current and later work retained", got)
	}
}

func TestNewCoordinatorRejectsMissingDependencies(t *testing.T) {
	lister := &fakeLister{snapshots: [][]string{{}}}
	processor := &recordedProcessFile{}
	var process ProcessFile = processor.process

	tests := []struct {
		name      string
		refresher Refresher
		lister    FileLister
		process   ProcessFile
	}{
		{name: "nil refresher", refresher: nil, lister: lister, process: process},
		{name: "nil lister", refresher: &fakeRefresher{}, lister: nil, process: process},
		{name: "nil processor", refresher: &fakeRefresher{}, lister: lister, process: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCoordinator(tc.refresher, tc.lister, tc.process); err == nil {
				t.Fatal("NewCoordinator() succeeded, want an error")
			}
		})
	}
}

func TestCoordinatorRunRejectsNilCoordinator(t *testing.T) {
	var coordinator *Coordinator
	if err := coordinator.Run(context.Background()); err == nil {
		t.Fatal("Run() on a nil coordinator succeeded, want an error")
	}
}

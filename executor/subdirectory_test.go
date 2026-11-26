package executor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type staticLister struct {
	paths []string
	err   error
}

func (l staticLister) FilePaths(context.Context) ([]string, error) {
	return l.paths, l.err
}

func TestSubdirectoryListerFiltersByExactFolderPrefix(t *testing.T) {
	all := []string{
		"docs/deep/page.md",
		"docs/notes.md",
		"docs2/file.md",
		"other/docs/file.md",
		"root.txt",
	}
	tests := []struct {
		name         string
		subdirectory string
		want         []string
	}{
		{name: "top-level folder", subdirectory: "docs", want: []string{"docs/deep/page.md", "docs/notes.md"}},
		{name: "nested folder", subdirectory: "docs/deep", want: []string{"docs/deep/page.md"}},
		{name: "sibling with shared name prefix is out of scope", subdirectory: "doc", want: []string{}},
		{name: "no match", subdirectory: "missing", want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lister := newSubdirectoryLister(staticLister{paths: all}, test.subdirectory)
			got, err := lister.FilePaths(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := test.want; !reflect.DeepEqual(got, want) {
				t.Fatalf("FilePaths() = %#v, want %#v", got, want)
			}
		})
	}

	t.Run("list error propagates", func(t *testing.T) {
		lister := newSubdirectoryLister(staticLister{err: errors.New("list failed")}, "docs")
		if _, err := lister.FilePaths(context.Background()); err == nil || err.Error() != "list failed" {
			t.Fatalf("FilePaths() error = %v, want the underlying error", err)
		}
	})

	t.Run("nil receiver rejects the call", func(t *testing.T) {
		var lister *subdirectoryLister
		if _, err := lister.FilePaths(context.Background()); err == nil {
			t.Fatal("FilePaths() on nil lister succeeded, want an error")
		}
	})
}

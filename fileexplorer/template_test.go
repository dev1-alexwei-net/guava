package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderPageContainsServerRenderedControls(t *testing.T) {
	var body bytes.Buffer
	data := pageData{
		Current:   "docs",
		ParentURL: "/",
		Breadcrumbs: []breadcrumb{
			{Name: "docs", URL: "/d/docs"},
		},
		Items: []pageItem{
			{Name: "notes.txt", Path: "docs/notes.txt", URL: "/f/docs/notes.txt", EditURL: "/edit/docs/notes.txt", DownloadURL: "/f/docs/notes.txt?download=1", DeleteURL: "/confirm-delete/docs/notes.txt", Size: 7},
		},
	}
	if err := renderPage(&body, data); err != nil {
		t.Fatalf("renderPage: %v", err)
	}
	got := body.String()
	for _, want := range []string{"Guava · File explorer", `name="viewport"`, `href="/static/bootstrap.min.css"`, `class="container py-4 py-lg-5"`, `class="col-12 col-lg-4"`, `href="/d/docs"`, `action="/mkdir"`, `action="/create-file"`, `action="/upload"`, `action="/rename"`, `href="/confirm-delete/docs/notes.txt"`, `href="/edit/docs/notes.txt"`, `aria-label="New name for notes.txt"`, "notes.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
	if strings.Contains(got, "<script") || strings.Contains(got, "fetch(") || strings.Contains(got, "XMLHttpRequest") || strings.Contains(got, "https://") {
		t.Fatal("rendered page contains JavaScript or a remote asset reference")
	}
}

func TestRenderPageEscapesEntryNames(t *testing.T) {
	var body bytes.Buffer
	data := pageData{Items: []pageItem{{Name: `<script>alert(1)</script>`, Path: "safe.txt", URL: "/f/safe.txt"}}}
	if err := renderPage(&body, data); err != nil {
		t.Fatalf("renderPage: %v", err)
	}
	if strings.Contains(body.String(), `<script>alert(1)</script>`) {
		t.Fatal("entry name was not HTML-escaped")
	}
	if !strings.Contains(body.String(), "&lt;script&gt;") {
		t.Fatal("escaped entry name not present")
	}
}

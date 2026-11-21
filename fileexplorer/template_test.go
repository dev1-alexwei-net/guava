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
			{Name: "notes.txt", Path: "docs/notes.txt", URL: "/f/docs/notes.txt", Size: 7},
		},
	}
	if err := renderPage(&body, data); err != nil {
		t.Fatalf("renderPage: %v", err)
	}
	got := body.String()
	for _, want := range []string{"Guava file explorer", `href="/d/docs"`, `action="/mkdir"`, `action="/upload"`, `action="/rename"`, `action="/delete"`, "notes.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
	if strings.Contains(got, "<script") || strings.Contains(got, "fetch(") || strings.Contains(got, "XMLHttpRequest") {
		t.Fatal("rendered page contains client-side JavaScript")
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

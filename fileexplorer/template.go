package main

import (
	"embed"
	"html/template"
	"io"
)

//go:embed templates/index.html
var pageFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(pageFiles, "templates/index.html"))

type pageData struct {
	Current     string
	Breadcrumbs []breadcrumb
	ParentURL   string
	Items       []pageItem
	Error       string
}

type breadcrumb struct {
	Name string
	URL  string
}

type pageItem struct {
	Name  string
	Path  string
	URL   string
	IsDir bool
	Size  int64
}

func renderPage(w io.Writer, data pageData) error {
	return pageTemplate.ExecuteTemplate(w, "index.html", data)
}

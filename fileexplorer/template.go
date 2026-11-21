package main

import (
	"embed"
	"html/template"
	"io"
)

//go:embed templates/index.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var pageFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(pageFiles, "templates/index.html"))

type pageData struct {
	Current       string
	Breadcrumbs   []breadcrumb
	ParentURL     string
	Items         []pageItem
	Error         string
	Editor        bool
	EditorPath    string
	EditorURL     string
	Content       string
	Saved         bool
	ConfirmDelete bool
	DeletePath    string
	DeleteName    string
	ReturnURL     string
}

type breadcrumb struct {
	Name string
	URL  string
}

type pageItem struct {
	Name        string
	Path        string
	URL         string
	EditURL     string
	DownloadURL string
	DeleteURL   string
	IsDir       bool
	Size        int64
}

func renderPage(w io.Writer, data pageData) error {
	return pageTemplate.ExecuteTemplate(w, "index.html", data)
}

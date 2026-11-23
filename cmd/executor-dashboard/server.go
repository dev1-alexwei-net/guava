package main

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dev1-alexwei-net/guava/executor"
)

//go:embed templates/index.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var dashboardFiles embed.FS

type dashboard struct {
	controller *executorController
	page       *template.Template
	routes     http.Handler
}

type dashboardPageData struct {
	Status    string
	Logs      string
	LastErr   string
	Error     string
	Refresh   int
	Theme     string
	NextTheme string
	Running   bool
	Busy      bool
}

func newDashboard(config executor.Config, logLevel slog.Level) (*dashboard, error) {
	controller, err := newExecutorController(config, logLevel)
	if err != nil {
		return nil, err
	}
	page, err := template.ParseFS(dashboardFiles, "templates/index.html")
	if err != nil {
		controller.Close()
		return nil, err
	}

	app := &dashboard{controller: controller, page: page}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.show)
	mux.HandleFunc("POST /start", app.start)
	mux.HandleFunc("POST /stop", app.stop)
	mux.HandleFunc("GET /static/bootstrap.min.css", app.stylesheet)
	app.routes = mux
	return app, nil
}

func (d *dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.routes.ServeHTTP(w, r)
}

func (d *dashboard) Close() {
	d.controller.Close()
}

func (d *dashboard) show(w http.ResponseWriter, r *http.Request) {
	snapshot := d.controller.Snapshot()
	refresh := parseRefresh(r.URL.Query().Get("refresh"))
	theme := parseTheme(r.URL.Query().Get("theme"))
	nextTheme := "dark"
	if theme == "dark" {
		nextTheme = "light"
	}
	data := dashboardPageData{
		Status:    snapshot.Status,
		Logs:      snapshot.Logs,
		LastErr:   snapshot.LastErr,
		Refresh:   refresh,
		Theme:     theme,
		NextTheme: nextTheme,
		Running:   snapshot.Status == "running",
		Busy:      snapshot.Status == "running" || snapshot.Status == "stopping",
		Error:     r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := d.page.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
	}
}

func (d *dashboard) stylesheet(w http.ResponseWriter, _ *http.Request) {
	css, err := dashboardFiles.ReadFile("static/bootstrap.min.css")
	if err != nil {
		http.Error(w, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(css)
}

func (d *dashboard) start(w http.ResponseWriter, r *http.Request) {
	err := d.controller.Start()
	redirectDashboard(w, r, err)
}

func (d *dashboard) stop(w http.ResponseWriter, r *http.Request) {
	err := d.controller.Stop()
	redirectDashboard(w, r, err)
}

func redirectDashboard(w http.ResponseWriter, r *http.Request, err error) {
	query := url.Values{}
	if refresh := parseRefresh(r.URL.Query().Get("refresh")); refresh > 0 {
		query.Set("refresh", strconv.Itoa(refresh))
	}
	if theme := parseTheme(r.URL.Query().Get("theme")); theme == "dark" {
		query.Set("theme", theme)
	}
	if err != nil {
		query.Set("error", err.Error())
	}
	target := "/"
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func parseTheme(value string) string {
	if value == "dark" {
		return "dark"
	}
	return "light"
}

func parseRefresh(value string) int {
	switch value {
	case "2":
		return 2
	case "5":
		return 5
	case "10":
		return 10
	case "30":
		return 30
	default:
		return 0
	}
}

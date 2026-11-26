package main

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/internal/version"
)

//go:embed templates/index.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var dashboardFiles embed.FS

type dashboard struct {
	controller   *executorController
	page         *template.Template
	databasePath string
	routes       http.Handler
}

type dashboardPageData struct {
	Status           string
	Version          string
	Logs             string
	LastErr          string
	Error            string
	Refresh          int
	Theme            string
	NextTheme        string
	Running          bool
	Busy             bool
	AgentID          string
	CLIArgs          string
	StartedAt        time.Time
	FinishedAt       time.Time
	StartedFormatted string
	Duration         string
	HasSession       bool
	Subdirectories   []string
	SubdirectoryHint string
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

	app := &dashboard{
		controller:   controller,
		page:         page,
		databasePath: config.DatabasePath,
	}
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
	subdirectories, subdirectoryErr := d.subdirectories(r.Context())
	if subdirectoryErr != nil {
		// The page still renders: the select falls back to the root option
		// only, and a hint explains the empty list.
		subdirectories = nil
	}
	data := pageData(d.controller.Snapshot(), r.URL.Query(), subdirectories, subdirectoryErr)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := d.page.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
	}
}

// subdirectories reads the candidate subdirectories from the fileexplorer
// manifest, fresh on every page render so the listing stays current when the
// page refreshes. The database is opened read-only; a failed read is an
// error the caller degrades on rather than a page failure.
func (d *dashboard) subdirectories(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return executor.ListSubdirectories(ctx, d.databasePath)
}

func pageData(snapshot executorSnapshot, query url.Values, subdirectories []string, subdirectoryErr error) dashboardPageData {
	refresh := parseRefresh(query.Get("refresh"))
	theme := parseTheme(query.Get("theme"))
	nextTheme := "dark"
	if theme == "dark" {
		nextTheme = "light"
	}
	hasSession := !snapshot.StartedAt.IsZero()
	duration := sessionDuration(snapshot)

	var startedFormatted string
	if hasSession {
		startedFormatted = snapshot.StartedAt.Format(time.DateTime)
	}

	var subdirectoryHint string
	if subdirectoryErr != nil {
		subdirectoryHint = "The subdirectory list is unavailable right now; the root option still works."
	}

	return dashboardPageData{
		Status:           snapshot.Status,
		Version:          version.SemVer,
		Logs:             snapshot.Logs,
		LastErr:          snapshot.LastErr,
		Refresh:          refresh,
		Theme:            theme,
		NextTheme:        nextTheme,
		Running:          snapshot.Status == "running",
		Busy:             snapshot.Status == "running" || snapshot.Status == "stopping",
		Error:            query.Get("error"),
		AgentID:          snapshot.AgentID,
		CLIArgs:          snapshot.CLIArgs,
		StartedAt:        snapshot.StartedAt,
		FinishedAt:       snapshot.FinishedAt,
		StartedFormatted: startedFormatted,
		Duration:         duration,
		HasSession:       hasSession,
		Subdirectories:   subdirectories,
		SubdirectoryHint: subdirectoryHint,
	}
}

// sessionDuration renders the session's duration: the live elapsed time while
// the run is still in progress, or the total run time once it has finished.
// Before the first run there is no session and the value is empty.
func sessionDuration(snapshot executorSnapshot) string {
	if snapshot.StartedAt.IsZero() {
		return ""
	}
	var elapsed time.Duration
	if !snapshot.FinishedAt.IsZero() {
		elapsed = snapshot.FinishedAt.Sub(snapshot.StartedAt)
	} else {
		elapsed = time.Since(snapshot.StartedAt)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second).String()
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

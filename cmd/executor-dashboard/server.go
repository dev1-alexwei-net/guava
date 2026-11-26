package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/internal/version"
	"github.com/dev1-alexwei-net/guava/sessions"
	"github.com/dev1-alexwei-net/guava/tasks"
)

//go:embed templates/index.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var dashboardFiles embed.FS

type dashboard struct {
	controller   *executorController
	store        *sessions.Store
	page         *template.Template
	databasePath string
	routes       http.Handler
}

type dashboardPageData struct {
	Status            string
	Version           string
	Logs              string
	LastErr           string
	Error             string
	Refresh           int
	Theme             string
	NextTheme         string
	Running           bool
	Busy              bool
	AgentID           string
	CLIArgs           string
	Subdirectory      string
	StartedAt         time.Time
	FinishedAt        time.Time
	StartedFormatted  string
	Duration          string
	HasSession        bool
	Subdirectories    []string
	SubdirectoryHint  string
	SavedSessions     []savedSessionChoice
	SavedSessionsHint string
	Notice            string
}

// savedSessionChoice contains only the fields needed to identify and choose a
// saved run; filesystem paths and other launch configuration stay server-side.
type savedSessionChoice struct {
	ID           string
	AgentID      string
	Subdirectory string
	Status       sessions.Status
	StatusClass  string
	UpdatedAt    string
	CanResume    bool
	CanDelete    bool
}

func defaultSessionStorePath(databasePath string) string {
	return databasePath + ".sessions.sqlite"
}

func newDashboard(config executor.Config, logLevel slog.Level, sessionStorePath string) (*dashboard, error) {
	if sessionStorePath == "" {
		sessionStorePath = defaultSessionStorePath(config.DatabasePath)
	}
	if filepath.Clean(sessionStorePath) == filepath.Clean(config.DatabasePath) {
		return nil, fmt.Errorf("task session metadata database must be separate from the fileexplorer database")
	}
	store, err := sessions.OpenStore(sessionStorePath)
	if err != nil {
		return nil, err
	}
	if err := store.RecoverInterrupted(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	controller, err := newExecutorController(config, logLevel, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	page, err := template.ParseFS(dashboardFiles, "templates/index.html")
	if err != nil {
		controller.Close()
		_ = store.Close()
		return nil, err
	}

	app := &dashboard{
		controller:   controller,
		store:        store,
		page:         page,
		databasePath: config.DatabasePath,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.show)
	mux.HandleFunc("POST /start", app.start)
	mux.HandleFunc("POST /resume", app.resume)
	mux.HandleFunc("POST /sessions/delete", app.deleteSession)
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
	_ = d.store.Close()
}

func (d *dashboard) show(w http.ResponseWriter, r *http.Request) {
	subdirectories, subdirectoryErr := d.subdirectories(r.Context())
	if subdirectoryErr != nil {
		// The page still renders: the select falls back to the root option
		// only, and a hint explains the empty list.
		subdirectories = nil
	}
	data := pageData(d.controller.Snapshot(), r.URL.Query(), subdirectories, subdirectoryErr)
	savedSessions, sessionErr := d.savedSessions(r.Context())
	data.SavedSessions = savedSessions
	if sessionErr != nil {
		data.SavedSessionsHint = "Saved sessions are unavailable right now; you can still start a fresh session."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := d.page.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
	}
}

func (d *dashboard) savedSessions(ctx context.Context) ([]savedSessionChoice, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	records, err := d.store.List(ctx)
	if err != nil {
		return nil, err
	}
	choices := make([]savedSessionChoice, 0, len(records))
	for _, record := range records {
		statusClass := "text-bg-secondary"
		switch record.Status {
		case sessions.StatusActive:
			statusClass = "text-bg-primary"
		case sessions.StatusPaused:
			statusClass = "text-bg-warning"
		case sessions.StatusEnded:
			statusClass = "text-bg-success"
		case sessions.StatusFailed:
			statusClass = "text-bg-danger"
		}
		choices = append(choices, savedSessionChoice{
			ID:           record.ID,
			AgentID:      record.AgentID,
			Subdirectory: record.Subdirectory,
			Status:       record.Status,
			StatusClass:  statusClass,
			UpdatedAt:    record.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC"),
			CanResume:    record.Status != sessions.StatusActive,
			CanDelete:    record.Status != sessions.StatusActive,
		})
	}
	return choices, nil
}

// subdirectories reads the candidate subdirectories from the fileexplorer
// manifest, fresh on every page render so the listing stays current when the
// page refreshes. The database is opened read-only; a failed read is an
// error the caller degrades on rather than a page failure.
func (d *dashboard) subdirectories(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return tasks.ListSubdirectories(ctx, d.databasePath)
}

func pageData(snapshot executorSnapshot, query url.Values, subdirectories []string, subdirectoryErr error) dashboardPageData {
	refresh := parseRefresh(query.Get("refresh"))
	theme := parseTheme(query.Get("theme"))
	nextTheme := "dark"
	if theme == "dark" {
		nextTheme = "light"
	}
	// Start accepts a run synchronously before the executor goroutine sets
	// StartedAt. Treat any non-idle controller status as an accepted session
	// so the summary (including the run's selected subdirectory and CLI args)
	// is visible immediately after Start.
	hasSession := !snapshot.StartedAt.IsZero() || snapshot.Status != "idle"
	duration := sessionDuration(snapshot)

	var startedFormatted string
	if !snapshot.StartedAt.IsZero() {
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
		Notice:           noticeMessage(query.Get("notice")),
		AgentID:          snapshot.AgentID,
		CLIArgs:          snapshot.CLIArgs,
		Subdirectory:     snapshot.Subdirectory,
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
	var err error
	if sessionID := strings.TrimSpace(r.FormValue("session_id")); sessionID != "" {
		err = d.controller.Resume(sessionID)
	} else {
		var subdirectory string
		subdirectory, err = d.selectedSubdirectory(r)
		if err == nil {
			err = d.controller.Start(subdirectory)
		}
	}
	redirectDashboard(w, r, err)
}

// selectedSubdirectory reads the subdirectory the user selected in the
// Controls card and verifies it against the current fileexplorer manifest
// before any run starts. An empty selection means the whole root and needs
// no check. A non-empty selection must name one of the candidate
// subdirectories, so an unknown or stale choice — including one from an old
// page — is rejected with a user-friendly error instead of starting a run
// that would process nothing.
func (d *dashboard) selectedSubdirectory(r *http.Request) (string, error) {
	selection := strings.TrimSpace(r.FormValue("subdirectory"))
	if selection == "" {
		return "", nil
	}
	candidates, err := d.subdirectories(r.Context())
	if err != nil {
		return "", fmt.Errorf("cannot verify subdirectory %q: the file list is unavailable right now; refresh the page and try again", selection)
	}
	for _, candidate := range candidates {
		if candidate == selection {
			return selection, nil
		}
	}
	return "", fmt.Errorf("subdirectory %q is not in the file list; refresh the page and try again", selection)
}

func (d *dashboard) resume(w http.ResponseWriter, r *http.Request) {
	err := d.controller.Resume(r.FormValue("session_id"))
	redirectDashboard(w, r, err)
}

func (d *dashboard) deleteSession(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("confirm") != "yes" {
		redirectDashboard(w, r, fmt.Errorf("confirm permanent deletion of the saved session history"))
		return
	}
	if err := d.controller.DeleteSession(r.FormValue("session_id")); err != nil {
		redirectDashboard(w, r, err)
		return
	}
	query := redirectQuery(r)
	query.Set("notice", "session_deleted")
	respondRedirect(w, r, query)
}

func noticeMessage(value string) string {
	if value == "session_deleted" {
		return "Saved session history deleted."
	}
	return ""
}

func (d *dashboard) stop(w http.ResponseWriter, r *http.Request) {
	err := d.controller.Stop()
	redirectDashboard(w, r, err)
}

func redirectQuery(r *http.Request) url.Values {
	query := url.Values{}
	if refresh := parseRefresh(r.URL.Query().Get("refresh")); refresh > 0 {
		query.Set("refresh", strconv.Itoa(refresh))
	}
	if theme := parseTheme(r.URL.Query().Get("theme")); theme == "dark" {
		query.Set("theme", theme)
	}
	return query
}

func respondRedirect(w http.ResponseWriter, r *http.Request, query url.Values) {
	target := "/"
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func redirectDashboard(w http.ResponseWriter, r *http.Request, err error) {
	query := redirectQuery(r)
	if err != nil {
		query.Set("error", err.Error())
	}
	respondRedirect(w, r, query)
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

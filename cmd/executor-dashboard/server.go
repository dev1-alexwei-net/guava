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
	"sync"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/internal/version"
	"github.com/dev1-alexwei-net/guava/internal/web"
	"github.com/dev1-alexwei-net/guava/sessions"
	"github.com/dev1-alexwei-net/guava/tasks"
	"github.com/gin-gonic/gin"
)

//go:embed templates/index.html templates/settings.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var dashboardFiles embed.FS

type dashboard struct {
	mu               sync.Mutex
	controller       *executorController
	store            *sessions.Store
	account          sessions.AccountSettings
	config           executor.Config
	logLevel         slog.Level
	page             *template.Template
	databasePath     string
	sessionStorePath string
	routes           *gin.Engine
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
	ArmedDeleteID     string
	SavedSessionsHint string
	Notice            string
	PendingFiles      []pendingFileRow
}

type settingsPageData struct {
	Account               sessions.AccountSettings
	SuggestedInstructions string
	Theme                 string
	Error                 string
}

// pendingFileRow is one row of the dashboard's pending-files view: the
// file's path and whether it was carried over from an earlier run of the
// session or newly discovered in the current file list.
type pendingFileRow struct {
	Path    string
	Carried bool
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
	settings := sessions.DefaultAccountSettings()
	settings.DatabasePath = config.DatabasePath
	settings.RootPath = config.RootPath
	settings.FileExplorerURL = config.FileExplorerURL
	settings.GatewayURL = config.GatewayURL
	settings.AgentID = config.AgentID
	settings.Subdirectory = config.Subdirectory
	return newDashboardWithAccountSettings(config, logLevel, sessionStorePath, settings)
}

func newDashboardWithAccountSettings(config executor.Config, logLevel slog.Level, sessionStorePath string, initial sessions.AccountSettings, accountFlow ...bool) (*dashboard, error) {
	if sessionStorePath == "" {
		storeDatabasePath := config.DatabasePath
		if storeDatabasePath == "" {
			storeDatabasePath = sessions.DefaultAccountSettings().DatabasePath
		}
		sessionStorePath = defaultSessionStorePath(storeDatabasePath)
	}
	if filepath.Clean(sessionStorePath) == filepath.Clean(config.DatabasePath) {
		return nil, fmt.Errorf("task session metadata database must be separate from the fileexplorer database")
	}
	store, err := sessions.OpenStore(sessionStorePath)
	if err != nil {
		return nil, err
	}
	account, err := store.EnsureAccount(context.Background(), initial)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if filepath.Clean(sessionStorePath) == filepath.Clean(account.DatabasePath) {
		_ = store.Close()
		return nil, fmt.Errorf("task session metadata database must be separate from the fileexplorer database")
	}
	config.DatabasePath = account.DatabasePath
	config.RootPath = account.RootPath
	config.Subdirectory = account.Subdirectory
	config.FileExplorerURL = account.FileExplorerURL
	config.GatewayURL = account.GatewayURL
	config.AgentID = account.AgentID
	if err := store.RecoverInterrupted(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	controller, err := newExecutorController(config, logLevel, store, account.ID)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	page, err := template.ParseFS(dashboardFiles, "templates/index.html", "templates/settings.html")
	if err != nil {
		controller.Close()
		_ = store.Close()
		return nil, err
	}

	app := &dashboard{
		controller:       controller,
		store:            store,
		account:          account,
		config:           config,
		logLevel:         logLevel,
		page:             page,
		databasePath:     config.DatabasePath,
		sessionStorePath: sessionStorePath,
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = true
	router.Use(web.SecurityHeaders())
	showLanding := len(accountFlow) > 0 && accountFlow[0]
	landing := startupLandingHandler(app.show, showLanding)
	router.GET("/", landing)
	router.HEAD("/", landing)
	router.GET("/settings", app.showSettings)
	router.HEAD("/settings", app.showSettings)
	router.POST("/settings", app.saveSettings)
	router.POST("/start", app.start)
	router.POST("/resume", app.resume)
	router.POST("/sessions/delete", app.deleteSession)
	router.POST("/stop", app.stop)
	router.GET("/static/bootstrap.min.css", app.stylesheet)
	router.HEAD("/static/bootstrap.min.css", app.stylesheet)
	app.routes = router
	return app, nil
}

func (d *dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.routes.ServeHTTP(w, r)
}

func (d *dashboard) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.controller.Close()
	_ = d.store.Close()
}

func (d *dashboard) showSettings(c *gin.Context) {
	w, r := c.Writer, c.Request
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := settingsPageData{Account: d.account, Theme: parseTheme(web.QueryValue(c, "theme")), Error: web.QueryValue(c, "error")}
	suggested, err := tasks.LookupConstant(r.Context(), d.account.DatabasePath, conversations.InstructionsKey)
	if err != nil {
		data.Error = "Suggested instructions are unavailable: " + err.Error()
	} else {
		data.SuggestedInstructions = suggested
	}
	if err := d.page.ExecuteTemplate(w, "settings.html", data); err != nil {
		http.Error(w, "settings unavailable", http.StatusInternalServerError)
	}
}

func (d *dashboard) saveSettings(c *gin.Context) {
	w, r := c.Writer, c.Request
	settings, err := accountSettingsFromForm(r)
	if err == nil && web.FormValue(c, "reset_instructions") == "1" {
		settings.Instructions, err = tasks.LookupConstant(r.Context(), d.account.DatabasePath, conversations.InstructionsKey)
	}
	if err == nil && filepath.Clean(settings.DatabasePath) == filepath.Clean(d.sessionStorePath) {
		err = fmt.Errorf("the fileexplorer database must be separate from the task session metadata database")
	}
	if err != nil {
		redirectSettings(w, r, err)
		return
	}
	if d.controller.Snapshot().Status == "running" || d.controller.Snapshot().Status == "stopping" {
		redirectSettings(w, r, fmt.Errorf("stop the executor before changing account settings"))
		return
	}
	if accountSettingsEqual(d.account, settings) {
		respondRedirect(w, r, url.Values{"notice": {"settings_unchanged"}})
		return
	}
	config := d.config
	config.DatabasePath = settings.DatabasePath
	config.RootPath = settings.RootPath
	config.Subdirectory = settings.Subdirectory
	config.FileExplorerURL = settings.FileExplorerURL
	config.GatewayURL = settings.GatewayURL
	config.AgentID = settings.AgentID
	controller, err := newExecutorController(config, d.logLevel, d.store, d.account.ID)
	if err != nil {
		redirectSettings(w, r, err)
		return
	}
	updated, err := d.store.ReplaceAccount(r.Context(), d.account.ID, settings)
	if err != nil {
		controller.Close()
		redirectSettings(w, r, err)
		return
	}
	controller.sessionManager.accountID = updated.ID
	d.controller.Close()
	d.controller = controller
	d.account = updated
	d.config = config
	d.databasePath = updated.DatabasePath
	respondRedirect(w, r, url.Values{"notice": {"settings_saved"}})
}

func accountSettingsFromForm(r *http.Request) (sessions.AccountSettings, error) {
	settings := sessions.AccountSettings{
		Host: strings.TrimSpace(r.FormValue("host")), Port: strings.TrimSpace(r.FormValue("port")),
		DatabasePath: strings.TrimSpace(r.FormValue("database_path")), RootPath: strings.TrimSpace(r.FormValue("root_path")),
		FileExplorerURL: strings.TrimSpace(r.FormValue("fileexplorer_url")), GatewayURL: strings.TrimSpace(r.FormValue("gateway_url")),
		AgentID: strings.TrimSpace(r.FormValue("agent_id")), Subdirectory: strings.TrimSpace(r.FormValue("subdirectory")),
		Instructions: r.FormValue("instructions"), AppendNearestInstructions: r.FormValue("append_nearest_instructions") == "on",
	}
	if settings.Host == "" || settings.DatabasePath == "" || settings.RootPath == "" || settings.AgentID == "" {
		return settings, fmt.Errorf("host, database path, root path, and agent ID are required")
	}
	port, err := strconv.Atoi(settings.Port)
	if err != nil || port < 1 || port > 65535 {
		return settings, fmt.Errorf("port must be a number from 1 to 65535")
	}
	for label, raw := range map[string]string{"fileexplorer URL": settings.FileExplorerURL, "Gateway URL": settings.GatewayURL} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return settings, fmt.Errorf("%s must be an HTTP or HTTPS URL with a host", label)
		}
	}
	return settings, nil
}

func accountSettingsEqual(a, b sessions.AccountSettings) bool {
	return a.Host == b.Host && a.Port == b.Port && a.DatabasePath == b.DatabasePath && a.RootPath == b.RootPath &&
		a.FileExplorerURL == b.FileExplorerURL && a.GatewayURL == b.GatewayURL && a.AgentID == b.AgentID && a.Subdirectory == b.Subdirectory &&
		a.Instructions == b.Instructions && a.AppendNearestInstructions == b.AppendNearestInstructions
}

func redirectSettings(w http.ResponseWriter, r *http.Request, err error) {
	query := url.Values{}
	if theme := parseTheme(r.URL.Query().Get("theme")); theme == "dark" {
		query.Set("theme", theme)
	}
	if err != nil {
		query.Set("error", err.Error())
	}
	http.Redirect(w, r, "/settings?"+query.Encode(), http.StatusSeeOther)
}

func (d *dashboard) show(c *gin.Context) {
	w, r := c.Writer, c.Request
	subdirectories, subdirectoryErr := d.subdirectories(r.Context())
	if subdirectoryErr != nil {
		// The page still renders: the select falls back to the root option
		// only, and a hint explains the empty list.
		subdirectories = nil
	}
	snapshot := d.controller.Snapshot()
	query := r.URL.Query()
	data := pageData(snapshot, query, subdirectories, subdirectoryErr)
	savedSessions, sessionErr := d.savedSessions(r.Context())
	data.SavedSessions = savedSessions
	if sessionErr != nil {
		data.SavedSessionsHint = "Saved sessions are unavailable right now; you can still start a fresh session."
	}
	data.PendingFiles = d.pendingFiles(r.Context(), snapshot.SessionID)
	// Deletion is two-step: a GET arms the target session's "Confirm
	// permanent deletion" action, and a second click performs the POST.
	data.ArmedDeleteID = strings.TrimSpace(web.QueryValue(c, "arming"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := d.page.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
	}
}

// pendingFiles reads the session's unfinished file set for the dashboard's
// pending-files view. A failed read leaves the list empty rather than
// failing the page; the rest of the dashboard keeps rendering.
func (d *dashboard) pendingFiles(ctx context.Context, sessionID string) []pendingFileRow {
	if sessionID == "" {
		return nil
	}
	files, err := d.store.PendingProgress(ctx, sessionID)
	if err != nil {
		return nil
	}
	rows := make([]pendingFileRow, 0, len(files))
	for _, file := range files {
		rows = append(rows, pendingFileRow{Path: file.Path, Carried: file.Origin == sessions.OriginCarried})
	}
	return rows
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

func (d *dashboard) stylesheet(c *gin.Context) {
	w := c.Writer
	css, err := dashboardFiles.ReadFile("static/bootstrap.min.css")
	if err != nil {
		http.Error(w, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(css)
}

func (d *dashboard) start(c *gin.Context) {
	w, r := c.Writer, c.Request
	var err error
	if sessionID := strings.TrimSpace(web.FormValue(c, "session_id")); sessionID != "" {
		err = d.controller.Resume(sessionID)
	} else {
		var subdirectory string
		subdirectory, err = d.selectedSubdirectory(c)
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
func (d *dashboard) selectedSubdirectory(c *gin.Context) (string, error) {
	selection := strings.TrimSpace(web.FormValue(c, "subdirectory"))
	if selection == "" {
		return "", nil
	}
	candidates, err := d.subdirectories(c.Request.Context())
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

func (d *dashboard) resume(c *gin.Context) {
	w, r := c.Writer, c.Request
	err := d.controller.Resume(web.FormValue(c, "session_id"))
	redirectDashboard(w, r, err)
}

func (d *dashboard) deleteSession(c *gin.Context) {
	w, r := c.Writer, c.Request
	if web.FormValue(c, "confirm") != "yes" {
		redirectDashboard(w, r, fmt.Errorf("confirm permanent deletion of the saved session history"))
		return
	}
	if err := d.controller.DeleteSession(web.FormValue(c, "session_id")); err != nil {
		redirectDashboard(w, r, err)
		return
	}
	query := redirectQuery(r)
	query.Set("notice", "session_deleted")
	respondRedirect(w, r, query)
}

func noticeMessage(value string) string {
	switch value {
	case "session_deleted":
		return "Saved session history deleted."
	case "settings_saved":
		return "Account settings saved. Restart the dashboard for host or port changes to take effect."
	case "settings_unchanged":
		return "No account settings changed."
	default:
		return ""
	}
}

func (d *dashboard) stop(c *gin.Context) {
	w, r := c.Writer, c.Request
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

package main

import (
	"errors"
	"net/http"
	"strings"
	"sync"
)

// application composes the filesystem, persistence, synchronization, mutation,
// presentation, and HTTP routing dependencies for one explorer instance.
type application struct {
	filesystem *rootedFS
	store      *Store
	syncer     *synchronizer
	mutations  *mutations
	renderer   *renderer
	routes     http.Handler
	mu         sync.Mutex
}

// newApplication wires one explorer from explicit dependencies.
func newApplication(filesystem *rootedFS, store *Store, syncer *synchronizer, mutations *mutations, renderer *renderer) (*application, error) {
	switch {
	case filesystem == nil:
		return nil, errors.New("rooted filesystem is required")
	case store == nil:
		return nil, errors.New("store is required")
	case syncer == nil:
		return nil, errors.New("synchronizer is required")
	case mutations == nil:
		return nil, errors.New("mutations are required")
	case renderer == nil:
		return nil, errors.New("renderer is required")
	}
	app := &application{
		filesystem: filesystem,
		store:      store,
		syncer:     syncer,
		mutations:  mutations,
		renderer:   renderer,
	}
	app.routes = app.buildRoutes()
	return app, nil
}

// newHandler adapts a root path and store to the explicit application constructor.
func newHandler(root string, store *Store) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	filesystem, err := openRootedFS(root)
	if err != nil {
		return nil, err
	}
	app, err := newApplication(filesystem, store, newSynchronizer(store, filesystem), newMutations(filesystem), newRenderer())
	if err != nil {
		_ = filesystem.Close()
		return nil, err
	}
	return app.Handler(), nil
}

// Handler returns the application's traversal guard and route tree.
func (a *application) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestHasTraversal(r.URL.Path) {
			a.renderError(w, http.StatusBadRequest, "Invalid path")
			return
		}
		a.routes.ServeHTTP(w, r)
	})
}

// buildRoutes is the single route table for the file explorer.
func (a *application) buildRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", a.serialized(a.rootPage))
	mux.Handle("GET /d/{$}", a.serialized(a.rootPage))
	mux.Handle("GET /d/{path...}", a.serialized(a.directoryPage))
	mux.Handle("GET /f/{path...}", a.serialized(a.filePage))
	mux.Handle("GET /edit/{path...}", a.serialized(a.editorPage))
	mux.Handle("POST /edit/{path...}", a.serialized(a.saveFile))
	mux.Handle("GET /confirm-delete/{path...}", a.serialized(a.confirmDeletePage))
	mux.Handle("GET /preview/{path...}", a.serialized(a.previewPage))
	mux.HandleFunc("GET /static/bootstrap.min.css", a.renderer.Stylesheet)
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		mux.Handle(method+" /mkdir", a.serialized(a.mkdir))
		mux.Handle(method+" /create-file", a.serialized(a.createFile))
		mux.Handle(method+" /rename", a.serialized(a.rename))
		mux.Handle(method+" /delete", a.serialized(a.delete))
		mux.Handle(method+" /upload", a.serialized(a.upload))
	}
	return mux
}

func requestHasTraversal(requestPath string) bool {
	for _, component := range strings.Split(strings.ReplaceAll(requestPath, `\`, "/"), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"plugin"
	"strings"
	"sync"

	"log/slog"

	"github.com/clavinjune/plugin-demo/view"
)

type (
	Context struct {
		context.Context
		Mutex  *sync.RWMutex
		Plugin http.Handler
	}
)

func main() {
	slog.Info("listening",
		slog.Int("port", 8000),
	)

	handler := pluginHandler(&Context{
		Context: context.Background(),
		Mutex:   &sync.RWMutex{},
		Plugin:  http.NotFoundHandler(),
	})

	http.Handle("/", http.FileServer(http.FS(view.FS)))
	http.Handle("/plugins", handler)
	http.ListenAndServe(":8000", nil)
}

func pluginHandler(ctx *Context) http.HandlerFunc {
	fetch := fetchHandler(ctx)
	store := storeHandler(ctx)

	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fetch(w, r)
		case http.MethodPost:
			store(w, r)
		default:
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		}
	}
}

func fetchHandler(ctx *Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx.Mutex.RLock()
		defer ctx.Mutex.RUnlock()

		ctx.Plugin.ServeHTTP(w, r)
	}
}

func storeHandler(ctx *Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		code := strings.TrimSpace(r.FormValue("code"))
		p, err := buildCode(ctx.Context, code)
		if err != nil {
			slog.Error(err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		h, err := lookupHandler(ctx.Context, p)
		if err != nil {
			slog.Error(err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ctx.Mutex.Lock()
		ctx.Plugin = h
		ctx.Mutex.Unlock()

		w.WriteHeader(http.StatusNoContent)
	}
}

func buildCode(ctx context.Context, code string) (*plugin.Plugin, error) {
	f, err := os.CreateTemp("", "plugin-demo*.go")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	defer os.Remove(f.Name())

	pluginName := fmt.Sprintf("%s.so", f.Name())
	defer os.Remove(pluginName)

	slog.Info("creating temp file",
		slog.String("filename", f.Name()),
	)

	if _, err := f.WriteString(code); err != nil {
		return nil, err
	}
	f.Close()

	slog.Info("building plugin",
		slog.String("filename", pluginName),
	)
	if _, err := exec.CommandContext(context.Background(), "go",
		"build",
		"-buildmode=plugin",
		`-ldflags=-s -w`,
		"-o",
		pluginName,
		f.Name(),
	).Output(); err != nil {
		return nil, err
	}

	return plugin.Open(pluginName)
}

func lookupHandler(ctx context.Context, p *plugin.Plugin) (http.HandlerFunc, error) {
	slog.Info("looking up Handler symbol inside plugin")
	s, err := p.Lookup("Handler")
	if err != nil {
		return nil, err
	}

	if h, ok := s.(*http.HandlerFunc); ok {
		return *h, nil
	}
	if h, ok := s.(http.HandlerFunc); ok {
		return h, nil
	}

	if h, ok := s.(func(http.ResponseWriter, *http.Request)); ok {
		return http.HandlerFunc(h), nil
	}

	return http.NotFound, errors.New("unhandled case")
}

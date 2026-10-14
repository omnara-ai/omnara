package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type ReadyFunc func(context.Context) error

func ServerHandler(set *Set, ready ReadyFunc) http.Handler {
	startedAt := time.Now().UTC()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusOK, map[string]string{
			"status":     "ok",
			"started_at": startedAt.Format(time.RFC3339Nano),
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				writeStatus(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
				return
			}
		}
		writeStatus(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("GET "+ScrapePath, set.Handler())
	return mux
}

func Serve(ctx context.Context, log *slog.Logger, addr string, set *Set, ready ReadyFunc) <-chan error {
	errc := make(chan error, 1)
	server := &http.Server{
		Addr:              addr,
		Handler:           ServerHandler(set, ready),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Info("metrics listening", "addr", addr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	go func() {
		select {
		case err := <-serveErr:
			errc <- err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				errc <- err
				return
			}
			errc <- <-serveErr
		}
	}()
	return errc
}

func writeStatus(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errchkjson // Status is already committed; client write errors are not actionable here.
	_ = json.NewEncoder(w).Encode(body)
}

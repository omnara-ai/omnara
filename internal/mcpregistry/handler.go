package mcpregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	ServersPath    = "/v1/servers"
	QueryParam     = "q"
	LimitParam     = "limit"
	CursorParam    = "cursor"
	RemoteURLParam = "remote_url"
	requestTimeout = 10 * time.Second
)

type errorBody struct {
	Error string `json:"error"`
}

func NewHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "database unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET "+ServersPath, func(w http.ResponseWriter, r *http.Request) {
		params, err := parseSearchParams(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		page, err := store.Search(ctx, params)
		if errors.Is(err, ErrInvalidCursor) {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody{Error: "search failed"})
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	return mux
}

func parseSearchParams(r *http.Request) (SearchParams, error) {
	values := r.URL.Query()
	params := SearchParams{
		Query:     values.Get(QueryParam),
		RemoteURL: values.Get(RemoteURLParam),
		Cursor:    values.Get(CursorParam),
	}
	if raw := values.Get(LimitParam); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxSearchLimit {
			return SearchParams{}, errors.New("limit must be an integer between 1 and " + strconv.Itoa(MaxSearchLimit))
		}
		params.Limit = limit
	}
	return params, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		return
	}
}

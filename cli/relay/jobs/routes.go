package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Router registers authenticated admin routes.
type Router interface {
	Handle(pattern string, h http.HandlerFunc)
}

// RebuildRequest is the body of POST /admin/api/indexes/{name}/rebuild.
type RebuildRequest struct {
	Fresh    bool `json:"fresh"`
	Reverify bool `json:"reverify"`
	DryRun   bool `json:"dry_run"`
}

// dropWait is how long a drop request waits for its job before answering
// 202 with it still running.
const dropWait = 30 * time.Second

// Routes registers the index and job API on r.
func (m *Manager) Routes(r Router) {
	r.Handle("GET /admin/api/indexes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"indexes": m.Indexes()})
	})

	r.Handle("POST /admin/api/indexes/{name}/rebuild", func(w http.ResponseWriter, req *http.Request) {
		var body RebuildRequest
		if data, _ := io.ReadAll(req.Body); len(data) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
				return
			}
		}
		name := req.PathValue("name")
		if body.DryRun {
			n, err := m.Estimate(name)
			if err != nil {
				writeStartError(w, err, nil)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"index": name, "estimate": n, "dry_run": true})
			return
		}
		job, started, err := m.Start(name, KindRebuild, Options{Fresh: body.Fresh, Reverify: body.Reverify})
		if err != nil {
			writeStartError(w, err, &job)
			return
		}
		status := http.StatusOK
		if started {
			status = http.StatusAccepted
		}
		writeJSON(w, status, job)
	})

	r.Handle("POST /admin/api/indexes/{name}/drop", func(w http.ResponseWriter, req *http.Request) {
		job, _, err := m.Start(req.PathValue("name"), KindDrop, Options{})
		if err != nil {
			writeStartError(w, err, &job)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), dropWait)
		defer cancel()
		for job.State == StateRunning && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
			if j, err := m.Get(job.ID); err == nil {
				job = j
			}
		}
		status := http.StatusOK
		if job.State == StateRunning {
			status = http.StatusAccepted
		}
		writeJSON(w, status, job)
	})

	r.Handle("GET /admin/api/jobs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": m.List()})
	})

	r.Handle("GET /admin/api/jobs/{id}", func(w http.ResponseWriter, req *http.Request) {
		job, err := m.Get(req.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	r.Handle("DELETE /admin/api/jobs/{id}", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), dropWait)
		defer cancel()
		job, err := m.Cancel(ctx, req.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
}

func writeStartError(w http.ResponseWriter, err error, job *Job) {
	switch {
	case errors.Is(err, ErrUnknownIndex):
		writeError(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, ErrUnavailable):
		// The relay's own convention for "this feature isn't turned on".
		writeError(w, http.StatusNotImplemented, err.Error(), nil)
	case errors.Is(err, ErrBusy):
		writeError(w, http.StatusConflict, err.Error(), job)
	default:
		writeError(w, http.StatusInternalServerError, err.Error(), nil)
	}
}

func writeError(w http.ResponseWriter, status int, msg string, job *Job) {
	body := map[string]any{"error": msg}
	if job != nil && job.ID != "" {
		body["job"] = job
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

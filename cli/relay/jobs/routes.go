package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Router registers authenticated admin routes.
type Router interface {
	Handle(pattern string, h http.HandlerFunc)
}

// IndexInfo is an index as listed: its status, the job running on it and
// the last one that ended.
type IndexInfo struct {
	Status
	Running *Job `json:"running,omitempty"`
	Last    *Job `json:"last,omitempty"`
	// RetryAt is when a failed job is retried (unix), 0 when none waits.
	RetryAt int64 `json:"retry_at,omitempty"`
}

// Plan is what a rebuild or drop would do, for a dry run.
type Plan struct {
	Index  string `json:"index"`
	Kind   Kind   `json:"kind"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Entries is what the index holds now; Scan is the stored events a
	// rebuild reads.
	Entries int64 `json:"entries"`
	Scan    int64 `json:"scan"`
	// Effect says, in a line, what happens to readers meanwhile.
	Effect string `json:"effect"`
	DryRun bool   `json:"dry_run"`
}

// Indexes lists every index.
func (m *Manager) Indexes(r *Reconciler) []IndexInfo {
	out := make([]IndexInfo, 0, len(m.order))
	for _, name := range m.order {
		info := IndexInfo{Status: m.indexes[name].Status(), Running: m.RunningJob(name), Last: m.LastShown(name)}
		if j := info.Running; j != nil {
			switch j.Kind {
			case KindRebuild:
				info.State = StateBuilding
			case KindCatchUp:
				info.State = StateCatchingUp
			case KindRescore:
				info.State = StateRescoring
			case KindDrop:
				info.State = StateDropping
			}
		}
		if r != nil {
			if t := r.RetryAt(name); !t.IsZero() {
				info.RetryAt = t.Unix()
			}
		}
		out = append(out, info)
	}
	return out
}

// Plan says what kind would do on index, without doing it.
func (m *Manager) Plan(index string, kind Kind) (Plan, error) {
	ix, err := m.Lookup(index)
	if err != nil {
		return Plan{}, err
	}
	st := ix.Status()
	p := Plan{Index: index, Kind: kind, State: st.State, Reason: st.Reason, Entries: st.Entries, Scan: st.Head, DryRun: true}
	switch {
	case kind == KindDrop && st.Enabled:
		return p, fmt.Errorf("%w: the %s index is on; rebuild it instead (ncli relay index rebuild %s)", ErrEnabled, index, index)
	case kind == KindRebuild && !st.Enabled:
		return p, fmt.Errorf("%w: %s", ErrUnavailable, st.Unavailable)
	case kind == KindDrop:
		p.Scan = 0
		p.Effect = fmt.Sprintf("removes the %s entries it holds; it is rebuilt from the stored events if turned on again", Count(st.Entries))
	case !ix.WritesStore():
		p.Effect = "builds a new index beside the live one, which keeps answering, then swaps it in"
	default:
		p.Effect = "builds a new index beside the live one, which keeps answering, then swaps it in; writes are never held up"
	}
	return p, nil
}

// Routes registers the index and job API on r.
func (m *Manager) Routes(router Router, rec *Reconciler) {
	router.Handle("GET /admin/api/indexes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"indexes": m.Indexes(rec)})
	})

	action := func(kind Kind) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				DryRun bool `json:"dry_run"`
				Fresh  bool `json:"fresh"`
			}
			if data, _ := io.ReadAll(req.Body); len(data) > 0 {
				if err := json.Unmarshal(data, &body); err != nil {
					writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
					return
				}
			}
			if body.Fresh {
				writeError(w, http.StatusBadRequest, "fresh was removed: a rebuild always builds a new index and swaps it in", nil)
				return
			}
			name := req.PathValue("name")
			if body.DryRun {
				p, err := m.Plan(name, kind)
				if err != nil {
					writeStartError(w, err, nil)
					return
				}
				writeJSON(w, http.StatusOK, p)
				return
			}
			job, err := m.Start(name, kind, TriggerAdmin, "")
			if errors.Is(err, ErrNothingToDo) {
				writeJSON(w, http.StatusOK, job)
				return
			}
			if err != nil {
				writeStartError(w, err, &job)
				return
			}
			if rec != nil {
				defer rec.Nudge()
			}
			status := http.StatusAccepted
			if job.Noop {
				status = http.StatusOK
			}
			writeJSON(w, status, job)
		}
	}
	router.Handle("POST /admin/api/indexes/{name}/rebuild", action(KindRebuild))
	router.Handle("POST /admin/api/indexes/{name}/drop", action(KindDrop))

	router.Handle("GET /admin/api/jobs", func(w http.ResponseWriter, req *http.Request) {
		index := req.URL.Query().Get("index")
		if index != "" {
			if _, err := m.Lookup(index); err != nil {
				writeError(w, http.StatusNotFound, err.Error(), nil)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": m.List(index)})
	})

	router.Handle("GET /admin/api/jobs/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		// ?wait=<seconds> holds the answer until the job ends or the wait runs out.
		if s := req.URL.Query().Get("wait"); s != "" {
			d, err := time.ParseDuration(s + "s")
			if err != nil || d < 0 {
				writeError(w, http.StatusBadRequest, "invalid wait: "+s, nil)
				return
			}
			ctx, cancel := context.WithTimeout(req.Context(), min(d, 60*time.Second))
			defer cancel()
			job, err := m.Wait(ctx, id)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error(), nil)
				return
			}
			writeJSON(w, http.StatusOK, job)
			return
		}
		job, err := m.Get(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	router.Handle("DELETE /admin/api/jobs/{id}", func(w http.ResponseWriter, req *http.Request) {
		job, err := m.Cancel(req.PathValue("id"))
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error(), nil)
		case errors.Is(err, ErrNotCancellable):
			writeError(w, http.StatusUnprocessableEntity, err.Error(), &job)
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error(), nil)
		case job.Noop:
			writeJSON(w, http.StatusOK, job)
		default:
			writeJSON(w, http.StatusAccepted, job)
		}
	})
}

func writeStartError(w http.ResponseWriter, err error, job *Job) {
	switch {
	case errors.Is(err, ErrUnknownIndex):
		writeError(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, ErrUnavailable):
		// The relay's own convention for "this feature isn't turned on".
		writeError(w, http.StatusNotImplemented, err.Error(), nil)
	case errors.Is(err, ErrEnabled):
		writeError(w, http.StatusBadRequest, err.Error(), nil)
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

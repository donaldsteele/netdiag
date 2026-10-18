package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"netdiag/internal/instance"
	"netdiag/internal/model"
	"netdiag/internal/report"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Interfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req model.ScanRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sc, err := s.svc.Start(req.WithOrigin(model.OriginUI))
	var watching *scan.WatchingError
	switch {
	case errors.As(err, &watching):
		// A scan is watching the network: it goes on, and the page asks the
		// person before it sends the request again with replaceLive.
		writeJSON(w, http.StatusConflict, map[string]any{"error": "netdiag is watching this network.", "watching": watching})
	case errors.Is(err, scan.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, scan.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"id": sc.ID})
	}
}

// handleRunning reports the scan that is running now, or {"scan":null}, so a
// reloaded or reopened page can attach to it again.
func (s *Server) handleRunning(w http.ResponseWriter, r *http.Request) {
	run, ok := s.svc.Running()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"scan": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan": run})
}

// identity is the GET /api/identity answer. The page polls it as a
// heartbeat: no answer means netdiag has stopped, another instanceId that
// it was restarted. A second launch asks it too, with ?nonce=: the same
// instanceId as in instance.json, and a proof made with the token in that
// file, mean this netdiag still serves the data folder (v0.6-D33). The
// proof does not give the token away.
type identity = instance.Identity

func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := identity{App: instance.App, Version: s.version(), PID: os.Getpid(),
		InstanceID: s.instanceID, StartedAt: s.startedAt}
	if n := r.URL.Query().Get("nonce"); n != "" && len(n) <= instance.MaxNonce {
		id.Proof = instance.Proof(s.token, n)
	}
	writeJSON(w, http.StatusOK, id)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.svc.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	writeJSON(w, http.StatusOK, model.RedactScan(sc.Snapshot()))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if !s.svc.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleExport downloads a scan as JSON or CSV. The scan may be running, just
// finished, or saved in History by an earlier run.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be json or csv")
		return
	}
	res, err := s.svc.ResolveScan(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not read the saved scan: "+err.Error())
		return
	}
	res = model.RedactScan(res)
	var buf bytes.Buffer
	var ctype string
	if format == "json" {
		ctype, err = "application/json", report.WriteJSON(&buf, res)
	} else {
		ctype, err = "text/csv; charset=utf-8", report.WriteCSV(&buf, res)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := "netdiag-" + time.Now().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	s.quitOnce.Do(func() { close(s.quit) })
}

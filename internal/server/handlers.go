package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/report"
	"netdiag/internal/scan"
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
	list, err := s.cfg.Interfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []netinfo.Iface{}
	}
	for i := range list {
		list[i].Suggested, _ = netinfo.SuggestTarget(list[i])
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
	sc, err := s.cfg.Registry.Start(s.baseCtx, req)
	switch {
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

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.cfg.Registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	writeJSON(w, http.StatusOK, sc.Snapshot())
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Registry.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.cfg.Registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	format := r.URL.Query().Get("format")
	var buf bytes.Buffer
	var ctype string
	var err error
	switch format {
	case "json":
		ctype, err = "application/json", report.WriteJSON(&buf, sc.Snapshot())
	case "csv":
		ctype, err = "text/csv; charset=utf-8", report.WriteCSV(&buf, sc.Snapshot())
	default:
		writeError(w, http.StatusBadRequest, "format must be json or csv")
		return
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

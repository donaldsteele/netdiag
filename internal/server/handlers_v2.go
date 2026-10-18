package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"netdiag/internal/diff"
	"netdiag/internal/model"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

func (s *Server) needStore(w http.ResponseWriter) bool {
	if !s.svc.HasStore() {
		writeError(w, http.StatusServiceUnavailable, "data store unavailable")
		return false
	}
	return true
}

// svcErr maps a service error to its HTTP response.
func (s *Server) svcErr(w http.ResponseWriter, err error) {
	var bad *service.BadRequestError
	var nf *service.NotFoundError
	switch {
	case errors.Is(err, service.ErrNoStore):
		writeError(w, http.StatusServiceUnavailable, "data store unavailable")
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Msg)
	case errors.As(err, &nf):
		writeError(w, http.StatusNotFound, nf.Msg)
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	set, repaired, err := s.svc.LoadSettingsRepaired()
	if err != nil {
		s.svcErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(set, repaired))
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	var in model.Settings
	if !decodeBody(w, r, &in) {
		return
	}
	out, err := s.svc.SaveSettings(in)
	if err != nil {
		s.svcErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(out, nil))
}

func (s *Server) handleIgnore(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	var req struct {
		MAC string `json:"mac"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.svc.IgnoreMAC(req.MAC); err != nil {
		s.svcErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	best, ok := s.svc.LatestHealth()
	if !ok {
		writeError(w, http.StatusNotFound, "no health report yet")
		return
	}
	writeJSON(w, http.StatusOK, best)
}

// handleProblems is the Problems right now card (v0.6 #1): what is wrong
// now, or that everything looks fine, and what was fixed today. It needs no
// session token, like the scan and health reads.
func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Problems())
}

func (s *Server) handleHealthRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.svc.RunHealth(r.Context(), req.Interface))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListHistory()
	if err != nil {
		s.svcErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown history id")
		return
	}
	s.svcErr(w, err)
}

func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.LoadScan(r.PathValue("id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteScan(r.PathValue("id")); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// buildDiff compares the scans named in the query. On failure it has already
// written the error response.
func (s *Server) buildDiff(w http.ResponseWriter, r *http.Request) (model.Diff, bool) {
	q := r.URL.Query()
	d, err := s.svc.Compare(q.Get("base"), q.Get("current"))
	if err != nil {
		s.svcErr(w, err)
		return model.Diff{}, false
	}
	return d, true
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.buildDiff(w, r); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) handleCompareExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be json or csv")
		return
	}
	d, ok := s.buildDiff(w, r)
	if !ok {
		return
	}
	var buf bytes.Buffer
	var ctype string
	var err error
	if format == "json" {
		ctype, err = "application/json", diff.WriteJSON(&buf, d)
	} else {
		ctype, err = "text/csv; charset=utf-8", diff.WriteCSV(&buf, d)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := "netdiag-diff-" + time.Now().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(buf.Bytes())
}

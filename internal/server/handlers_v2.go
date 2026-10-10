package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"netdiag/internal/diff"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/store"
)

const healthRunCap = 10 * time.Second

func (s *Server) needStore(w http.ResponseWriter) bool {
	if s.cfg.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "data store unavailable")
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	set, err := s.cfg.Store.LoadSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// saveSettings validates, persists and live-applies settings.
func (s *Server) saveSettings(w http.ResponseWriter, in model.Settings) (model.Settings, bool) {
	norm, err := store.NormalizeSettings(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return norm, false
	}
	out, err := s.cfg.Store.SaveSettings(norm)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return out, false
	}
	if sc, ok := s.cfg.Registry.Running(); ok {
		sc.ApplySettings(out)
	}
	return out, true
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	var in model.Settings
	if !decodeBody(w, r, &in) {
		return
	}
	if out, ok := s.saveSettings(w, in); ok {
		writeJSON(w, http.StatusOK, out)
	}
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
	hw, err := net.ParseMAC(strings.TrimSpace(req.MAC))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid mac")
		return
	}
	mac := strings.ToUpper(hw.String())
	set, err := s.cfg.Store.LoadSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	for _, m := range set.IgnoredConflictMACs {
		if strings.EqualFold(m, mac) {
			found = true
		}
	}
	if !found {
		set.IgnoredConflictMACs = append(set.IgnoredConflictMACs, mac)
	}
	if _, ok := s.saveSettings(w, set); ok {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var best *model.HealthReport
	consider := func(h *model.HealthReport) {
		if h != nil && (best == nil || h.CheckedAt > best.CheckedAt) {
			best = h
		}
	}
	if sc, ok := s.cfg.Registry.Running(); ok {
		consider(sc.Snapshot().Health)
	}
	if best == nil {
		for _, sc := range s.cfg.Registry.List() {
			consider(sc.Snapshot().Health)
		}
	}
	if best == nil && s.cfg.Store != nil {
		list, _ := s.cfg.Store.ListHistory()
		for _, e := range list {
			if res, err := s.cfg.Store.LoadScan(e.ID); err == nil && res.Health != nil {
				best = res.Health
				break
			}
		}
	}
	if best == nil {
		writeError(w, http.StatusNotFound, "no health report yet")
		return
	}
	writeJSON(w, http.StatusOK, best)
}

func (s *Server) handleHealthRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	in := health.Input{}
	if list, err := s.cfg.Interfaces(); err == nil && len(list) > 0 {
		f := list[0]
		for _, c := range list {
			if req.Interface != "" && c.Name == req.Interface {
				f = c
				break
			}
		}
		in.Iface, in.IfaceIP = f.Name, f.IP
	}
	if gw, err := s.cfg.Gateway(); err == nil {
		in.Gateway = gw
	}
	if s.cfg.Store != nil {
		if set, err := s.cfg.Store.LoadSettings(); err == nil {
			in.Config = health.Config{WebURL: set.WebServer.URL, MustContain: set.WebServer.MustContain,
				ExpectStatus: set.WebServer.ExpectStatus, InternetURL: set.InternetCheckURL}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), healthRunCap)
	defer cancel()
	writeJSON(w, http.StatusOK, s.cfg.Health.Run(ctx, in))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	list, err := s.cfg.Store.ListHistory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []model.HistoryEntry{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown history id")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	res, err := s.cfg.Store.LoadScan(r.PathValue("id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	if !s.needStore(w) {
		return
	}
	if err := s.cfg.Store.DeleteScan(r.PathValue("id")); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveScan finds a scan by id in the live registry, then in history.
func (s *Server) resolveScan(id string) (model.ScanResult, error) {
	if sc, ok := s.cfg.Registry.Get(id); ok {
		return sc.Snapshot(), nil
	}
	if s.cfg.Store == nil {
		return model.ScanResult{}, store.ErrNotFound
	}
	return s.cfg.Store.LoadScan(id)
}

// buildDiff resolves base/current from the query and compares them. On
// failure it has already written the error response.
func (s *Server) buildDiff(w http.ResponseWriter, r *http.Request) (model.Diff, bool) {
	q := r.URL.Query()
	curID, baseID := q.Get("current"), q.Get("base")
	if curID == "" {
		writeError(w, http.StatusBadRequest, "current is required")
		return model.Diff{}, false
	}
	cur, err := s.resolveScan(curID)
	if err != nil {
		s.compareErr(w, err, "current scan not found")
		return model.Diff{}, false
	}
	if baseID == "" {
		if s.cfg.Store == nil {
			writeError(w, http.StatusNotFound, "no baseline available")
			return model.Diff{}, false
		}
		e, ok, err := s.cfg.Store.Previous(cur.Request.CIDR, cur.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return model.Diff{}, false
		}
		if !ok {
			writeError(w, http.StatusNotFound, "no previous scan of "+cur.Request.CIDR)
			return model.Diff{}, false
		}
		baseID = e.ID
	}
	if baseID == curID || baseID == cur.ID {
		writeError(w, http.StatusBadRequest, "base and current must differ")
		return model.Diff{}, false
	}
	base, err := s.resolveScan(baseID)
	if err != nil {
		s.compareErr(w, err, "base scan not found")
		return model.Diff{}, false
	}
	return diff.Compare(base, cur), true
}

func (s *Server) compareErr(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, msg)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
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

// defaultGateway is the production Config.Gateway.
func defaultGateway() (string, error) {
	r, err := netinfo.DefaultRoute()
	if err != nil {
		return "", err
	}
	if !r.Gateway.IsValid() {
		return "", errors.New("no default gateway")
	}
	return r.Gateway.String(), nil
}

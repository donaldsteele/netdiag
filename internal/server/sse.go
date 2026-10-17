package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"netdiag/internal/model"
)

// handleEvents streams a scan's events as server-sent events. The bus replays
// a snapshot to new subscribers, so late connections see current state first.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.svc.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown scan")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	events, unsub := sc.Subscribe()
	defer unsub()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	tick := time.NewTicker(s.keepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case ev, open := <-events:
			if !open {
				return
			}
			if err := writeEvent(w, ev); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// writeEvent writes one SSE frame. Snapshot and done events carry the whole
// scan result, so the SNMP community is redacted first.
func writeEvent(w http.ResponseWriter, ev model.Event) error {
	switch d := ev.Data.(type) {
	case model.ScanResult:
		ev.Data = model.RedactScan(d)
	case *model.ScanResult:
		if d != nil {
			ev.Data = model.RedactScan(*d)
		}
	}
	b, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
	return err
}

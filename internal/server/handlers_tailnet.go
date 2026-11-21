package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"netdiag/internal/service"
)

// handleTailnet is GET /api/tailnet (v0.6.1): this computer's Tailscale
// state, its tailnet's devices and the Tailscale card. It needs no session
// token, like the other reads; it carries no email address (the tailnet is
// named by its MagicDNS suffix).
func (s *Server) handleTailnet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Tailnet(r.Context()))
}

// handleTailnetPing is POST /api/tailnet/ping {ip} (v0.6.1): a Tailscale
// ping to one tailnet device, at most every 5 s per device.
func (s *Server) handleTailnetPing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.IP == "" {
		writeError(w, http.StatusBadRequest, "send the device's Tailscale address as {\"ip\": ...}")
		return
	}
	res, err := s.svc.PingTailnet(r.Context(), req.IP)
	switch {
	case errors.Is(err, service.ErrTailnetPingTooSoon):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, service.ErrNotTailscaleAddr):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

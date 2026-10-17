package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"netdiag/internal/model"
	"netdiag/internal/remote"
)

func (s *Server) handleRemoteCaps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Remote.Caps())
}

// remoteTarget finds ip in scan id and checks that the port of kind k was
// found open there, so only devices netdiag actually saw can be opened.
func (s *Server) remoteTarget(w http.ResponseWriter, id, ip string, k remote.Kind) (port int, ok bool) {
	sc, found := s.svc.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "unknown scan")
		return 0, false
	}
	port = remote.Port(k)
	if port == 0 {
		writeError(w, http.StatusBadRequest, "kind must be rdp or vnc")
		return 0, false
	}
	if a, err := netip.ParseAddr(ip); err != nil || !remote.Local(a) {
		writeError(w, http.StatusBadRequest, ip+" is not on a local network")
		return 0, false
	}
	hosts := sc.Snapshot().Hosts
	i := slices.IndexFunc(hosts, func(h model.Host) bool { return h.IP == ip })
	if i < 0 {
		writeError(w, http.StatusNotFound, "no device "+ip+" in this scan")
		return 0, false
	}
	if hosts[i].IsSelf {
		writeError(w, http.StatusBadRequest, ip+" is this computer")
		return 0, false
	}
	if !slices.Contains(hosts[i].OpenPorts, port) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("port %d is not open on %s", port, ip))
		return 0, false
	}
	return port, true
}

// handleRemoteOpen starts Remote Desktop or Screen Sharing to a device.
func (s *Server) handleRemoteOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP   string      `json:"ip"`
		Kind remote.Kind `json:"kind"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Kind != remote.RDP && body.Kind != remote.VNC {
		writeError(w, http.StatusBadRequest, "kind must be rdp or vnc")
		return
	}
	port, ok := s.remoteTarget(w, r.PathValue("id"), body.IP, body.Kind)
	if !ok {
		return
	}
	switch err := s.cfg.Remote.Open(body.Kind, body.IP, port); {
	case errors.Is(err, remote.ErrNoApp):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "noApp": true})
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleOSOpen opens a page of this computer's own settings, named from a
// fixed list ({"page":"location"}), for the advice's Open Location
// settings button. Any other name gets 400; a computer without the page
// (macOS) gets 501.
func (s *Server) handleOSOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Page string `json:"page"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !remote.KnownOSPage(body.Page) {
		writeError(w, http.StatusBadRequest, "unknown settings page")
		return
	}
	switch err := s.cfg.OpenOSPage(body.Page); {
	case errors.Is(err, remote.ErrOSPageUnsupported):
		writeError(w, http.StatusNotImplemented, "netdiag can't open this settings page on this computer")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRemoteRDPFile downloads a Remote Desktop connection file, for
// computers where netdiag cannot start Remote Desktop itself.
func (s *Server) handleRemoteRDPFile(w http.ResponseWriter, r *http.Request) {
	ip := r.URL.Query().Get("ip")
	port, ok := s.remoteTarget(w, r.PathValue("id"), ip, remote.RDP)
	if !ok {
		return
	}
	b, err := remote.RDPFile(ip, port)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-rdp")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(ip, ":", "-")+`.rdp"`)
	_, _ = w.Write(b)
}

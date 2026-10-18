package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"netdiag/internal/chat"
)

// The team chat (bubbles) routes (v0.6 4.10, 4.16). Every one needs the
// session token: the actions by the method-based guard, the two reads by
// sessionReads, since the status names the server and the pairing state
// carries the code the admin compares (v0.6-D14). None of them is open to
// MCP agents: connecting, pairing, choosing the channel, the test alert and
// disconnecting are the person's (roadmap 8.11).

// needChat writes 503 when netdiag has no data folder to keep a team chat
// connection in.
func (s *Server) needChat(w http.ResponseWriter) bool {
	if s.chat == nil {
		writeError(w, http.StatusServiceUnavailable, "data store unavailable")
		return false
	}
	return true
}

// chatErr maps a manager error to its answer: words for the page (400) or
// a failure (500).
func chatErr(w http.ResponseWriter, err error) {
	var r *chat.RefusedError
	if errors.As(err, &r) {
		writeError(w, http.StatusBadRequest, r.Msg)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// decodeOptional reads a JSON body that may be empty.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleChatStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.chat == nil {
		writeJSON(w, http.StatusOK, chat.Status{State: chat.StateOff})
		return
	}
	writeJSON(w, http.StatusOK, s.chat.Status())
}

func (s *Server) handleChatDiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Interface string `json:"interface"`
	}
	if !s.needChat(w) || !decodeOptional(w, r, &req) {
		return
	}
	d, err := s.chat.Discover(r.Context(), req.Interface)
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleChatInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address     string `json:"address"`
		OnboardPort int    `json:"onboardPort"`
	}
	if !s.needChat(w) || !decodeBody(w, r, &req) {
		return
	}
	in, err := s.chat.Inspect(r.Context(), req.Address, req.OnboardPort)
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleChatConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionString string `json:"connectionString"`
	}
	if !s.needChat(w) || !decodeBody(w, r, &req) {
		return
	}
	c, err := s.chat.ConnectString(r.Context(), req.ConnectionString)
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleChatPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address     string `json:"address"`
		Fingerprint string `json:"fingerprint"`
		Confirmed   bool   `json:"confirmed"`
		Reconnect   bool   `json:"reconnect"`
	}
	if !s.needChat(w) || !decodeBody(w, r, &req) {
		return
	}
	if !req.Confirmed && !req.Reconnect {
		writeError(w, http.StatusBadRequest, "Compare the chat server's fingerprint first.")
		return
	}
	p, err := s.chat.StartPairing(r.Context(), req.Address, req.Fingerprint, req.Reconnect)
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleChatPairStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.needChat(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.chat.PairStatus())
}

func (s *Server) handleChatPairCancel(w http.ResponseWriter, r *http.Request) {
	if !s.needChat(w) {
		return
	}
	s.chat.CancelPairing()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChatChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelID string `json:"channelId"`
	}
	if !s.needChat(w) || !decodeOptional(w, r, &req) {
		return
	}
	c, err := s.chat.SetChannel(r.Context(), req.ChannelID)
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleChatTest(w http.ResponseWriter, r *http.Request) {
	if !s.needChat(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.chat.SendTest(r.Context()))
}

// handleChatRetry is Try now: ask the chat server at once and let the
// waiting alerts go (v0.6 4.10 Recovery). It answers the status.
func (s *Server) handleChatRetry(w http.ResponseWriter, r *http.Request) {
	if !s.needChat(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.chat.TryNow(r.Context()))
}

func (s *Server) handleChatDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.needChat(w) {
		return
	}
	if err := s.chat.Disconnect(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not forget the team chat connection: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

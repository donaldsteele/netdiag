package server

import (
	"crypto/subtle"
	"mime"
	"net/http"
	"path"
	"strings"
)

// guard rejects requests whose Host header is not the loopback address the
// server is bound to (DNS-rebinding defence) and POSTs that are not JSON
// (blocks simple cross-origin form submissions).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// SessionHeader carries this launch's session token (roadmap 8.3).
const SessionHeader = "X-Netdiag-Session"

// sessionMessage is the 401 answer's error. The page shows its own banner.
const sessionMessage = "Open netdiag again to use this button."

// sessionGuard requires the launch token in X-Netdiag-Session on every
// request under /api/ that is not a GET or HEAD, whatever its route (so a
// route added later is covered by itself), and on the reads that return a
// secret: GET /api/settings has the SNMP community in clear (v0.6-D14).
// Other OS users on this computer, and programs that cannot read this
// user's data folder (where instance.json keeps the token), can reach
// 127.0.0.1 but not the token. A program running as this user can read the
// folder anyway, so it is out of scope (v0.6 4.1). /mcp is not under /api/:
// the Agent access switch and the Origin check stay its consent.
func (s *Server) sessionGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsSession(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get(SessionHeader)), []byte(s.token)) != 1 {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": sessionMessage, "session": false})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionReads are the GET routes that need the session token (v0.6-D14):
// the settings (the SNMP community in clear), the team chat status (which
// server and channel netdiag posts to) and the pairing state (the code the
// chat admin compares).
var sessionReads = map[string]bool{"/api/settings": true, "/api/bubbles": true, "/api/bubbles/pair/status": true}

// needsSession reports whether r must carry the session token. The path is
// cleaned first, so another spelling of a gated path is gated too.
func needsSession(r *http.Request) bool {
	p := path.Clean("/" + r.URL.Path)
	if p != "/api" && !strings.HasPrefix(p, "/api/") {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return sessionReads[p]
	}
	return true
}

func (s *Server) hostAllowed(host string) bool {
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	return port != "" && (host == "127.0.0.1:"+port || host == "localhost:"+port)
}

// originAllowed permits an absent Origin or the server's own loopback origin.
func (s *Server) originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	return o == "http://127.0.0.1:"+port || o == "http://localhost:"+port
}

// mcpGate applies the Origin check and the runtime enable switch before next.
func (s *Server) mcpGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "forbidden origin")
			return
		}
		if !s.mcpOn.Load() {
			writeError(w, http.StatusNotFound, "MCP is disabled; enable it in Settings > Agent access")
			return
		}
		next.ServeHTTP(w, r)
	})
}

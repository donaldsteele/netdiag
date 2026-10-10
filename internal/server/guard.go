package server

import (
	"mime"
	"net/http"
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

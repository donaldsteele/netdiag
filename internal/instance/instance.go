// Package instance keeps one netdiag window per data folder (v0.6 F9b). It
// owns instance.json, which names the running netdiag's port, process,
// instance id and session token, and the start lock that lets only one
// netdiag at a time serve a data folder. A second launch reads the file,
// asks GET /api/identity on the remembered port and, when the same netdiag
// answers, opens its page instead of starting another one.
//
// The token is new on every launch. It gates the action routes of the
// loopback API (X-Netdiag-Session, roadmap 8.3), so it never goes into
// settings, logs, exports or MCP output.
package instance

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"netdiag/internal/store"
)

// schemaVersion is instance.json's schemaVersion.
const schemaVersion = 1

// DefaultPort is the address netdiag prefers when nothing is remembered and
// --port is not given (v0.6-D6): http://127.0.0.1:8787.
const DefaultPort = 8787

// App is what GET /api/identity names in app.
const App = "netdiag"

// Info is instance.json: the netdiag serving this data folder, as it was
// when it started. The file stays after netdiag exits, so the next launch
// remembers the port.
type Info struct {
	SchemaVersion int    `json:"schemaVersion"`
	Port          int    `json:"port"`
	PID           int    `json:"pid"`
	InstanceID    string `json:"instanceId"`
	Token         string `json:"token"`
	StartedAt     string `json:"startedAt"`
	Version       string `json:"version"`
}

// String describes the instance without its token.
func (i Info) String() string {
	return fmt.Sprintf("netdiag %s on port %d (pid %d, %s)", i.Version, i.Port, i.PID, i.InstanceID)
}

// GoString keeps %#v from printing the token too.
func (i Info) GoString() string { return i.String() }

// Identity is the answer of GET /api/identity.
type Identity struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	PID        int    `json:"pid"`
	InstanceID string `json:"instanceId"`
	StartedAt  string `json:"startedAt"`
}

// Matches reports whether id is the netdiag that wrote i: it names this app
// and the instance id in the file.
func (i Info) Matches(id Identity) bool {
	return i.InstanceID != "" && id.App == App && id.InstanceID == i.InstanceID
}

// Foreign reports whether id is another netdiag than the one that wrote i:
// another OS user's netdiag, or one with another data folder, now holds the
// remembered port.
func (i Info) Foreign(id Identity) bool { return id.App == App && !i.Matches(id) }

// Load reads instance.json. A missing file gives the zero Info and no
// error. A file that is not valid JSON is set aside (instance.json.bad-<time>)
// and also gives the zero Info: the next Save writes a new one.
func Load(st *store.Store) (Info, error) {
	b, err := st.ReadFile(store.FileInstance)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil || i.Port < 0 || i.Port > 65535 {
		_, qerr := st.QuarantineFile(store.FileInstance)
		return Info{}, qerr
	}
	return i, nil
}

// Save writes instance.json atomically (owner-only on macOS).
func Save(st *store.Store, i Info) error {
	i.SchemaVersion = schemaVersion
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	return st.WriteFile(store.FileInstance, append(b, '\n'))
}

// NewToken returns a new session token: 32 bytes from crypto/rand, base64url
// without padding (43 characters).
func NewToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("instance: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// NewID returns a new instance id, "i-" and 32 hex digits.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("instance: crypto/rand: " + err.Error())
	}
	return "i-" + hex.EncodeToString(b[:])
}

// PageURL is the address that opens the page of the netdiag on port with
// buttons enabled: the token rides in the fragment, which the browser never
// sends to the server.
func PageURL(port int, token string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/#s=" + token
}

// IdentityTimeout is how long a launch waits for the remembered port to
// answer GET /api/identity.
const IdentityTimeout = time.Second

// Probe asks GET /api/identity on 127.0.0.1:port. It never goes through a
// proxy.
func Probe(ctx context.Context, port int, timeout time.Duration) (Identity, error) {
	if port <= 0 || port > 65535 {
		return Identity{}, errors.New("instance: no port")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+"/api/identity", nil)
	if err != nil {
		return Identity{}, err
	}
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("instance: identity answered %d", resp.StatusCode)
	}
	var id Identity
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// Running asks the port in i whether the netdiag that wrote i still serves
// it. ans is what answered, if anything did.
func Running(ctx context.Context, i Info) (running bool, ans Identity, answered bool) {
	if i.Port == 0 {
		return false, Identity{}, false
	}
	id, err := Probe(ctx, i.Port, IdentityTimeout)
	if err != nil {
		return false, Identity{}, false
	}
	return i.Matches(id), id, true
}

// Ports is the order a launch tries ports in (v0.6-D6): --port alone when it
// is given; otherwise the remembered port, then DefaultPort, then 0 (a
// random free port). When another netdiag answered on the remembered port
// (foreign), the launch goes straight to a random port.
func Ports(flagPort, remembered int, foreign bool) []int {
	if flagPort > 0 {
		return []int{flagPort}
	}
	if foreign {
		return []int{0}
	}
	var out []int
	if remembered > 0 && remembered <= 65535 && remembered != 80 && remembered != 443 {
		out = append(out, remembered)
	}
	if remembered != DefaultPort {
		out = append(out, DefaultPort)
	}
	return append(out, 0)
}

package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/store"
)

// Fingerprint tells one network from another without its Wi-Fi name: this
// computer's address prefix, the router and the router's MAC. ID is
// model.NetworkID of the three, the id the "This network has no internet"
// switch keeps and health reports carry, so all three agree on a network.
type Fingerprint struct {
	ID         string `json:"id"`
	CIDR       string `json:"cidr"`
	Gateway    string `json:"gateway"`
	GatewayMAC string `json:"gatewayMAC"`
}

// NewFingerprint names the network the way the health checks do: by its
// prefix alone when there is no router, and not at all (ID "") when there
// is a router whose MAC is unknown, since two networks that use the same
// addresses could not be told apart.
func NewFingerprint(cidr, gateway, gatewayMAC string) Fingerprint {
	fp := Fingerprint{CIDR: cidr, Gateway: gateway, GatewayMAC: gatewayMAC}
	switch {
	case gateway == "" && cidr != "":
		fp.ID = model.NetworkID(cidr, "", "")
	case gateway != "" && gatewayMAC != "":
		fp.ID = model.NetworkID(cidr, gateway, gatewayMAC)
	}
	return fp
}

// samePrefix and sameRouter report whether this computer's address prefix
// and the router's MAC, read in a live cycle, still match fp. A part either
// side does not know matches.
func (fp Fingerprint) samePrefix(cidr string) bool {
	return cidr == "" || fp.CIDR == "" || maskedPrefix(cidr) == maskedPrefix(fp.CIDR)
}

func (fp Fingerprint) sameRouter(gatewayMAC string) bool {
	return gatewayMAC == "" || fp.GatewayMAC == "" || strings.EqualFold(gatewayMAC, fp.GatewayMAC)
}

func maskedPrefix(s string) string {
	if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
		return p.Masked().String()
	}
	return s
}

// ProblemLedger is the open-problem ledger (*problems.Ledger satisfies it).
// A scan seeds its alert engine with the ledger's problems for its network
// at the end of the first pass and records every notice after that.
type ProblemLedger interface {
	Apply(network string, ns []model.Notice)
	OpenFor(network string) []notify.OpenFault
}

// ProfileStore remembers the networks netdiag has watched (*Profiles
// satisfies it).
type ProfileStore interface {
	Seen(fp Fingerprint, at time.Time) error
}

// MaxProfiles is how many networks network-profile.json remembers; the one
// seen longest ago goes first.
const MaxProfiles = 20

// profilesSchema is network-profile.json's schemaVersion.
const profilesSchema = 1

// NetworkProfile is one remembered network.
type NetworkProfile struct {
	Fingerprint
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

type profilesFile struct {
	SchemaVersion int              `json:"schemaVersion"`
	Networks      []NetworkProfile `json:"networks"`
}

// ProfileFiles is the part of *store.Store network-profile.json goes
// through.
type ProfileFiles interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte) error
	QuarantineFile(name string) (string, error)
}

// Profiles keeps network-profile.json. It is safe for concurrent use; one
// per process is the file's only writer.
type Profiles struct {
	mu    sync.Mutex
	files ProfileFiles
}

// errSetAside means an unreadable network-profile.json was set aside; the
// next Seen starts a new one.
var errSetAside = errors.New("network profiles set aside")

// NewProfiles returns the profile store kept in files.
func NewProfiles(files ProfileFiles) *Profiles { return &Profiles{files: files} }

// List returns the remembered networks, the one seen last first. An
// unreadable file is set aside as <name>.bad-<time> and lists nothing.
func (p *Profiles) List() ([]NetworkProfile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.load()
}

func (p *Profiles) load() ([]NetworkProfile, error) {
	data, err := p.files.ReadFile(store.FileNetworkProfile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f profilesFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion < 1 {
		bad, qerr := p.files.QuarantineFile(store.FileNetworkProfile)
		if qerr != nil {
			return nil, fmt.Errorf("%s is unreadable and could not be set aside: %v", store.FileNetworkProfile, qerr)
		}
		return nil, fmt.Errorf("%w: %s was unreadable and is now %s", errSetAside, store.FileNetworkProfile, bad)
	}
	return f.Networks, nil
}

// Seen records that netdiag watched the network fp at at. A fingerprint
// without an id is not recorded.
func (p *Profiles) Seen(fp Fingerprint, at time.Time) error {
	if fp.ID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil && !errors.Is(err, errSetAside) {
		return err
	}
	ts := at.UTC().Format(time.RFC3339)
	found := false
	for i := range list {
		if list[i].ID == fp.ID {
			list[i].Fingerprint, list[i].LastSeen = fp, ts
			found = true
		}
	}
	if !found {
		list = append(list, NetworkProfile{Fingerprint: fp, FirstSeen: ts, LastSeen: ts})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].LastSeen > list[j].LastSeen })
	if len(list) > MaxProfiles {
		list = list[:MaxProfiles]
	}
	data, err := json.MarshalIndent(profilesFile{SchemaVersion: profilesSchema, Networks: list}, "", "  ")
	if err != nil {
		return err
	}
	return p.files.WriteFile(store.FileNetworkProfile, data)
}

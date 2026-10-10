package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DHCPInfo describes how the scan interface got its address.
type DHCPInfo struct {
	Enabled       bool
	APIPA         bool
	IP            string
	Server        string
	LeaseObtained string
	LeaseExpires  string
	LeaseSeconds  int
}

// WiFiInfo describes the current wireless association.
type WiFiInfo struct {
	Connected  bool
	SSID       string
	BSSID      string
	Redacted   bool // SSID hidden by the OS (macOS privacy)
	SignalPct  int
	RSSI       int // dBm; 0 = unknown
	Channel    int
	Band       string
	PHY        string
	TxRateMbps float64
}

var errUnrecognized = errors.New("unrecognized command output (localized OS?)")

func splitKV(line string) (key, val string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimRight(strings.TrimSpace(line[:i]), ". ")
	val = strings.TrimSpace(line[i+1:])
	return key, val, key != ""
}

// parseIpconfigAll finds the adapter owning ip in `ipconfig /all` output
// (English keys only; anything else yields an error).
func parseIpconfigAll(out, ip string) (DHCPInfo, error) {
	var blocks [][]string
	var cur []string
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln != "" && ln[0] != ' ' && ln[0] != '\t' && strings.HasSuffix(strings.TrimSpace(ln), ":") {
			if cur != nil {
				blocks = append(blocks, cur)
			}
			cur = []string{}
			continue
		}
		if cur != nil {
			cur = append(cur, ln)
		}
	}
	if cur != nil {
		blocks = append(blocks, cur)
	}
	for _, b := range blocks {
		kv := map[string]string{}
		found := false
		for _, ln := range b {
			k, v, ok := splitKV(ln)
			if !ok {
				continue
			}
			if _, dup := kv[k]; !dup {
				kv[k] = v
			}
			if ip != "" && strings.HasPrefix(v, ip) && (len(v) == len(ip) || v[len(ip)] == '(') {
				found = true
			}
		}
		if !found {
			continue
		}
		en, ok := kv["DHCP Enabled"]
		if !ok {
			return DHCPInfo{}, errUnrecognized
		}
		return DHCPInfo{
			IP:            ip,
			Enabled:       strings.EqualFold(en, "yes"),
			Server:        kv["DHCP Server"],
			LeaseObtained: kv["Lease Obtained"],
			LeaseExpires:  kv["Lease Expires"],
			APIPA:         strings.HasPrefix(ip, "169.254."),
		}, nil
	}
	return DHCPInfo{}, fmt.Errorf("no adapter with address %q found", ip)
}

// parseGetPacket parses macOS `ipconfig getpacket <if>`. Empty or
// lease-less output means no DHCP lease (static configuration).
func parseGetPacket(out string) DHCPInfo {
	var info DHCPInfo
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "yiaddr = "):
			v := strings.TrimSpace(strings.TrimPrefix(ln, "yiaddr = "))
			if v != "0.0.0.0" {
				info.IP = v
			}
		case strings.HasPrefix(ln, "server_identifier"):
			if _, v, ok := splitKV(ln); ok {
				info.Server = v
			}
		case strings.HasPrefix(ln, "lease_time"):
			if _, v, ok := splitKV(ln); ok {
				if h, found := strings.CutPrefix(v, "0x"); found {
					if n, err := strconv.ParseInt(h, 16, 64); err == nil {
						info.LeaseSeconds = int(n)
					}
				} else if n, err := strconv.Atoi(v); err == nil {
					info.LeaseSeconds = n
				}
			}
		}
	}
	info.Enabled = info.Server != "" || info.IP != ""
	info.APIPA = strings.HasPrefix(info.IP, "169.254.")
	return info
}

var pctRe = regexp.MustCompile(`^(\d+)`)

func bandFromChannel(ch int) string {
	switch {
	case ch <= 0:
		return ""
	case ch <= 14:
		return "2.4 GHz"
	default:
		return "5 GHz"
	}
}

// parseNetshWlan parses English `netsh wlan show interfaces`. Output is
// only trusted when the English "State" key is present.
func parseNetshWlan(out string) (WiFiInfo, error) {
	var blocks []map[string]string
	var cur map[string]string
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == "" || (ln[0] != ' ' && ln[0] != '\t') {
			continue
		}
		k, v, ok := splitKV(ln)
		if !ok {
			continue
		}
		if k == "Name" || cur == nil {
			cur = map[string]string{}
			blocks = append(blocks, cur)
		}
		if _, dup := cur[k]; !dup {
			cur[k] = v
		}
	}
	var pick map[string]string
	for _, b := range blocks {
		st, ok := b["State"]
		if !ok {
			continue
		}
		if pick == nil {
			pick = b
		}
		if strings.EqualFold(st, "connected") {
			pick = b
			break
		}
	}
	if pick == nil {
		return WiFiInfo{}, errUnrecognized
	}
	if !strings.EqualFold(pick["State"], "connected") {
		return WiFiInfo{Connected: false}, nil
	}
	w := WiFiInfo{Connected: true, SSID: pick["SSID"], BSSID: pick["BSSID"], PHY: pick["Radio type"], Band: pick["Band"]}
	if w.BSSID == "" {
		w.BSSID = pick["AP BSSID"]
	}
	if m := pctRe.FindString(pick["Signal"]); m != "" {
		w.SignalPct, _ = strconv.Atoi(m)
		w.RSSI = w.SignalPct/2 - 100
	}
	w.Channel, _ = strconv.Atoi(pick["Channel"])
	if w.Band == "" {
		w.Band = bandFromChannel(w.Channel)
	}
	w.TxRateMbps, _ = strconv.ParseFloat(pick["Transmit rate (Mbps)"], 64)
	return w, nil
}

var (
	dbmRe  = regexp.MustCompile(`(-?\d+)\s*dBm`)
	chanRe = regexp.MustCompile(`^\s*(\d+)`)
	ghzRe  = regexp.MustCompile(`\(\s*(\d+(?:\.\d+)?)\s*GHz`)
)

type spProfiler struct {
	Data []struct {
		Ifaces []struct {
			Name string `json:"_name"`
			Cur  *struct {
				Name   string `json:"_name"`
				Chan   string `json:"spairport_network_channel"`
				Phy    string `json:"spairport_network_phymode"`
				Rate   any    `json:"spairport_network_rate"`
				Signal string `json:"spairport_signal_noise"`
			} `json:"spairport_current_network_information"`
		} `json:"spairport_airport_interfaces"`
	} `json:"SPAirPortDataType"`
}

// parseSystemProfiler parses `system_profiler SPAirPortDataType -json`.
// ifaceName is preferred; otherwise the first associated interface wins.
func parseSystemProfiler(data []byte, ifaceName string) (WiFiInfo, error) {
	var root spProfiler
	if err := json.Unmarshal(data, &root); err != nil {
		return WiFiInfo{}, fmt.Errorf("system_profiler json: %w", err)
	}
	if len(root.Data) == 0 {
		return WiFiInfo{}, errUnrecognized
	}
	for pass := 0; pass < 2; pass++ {
		for _, d := range root.Data {
			for _, f := range d.Ifaces {
				if f.Cur == nil || (pass == 0 && ifaceName != "" && f.Name != ifaceName) {
					continue
				}
				w := WiFiInfo{Connected: true, SSID: f.Cur.Name, PHY: f.Cur.Phy}
				if w.SSID == "<redacted>" || w.SSID == "" {
					w.SSID, w.Redacted = "", true
				}
				if m := dbmRe.FindStringSubmatch(f.Cur.Signal); m != nil {
					w.RSSI, _ = strconv.Atoi(m[1])
					p := 2 * (w.RSSI + 100)
					w.SignalPct = min(max(p, 0), 100)
				}
				if m := chanRe.FindStringSubmatch(f.Cur.Chan); m != nil {
					w.Channel, _ = strconv.Atoi(m[1])
				}
				if m := ghzRe.FindStringSubmatch(f.Cur.Chan); m != nil {
					w.Band = m[1] + " GHz"
				} else {
					w.Band = bandFromChannel(w.Channel)
				}
				switch r := f.Cur.Rate.(type) {
				case float64:
					w.TxRateMbps = r
				case string:
					w.TxRateMbps, _ = strconv.ParseFloat(r, 64)
				}
				return w, nil
			}
		}
	}
	return WiFiInfo{Connected: false}, nil
}

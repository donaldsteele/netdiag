// Package notify is a pure, deterministic alert rule engine. It turns state
// transitions into Notices, honouring per-trigger toggles, a supply threshold,
// dedupe keys and a cooldown. It never performs I/O.
package notify

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"netdiag/internal/model"
)

// DefaultCooldown is the default dedupe cooldown.
const DefaultCooldown = 300 * time.Second

// DefaultSupplyThresholdPct is the default low-supply threshold.
const DefaultSupplyThresholdPct = 15

// Config holds the alert settings the engine needs.
type Config struct {
	// Triggers maps trigger name (model.Trigger*) to enabled. Missing = disabled.
	Triggers           map[string]bool
	SupplyThresholdPct int
	Cooldown           time.Duration
}

// DefaultConfig mirrors the default settings (deviceOffline off).
func DefaultConfig() Config {
	return Config{
		Triggers: map[string]bool{
			model.TriggerPrinterOffline: true,
			model.TriggerDeviceOffline:  false,
			model.TriggerSupplyLow:      true,
			model.TriggerPrinterAlert:   true,
			model.TriggerNewDevice:      true,
			model.TriggerIPConflict:     true,
			model.TriggerHealthFailed:   true,
		},
		SupplyThresholdPct: DefaultSupplyThresholdPct,
		Cooldown:           DefaultCooldown,
	}
}

// Engine produces Notices from transitions.
type Engine struct {
	mu   sync.Mutex
	cfg  Config
	now  func() time.Time
	seq  int
	last map[string]time.Time
	// faults holds, per printer IP, every alert seen since the printer was last
	// clear, in first-seen order, so Printer OK again names all of them even
	// when they clear one at a time.
	faults map[string][]string
}

// New creates an engine. A nil clock uses time.Now.
func New(cfg Config, clock func() time.Time) *Engine {
	if clock == nil {
		clock = time.Now
	}
	return &Engine{cfg: cfg, now: clock, last: map[string]time.Time{}, faults: map[string][]string{}}
}

// SetConfig replaces the config (dedupe history is kept).
func (e *Engine) SetConfig(cfg Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

func (e *Engine) emit(out []model.Notice, trigger, sev, ip, title, msg, keyPart string) []model.Notice {
	if !e.cfg.Triggers[trigger] {
		return out
	}
	key := trigger + "|" + ip + "|" + keyPart
	now := e.now()
	if t, ok := e.last[key]; ok && now.Sub(t) < e.cfg.Cooldown {
		return out
	}
	e.last[key] = now
	e.seq++
	return append(out, model.Notice{
		ID: fmt.Sprintf("n-%d", e.seq), Trigger: trigger, Severity: sev, IP: ip,
		Title: title, Message: msg, At: now.UTC().Format(time.RFC3339), Key: key,
	})
}

// forget drops the cooldown history of every key starting with prefix.
func (e *Engine) forget(prefix string) {
	for k := range e.last {
		if strings.HasPrefix(k, prefix) {
			delete(e.last, k)
		}
	}
}

func label(h model.Host) string {
	switch {
	case h.Hostname != "":
		return h.Hostname + " (" + h.IP + ")"
	case h.Printer != nil && h.Printer.Model != "":
		return h.Printer.Model + " (" + h.IP + ")"
	}
	return h.IP
}

// Observe compares a host's previous and current state. A zero prev (empty IP)
// means the host was not known before.
func (e *Engine) Observe(prev, cur model.Host) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	ip := cur.IP
	l := label(cur)

	if prev.IP == "" {
		if cur.IsNew {
			out = e.emit(out, model.TriggerNewDevice, "info", ip, "New device",
				fmt.Sprintf("%s appeared on the network.", l), "")
		}
		return out
	}

	if prev.Online && !cur.Online {
		if cur.IsPrinter {
			out = e.emit(out, model.TriggerPrinterOffline, "critical", ip, "Printer offline",
				fmt.Sprintf("Printer %s stopped responding.", l), "")
		} else {
			out = e.emit(out, model.TriggerDeviceOffline, "warn", ip, "Device offline",
				fmt.Sprintf("%s stopped responding.", l), "")
		}
	}

	if cur.Printer != nil {
		var pp *model.PrinterInfo = prev.Printer
		if pp != nil {
			thr := e.cfg.SupplyThresholdPct
			for _, cs := range cur.Printer.Supplies {
				for _, ps := range pp.Supplies {
					if ps.Description != cs.Description {
						continue
					}
					if ps.Percent >= 0 && cs.Percent >= 0 && ps.Percent > thr && cs.Percent <= thr {
						out = e.emit(out, model.TriggerSupplyLow, "warn", ip, "Supply low",
							fmt.Sprintf("%s: %s at %d%% (threshold %d%%).", l, cs.Description, cs.Percent, thr),
							cs.Description)
					}
					break
				}
			}
		}
		seen := map[string]bool{}
		if pp != nil {
			for _, a := range pp.Alerts {
				seen[a.Code+"|"+a.Description] = true
			}
			e.recordFaults(ip, pp.Alerts)
		}
		e.recordFaults(ip, cur.Printer.Alerts)
		if len(cur.Printer.Alerts) == 0 {
			if pp != nil && len(pp.Alerts) > 0 {
				// A fix is a transition: forget the cooldown of the cleared
				// alerts so a relapse is reported straight away.
				e.forget(model.TriggerPrinterAlert + "|" + ip + "|")
				out = e.emit(out, model.TriggerPrinterAlert, "info", ip, "Printer OK again",
					fmt.Sprintf("%s: cleared %s.", l, strings.Join(e.faults[ip], ", ")), "cleared")
			}
			delete(e.faults, ip)
		}
		for _, a := range cur.Printer.Alerts {
			k := a.Code + "|" + a.Description
			if seen[k] {
				continue
			}
			sev := "warn"
			if a.Severity == "critical" || a.Severity == "error" {
				sev = "critical"
			}
			out = e.emit(out, model.TriggerPrinterAlert, sev, ip, "Printer alert",
				fmt.Sprintf("%s: %s", l, a.Description), k)
		}
	}
	return out
}

// recordFaults adds alert descriptions not yet in the printer's fault episode.
func (e *Engine) recordFaults(ip string, alerts []model.Alert) {
	for _, a := range alerts {
		dup := false
		for _, d := range e.faults[ip] {
			if d == a.Description {
				dup = true
				break
			}
		}
		if !dup {
			e.faults[ip] = append(e.faults[ip], a.Description)
		}
	}
}

// ObserveConflict emits a notice for a newly detected conflict.
func (e *Engine) ObserveConflict(c model.Conflict, isNew bool) []model.Notice {
	if !isNew {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ip := ""
	if len(c.IPs) > 0 {
		ip = c.IPs[0]
	}
	sev := "warn"
	if c.Severity == "fail" {
		sev = "critical"
	}
	return e.emit(nil, model.TriggerIPConflict, sev, ip, "Network conflict", c.Detail, c.ID)
}

func rank(s string) int {
	switch s {
	case "fail":
		return 3
	case "warn":
		return 2
	}
	return 0
}

// ObserveHealth emits a notice for each check that newly became "fail".
// A check absent from prev is treated as ok.
func (e *Engine) ObserveHealth(prev, cur model.HealthReport) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	pm := map[string]string{}
	for _, p := range prev.Checks {
		pm[p.ID] = p.Status
	}
	var out []model.Notice
	for _, c := range cur.Checks {
		if c.Status != model.HealthFail || rank(pm[c.ID]) >= rank(model.HealthFail) {
			continue
		}
		name := c.Label
		if name == "" {
			name = c.ID
		}
		out = e.emit(out, model.TriggerHealthFailed, "critical", "", "Health check failed",
			fmt.Sprintf("%s check failed.", name), c.ID)
	}
	return out
}

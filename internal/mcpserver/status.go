package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/service"
)

// ---- get_status ----

// StatusOut is get_status: the service's Status (a value with no tokens)
// plus this server's version and transport.
type StatusOut struct {
	Version   string `json:"version"`
	Transport string `json:"transport"`
	service.Status
}

func (t *tools) getStatus(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, StatusOut, error) {
	return nil, t.status(ctx), nil
}

func (t *tools) status(ctx context.Context) StatusOut {
	v := t.opts.Version
	if v == "" {
		v = "dev"
	}
	return StatusOut{Version: v, Transport: t.opts.Transport, Status: t.svc.Status(ctx)}
}

// ---- list_problems ----

const (
	defaultProblems = 50
	maxProblems     = 200
)

type listProblemsIn struct {
	IncludeFixed *bool `json:"includeFixed,omitempty" jsonschema:"also list the problems fixed in the last 24 hours (default true)"`
	Limit        int   `json:"limit,omitempty" jsonschema:"max open problems, and max fixed ones (default 50, max 200)"`
}

// ProblemChat is a problem's team chat state: posted once its alert is in
// the chat, waiting while netdiag still has to send it.
type ProblemChat struct {
	Posted  bool `json:"posted"`
	Waiting bool `json:"waiting,omitempty"`
}

// Recheck names the tool that checks a problem again, with its arguments.
type Recheck struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
}

// OpenProblem is one open problem of list_problems: the card's item
// (problems.Problem), with its chat state and its re-check.
type OpenProblem struct {
	// Key equals the team chat alert's dedupe_key.
	Key      string              `json:"key"`
	Kind     string              `json:"kind"`
	Severity string              `json:"severity"` // critical, warning or info
	Title    string              `json:"title"`
	Detail   string              `json:"detail,omitempty"`
	Device   *problems.DeviceRef `json:"device,omitempty"`
	Check    string              `json:"check,omitempty"`
	Since    string              `json:"since,omitempty"`
	// LastCheckedAt is when netdiag last looked (the list's checkedAt).
	LastCheckedAt string        `json:"lastCheckedAt,omitempty"`
	Advice        *model.Advice `json:"advice,omitempty"`
	Chat          ProblemChat   `json:"chat"`
	Recheck       Recheck       `json:"recheck"`
}

// ProblemsOut is list_problems.
type ProblemsOut struct {
	CheckedAt      string               `json:"checkedAt,omitempty"`
	EverythingFine bool                 `json:"everythingFine"`
	Source         string               `json:"source"`
	Watching       bool                 `json:"watching"`
	Held           bool                 `json:"held,omitempty"`
	HeldReason     string               `json:"heldReason,omitempty"`
	Open           []OpenProblem        `json:"open"`
	Fixed          []problems.FixedItem `json:"fixed"`
	Truncated      bool                 `json:"truncated,omitempty"`
}

func (t *tools) listProblems(_ context.Context, _ *mcp.CallToolRequest, in listProblemsIn) (*mcp.CallToolResult, ProblemsOut, error) {
	return nil, problemsOut(t.svc.Problems(), in), nil
}

// problemsOut shapes the card's view for agents: each open problem gets
// its chat state and the tool that re-checks it, and both lists are capped.
func problemsOut(v problems.View, in listProblemsIn) ProblemsOut {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultProblems
	}
	limit = min(limit, maxProblems)
	out := ProblemsOut{CheckedAt: v.CheckedAt, EverythingFine: v.EverythingFine, Source: v.Source, Watching: v.Watching,
		Held: v.Held, HeldReason: v.HeldReason, Open: []OpenProblem{}, Fixed: []problems.FixedItem{}}
	for i, p := range v.Open {
		if i >= limit {
			out.Truncated = true
			break
		}
		out.Open = append(out.Open, OpenProblem{Key: p.Key, Kind: p.Kind, Severity: p.Severity, Title: p.Title,
			Detail: p.Detail, Device: p.Device, Check: p.Check, Since: p.Since, LastCheckedAt: v.CheckedAt, Advice: p.Advice,
			Chat:    ProblemChat{Posted: p.Chat == problems.ChatPosted, Waiting: p.Chat == problems.ChatPending},
			Recheck: recheckFor(p)})
	}
	if in.IncludeFixed == nil || *in.IncludeFixed {
		for i, f := range v.Fixed {
			if i >= limit {
				out.Truncated = true
				break
			}
			out.Fixed = append(out.Fixed, f)
		}
	}
	return out
}

// recheckFor is the check an agent may run again for p: the health checks
// for a health problem, else a probe of its device.
func recheckFor(p problems.Problem) Recheck {
	if p.Check != "" || p.Device == nil {
		return Recheck{Tool: "run_health_check", Args: map[string]string{}}
	}
	return Recheck{Tool: "probe_host", Args: map[string]string{"ip": p.Device.IP}}
}

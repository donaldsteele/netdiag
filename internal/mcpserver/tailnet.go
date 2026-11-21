package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/model"
	"netdiag/internal/service"
)

// Tailnet tools (v0.6.1). Tailscale is read from this computer's own
// Tailscale service through the service layer; netdiag never changes it.

type pingTailnetIn struct {
	IP string `json:"ip" jsonschema:"the device's Tailscale address (100.x.y.z or fd7a:115c:a1e0::...), from get_tailnet"`
}

func registerTailnetTools(s *mcp.Server, t *tools) {
	mcp.AddTool(s, &mcp.Tool{Name: "get_tailnet", Title: "Get the tailnet",
		Description: "This computer's Tailscale tailnet: whether Tailscale runs here (state: running, stopped, needs-login or not-installed), the tailnet's MagicDNS suffix, this computer, and every other device with its Tailscale addresses, OS, online state, last seen, key expiry (keyExpiresSoon within 7 days) and the path traffic takes (path: direct with its endpoint, relayed through a Tailscale DERP relay with its region code, idle when there was no traffic lately, or offline). card is the Tailscale card of the Health view, with plain-words advice. Read from the local Tailscale service; nothing is sent on the network. The tailnet is named by its suffix only.",
		Annotations: readAnn("Get the tailnet")}, t.getTailnet)

	mcp.AddTool(s, &mcp.Tool{Name: "ping_tailnet_device", Title: "Ping a tailnet device",
		Description: "Ping one tailnet device through Tailscale and report whether it answers, the latency in milliseconds and the path (direct with its endpoint, or relayed with the DERP region). Only a Tailscale address of a device listed by get_tailnet; one device at a time, at most once every 5 seconds per device. Sends real traffic through Tailscale to that one device.",
		Annotations: probeAnn("Ping a tailnet device")}, t.pingTailnet)
}

func (t *tools) getTailnet(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, model.TailnetView, error) {
	return nil, t.svc.Tailnet(ctx), nil
}

func (t *tools) pingTailnet(ctx context.Context, _ *mcp.CallToolRequest, in pingTailnetIn) (*mcp.CallToolResult, model.TailnetPing, error) {
	res, err := t.svc.PingTailnet(ctx, strings.TrimSpace(in.IP))
	switch {
	case errors.Is(err, service.ErrTailnetPingTooSoon):
		return nil, model.TailnetPing{}, errors.New("this device was pinged a moment ago; wait 5 seconds before pinging it again")
	case errors.Is(err, service.ErrNotTailscaleAddr):
		return nil, model.TailnetPing{}, errors.New("not a Tailscale address of a device on this tailnet; call get_tailnet for the devices and their addresses")
	case err != nil:
		return nil, model.TailnetPing{}, err
	}
	return nil, res, nil
}

// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/read.go (ListChannels) and pkg/bubblesclient/device.go
// (Status).
// Modified for netdiag: the package name and the protocol import path;
// message lists, search, alert lists, acknowledging and resolving by id
// (which need messages:read or a person's session) and the device sign-in
// calls are left out.

package bubbles

import (
	"context"
	"net/http"

	"netdiag/internal/bubbles/protocol"
)

// ListChannels returns the channels the token's bot belongs to (inside the
// token's channel allowlist). The token needs channels:read or messages:read.
func (c *Client) ListChannels(ctx context.Context) ([]protocol.Channel, error) {
	var out protocol.ChannelList
	if err := c.do(ctx, "GET", apiPrefix+"/channels", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Status fetches the public server status (GET /status): the instance name
// and whether sign-up is open. It works without a token.
func (c *Client) Status(ctx context.Context) (*protocol.ServerStatus, error) {
	var out protocol.ServerStatus
	if err := c.do(ctx, http.MethodGet, apiPrefix+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

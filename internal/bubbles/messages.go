// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/messages.go.
// Modified for netdiag: the package name and the protocol import path.

package bubbles

import (
	"context"

	"netdiag/internal/bubbles/protocol"
)

// PostMessage posts a message to the channel with the given id. The token
// needs messages:write.
func (c *Client) PostMessage(ctx context.Context, channel string, m protocol.NewMessage) (*protocol.Message, error) {
	var out protocol.Message
	if err := c.do(ctx, "POST", apiPrefix+"/channels/"+pathEscape(channel)+"/messages", m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Self returns the app and token this client authenticates as.
func (c *Client) Self(ctx context.Context) (*protocol.AppSelf, error) {
	var out protocol.AppSelf
	if err := c.do(ctx, "GET", apiPrefix+"/apps/self", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

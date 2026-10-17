// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/alerts.go and pkg/bubblesclient/read.go
// (PostAlertResult and ResolveAlertKey).
// Modified for netdiag: the package name and the protocol import path.

package bubbles

import (
	"context"

	"netdiag/internal/bubbles/protocol"
)

// PostAlert creates or updates the alert identified by a.DedupeKey and
// returns its message. The token needs alerts:write.
func (c *Client) PostAlert(ctx context.Context, a protocol.AlertInput) (*protocol.Message, error) {
	var out protocol.PostedAlert
	if err := c.do(ctx, "POST", apiPrefix+"/alerts", a, &out); err != nil {
		return nil, err
	}
	return &out.Message, nil
}

// ResolveAlert resolves the open alert with the given dedupe key; text is an
// optional resolution note.
func (c *Client) ResolveAlert(ctx context.Context, dedupeKey, text string) error {
	return c.do(ctx, "POST", apiPrefix+"/alerts/resolve",
		protocol.ResolveAlertByKeyRequest{DedupeKey: dedupeKey, Text: text}, nil)
}

// PostAlertResult is PostAlert that also reports the transition (an
// AlertTransition* constant) the post caused.
func (c *Client) PostAlertResult(ctx context.Context, a protocol.AlertInput) (*protocol.PostedAlert, error) {
	var out protocol.PostedAlert
	if err := c.do(ctx, "POST", apiPrefix+"/alerts", a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveAlertKey resolves the open alert with the dedupe key and returns
// its message.
func (c *Client) ResolveAlertKey(ctx context.Context, dedupeKey, text string) (*protocol.Message, error) {
	var out protocol.Message
	in := protocol.ResolveAlertByKeyRequest{DedupeKey: dedupeKey, Text: text}
	if err := c.do(ctx, "POST", apiPrefix+"/alerts/resolve", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

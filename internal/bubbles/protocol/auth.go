// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/auth.go.
// Modified for netdiag: only ServerStatus is kept; sign-in, invites,
// sessions and devices are left out.

package protocol

// ServerStatus is the body of GET /status, which needs no sign-in: what a
// client shows before anyone is signed in.
type ServerStatus struct {
	InstanceName string `json:"instance_name"`
	// RegistrationOpen reports whether anyone may sign up right now.
	RegistrationOpen bool `json:"registration_open"`
	// RegistrationClosesAt is when open sign-up ends; null when it has no
	// deadline or is closed.
	RegistrationClosesAt *string `json:"registration_closes_at"`
}

// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/pairing.go.
// Modified for netdiag: the admin-side types are left out, and the
// PairingRequest alias comes from pkg/protocol/sdk.go.

package protocol

// Pairing request states.
const (
	PairingPending  = "pending"
	PairingApproved = "approved"
	PairingDenied   = "denied"
	PairingExpired  = "expired"
	PairingClaimed  = "claimed"
)

// PairingHeader carries the poll secret on GET /pairing/requests/{id}.
const PairingHeader = "X-Poll-Secret"

// PairingRequestInput is the body of POST /pairing/requests.
type PairingRequestInput struct {
	AppName         string   `json:"app_name"`
	Description     string   `json:"description,omitempty"`
	RequestedScopes []string `json:"requested_scopes,omitempty"`
}

// PairingRequest is the body of POST /api/v1/pairing/requests, under the
// name the Go SDK contract uses.
type PairingRequest = PairingRequestInput

// PairingCreated is the answer of POST /pairing/requests. The app shows Code
// to the admin and polls with PollSecret, which is shown only here.
type PairingCreated struct {
	ID         string `json:"id"`
	PollSecret string `json:"poll_secret"`
	Code       string `json:"code"`
	ExpiresAt  string `json:"expires_at"`
}

// PairingStatus is the answer of GET /pairing/requests/{id}. Token is set
// exactly once, on the first poll after the admin approved the request; the
// state is then claimed.
type PairingStatus struct {
	State     string `json:"state"`
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/errors.go.
// Modified for netdiag: the protocol import path; fatal (used only by the
// queue, which is not copied) is left out.

package bubbles

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"netdiag/internal/bubbles/protocol"
)

// Sentinel errors, comparable with errors.Is.
var (
	// ErrUnauthorized means the token was revoked or the server was reset
	// (HTTP 401, WebSocket close 4009/4010). The app must re-pair.
	ErrUnauthorized = errors.New("bubblesclient: unauthorized: token revoked or server reset; re-pair")
	// ErrServerIdentityChanged means the server's certificate chain does not
	// end in the pinned CA (CA rotated or an impostor). It is never trusted
	// automatically; the app must re-pair.
	ErrServerIdentityChanged = errors.New("bubblesclient: server identity changed: pinned CA fingerprint mismatch")
	// ErrRateLimited means HTTP 429.
	ErrRateLimited = errors.New("bubblesclient: rate limited")
	// ErrUnavailable means the server could not be reached or answered 5xx.
	ErrUnavailable = errors.New("bubblesclient: server unavailable")
)

// Pairing outcome errors.
var (
	ErrPairingDenied  = errors.New("bubblesclient: pairing request denied")
	ErrPairingExpired = errors.New("bubblesclient: pairing request expired")
	ErrPairingClaimed = errors.New("bubblesclient: pairing token was already delivered")
)

// errPinMismatch is returned from the TLS verification callbacks and mapped
// to ErrServerIdentityChanged by the request path.
var errPinMismatch = errors.New("pinned CA not found in the server's certificate chain")

// APIError is a non-2xx answer from the server's error model.
type APIError struct {
	Status     int
	Code       string
	Message    string
	Details    map[string]any
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bubbles: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// Is maps the HTTP status onto the sentinel errors.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case ErrUnavailable:
		return e.Status >= 500
	}
	return false
}

// FieldErrors returns the per-field validation messages, or nil.
func (e *APIError) FieldErrors() map[string]string {
	return (&protocol.Error{Code: e.Code, Message: e.Message, Details: e.Details}).FieldErrors()
}

// retryable reports whether err is worth retrying later.
func retryable(err error) bool {
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnavailable)
}

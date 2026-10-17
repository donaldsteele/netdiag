// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/alert.go.
// Modified for netdiag: the AlertTransition constants moved here from
// pkg/protocol/event.go (netdiag uses no WebSocket stream); the by-id
// resolve body and the alert list page are left out.

package protocol

// Limits of an AlertInput.
const (
	MaxAlertTitle      = 200
	MaxAlertText       = 4000
	MaxAlertFields     = 20
	MaxAlertFieldName  = 64
	MaxAlertFieldValue = 512
	MaxAlertDedupeKey  = 200
	MaxAlertLink       = 2048
)

// AlertInput is the body of POST /alerts. Channel is a channel name (with or
// without a leading #) or id. DedupeKey identifies the condition: a repeat
// with the same key updates the open alert instead of creating a new one.
// RenotifyAfterS defaults to the instance setting alert_renotify_s; 0 turns
// renotification off. OccurredAt (RFC 3339) is when the condition was seen;
// the server never records a time in the future.
type AlertInput struct {
	Channel        string       `json:"channel"`
	Severity       string       `json:"severity"`
	Title          string       `json:"title"`
	Text           string       `json:"text,omitempty"`
	Fields         []AlertField `json:"fields,omitempty"`
	Link           *string      `json:"link,omitempty"`
	DedupeKey      string       `json:"dedupe_key"`
	RenotifyAfterS *int         `json:"renotify_after_s,omitempty"`
	OccurredAt     *string      `json:"occurred_at,omitempty"`
}

// PostedAlert is the answer of POST /alerts: the alert's message and what
// the post did to the alert (an AlertTransition* constant). A new alert is
// 201 with transition created; a repeat is 200.
type PostedAlert struct {
	Message    Message `json:"message"`
	Transition string  `json:"transition"`
}

// Alert transitions: what a post did to its alert. Only created, escalated
// and renotify_due can notify; the others are informational.
const (
	AlertTransitionCreated      = "created"
	AlertTransitionEscalated    = "escalated"
	AlertTransitionRenotifyDue  = "renotify_due"
	AlertTransitionUpdatedOther = "updated_other"
	AlertTransitionAcked        = "acked"
	AlertTransitionResolved     = "resolved"
)

// ResolveAlertByKeyRequest is the body of POST /alerts/resolve.
type ResolveAlertByKeyRequest struct {
	DedupeKey string `json:"dedupe_key"`
	Text      string `json:"text,omitempty"`
}

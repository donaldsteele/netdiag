// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/channel.go.
// Modified for netdiag: memberships, unread counts and the bootstrap snapshot
// are left out, and ChannelList comes from pkg/protocol/requests.go.

package protocol

// Channel kinds.
const (
	ChannelPublic  = "public"
	ChannelPrivate = "private"
	ChannelDM      = "dm"
	ChannelGroupDM = "group_dm"
)

// Alert severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Channel is the wire view of a channel row. Name is null for direct
// messages; ParticipantIDs is set for dm and group_dm only (a note-to-self
// has exactly one participant).
type Channel struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Name           *string  `json:"name"`
	Topic          string   `json:"topic"`
	Description    string   `json:"description"`
	IsDefault      bool     `json:"is_default"`
	RetentionDays  *int     `json:"retention_days"`
	CreatedBy      *string  `json:"created_by"`
	CreatedAt      string   `json:"created_at"`
	ArchivedAt     *string  `json:"archived_at"`
	MemberCount    int      `json:"member_count"`
	ParticipantIDs []string `json:"participant_ids,omitempty"`
}

// ChannelList is the body of GET /channels.
type ChannelList struct {
	Items []Channel `json:"items"`
}

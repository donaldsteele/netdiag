// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/message.go.
// Modified for netdiag: threads, pins and message pages are left out; File
// comes from pkg/protocol/file.go, CreateMessageRequest from
// pkg/protocol/requests.go and the NewMessage alias from pkg/protocol/sdk.go.

package protocol

// Message kinds.
const (
	MessageText   = "text"
	MessageAlert  = "alert"
	MessageSystem = "system"
)

// Alert states.
const (
	AlertOpen     = "open"
	AlertAcked    = "acked"
	AlertResolved = "resolved"
)

// Author is the sender of a message. Accounts are deactivated, never
// deleted, so the author is always present.
type Author struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Kind        string  `json:"kind"`
	AvatarURL   *string `json:"avatar_url"`
}

// Reaction is the aggregate of one emoji on a message.
type Reaction struct {
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	UserIDs []string `json:"user_ids"`
}

// Mentions lists who a message mentions.
type Mentions struct {
	UserIDs []string `json:"user_ids"`
	Channel bool     `json:"channel"`
	Here    bool     `json:"here"`
}

// AlertField is one name/value pair of an alert.
type AlertField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// Alert is the alert state of a kind=alert message.
type Alert struct {
	Severity    string       `json:"severity"`
	State       string       `json:"state"`
	Title       string       `json:"title"`
	Fields      []AlertField `json:"fields"`
	Link        *string      `json:"link"`
	DedupeKey   string       `json:"dedupe_key"`
	Occurrences int          `json:"occurrences"`
	FirstSeenAt string       `json:"first_seen_at"`
	LastSeenAt  string       `json:"last_seen_at"`
	AckedBy     *string      `json:"acked_by"`
	AckedAt     *string      `json:"acked_at"`
	ResolvedBy  *string      `json:"resolved_by"`
	ResolvedAt  *string      `json:"resolved_at"`
}

// Message is the wire view of a chat message. A deleted message keeps its
// row as a tombstone: Deleted is true, Body is empty and Files, Reactions and
// Mentions are empty.
type Message struct {
	ID           string     `json:"id"`
	ChannelID    string     `json:"channel_id"`
	ThreadRootID *string    `json:"thread_root_id"`
	Kind         string     `json:"kind"`
	Author       Author     `json:"author"`
	OverrideName *string    `json:"override_name"`
	Body         string     `json:"body"`
	Files        []File     `json:"files"`
	Reactions    []Reaction `json:"reactions"`
	Mentions     Mentions   `json:"mentions"`
	ReplyCount   int        `json:"reply_count"`
	LastReplyAt  *string    `json:"last_reply_at"`
	EditedAt     *string    `json:"edited_at"`
	Deleted      bool       `json:"deleted"`
	CreatedAt    string     `json:"created_at"`
	Alert        *Alert     `json:"alert"`
}

// File is an uploaded file, as an element of Message.Files. Name is the
// sanitized original file name. MetaStripped is false for files whose
// metadata could not be removed (HEIC, video, audio, documents and images
// that were not processed).
type File struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Mime         string  `json:"mime"`
	Category     string  `json:"category"`
	Width        *int    `json:"width"`
	Height       *int    `json:"height"`
	HasThumb     bool    `json:"has_thumb"`
	MetaStripped bool    `json:"meta_stripped"`
	URL          string  `json:"url"`
	ThumbURL     *string `json:"thumb_url"`
}

// CreateMessageRequest is the body of POST /channels/{id}/messages. A repeated
// nonce returns the message it created first.
type CreateMessageRequest struct {
	Body         string   `json:"body"`
	ThreadRootID *string  `json:"thread_root_id,omitempty"`
	FileIDs      []string `json:"file_ids,omitempty"`
	Nonce        string   `json:"nonce,omitempty"`
}

// NewMessage is the body of POST /api/v1/channels/{id}/messages, under the
// name the Go SDK contract uses.
type NewMessage = CreateMessageRequest

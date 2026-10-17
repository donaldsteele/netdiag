// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/protocol/app.go.
// Modified for netdiag: the admin-side app and token requests are left out.

package protocol

// App token scopes. A token carries a subset of these; every endpoint an app
// calls checks the scope it needs.
const (
	ScopeMessagesRead  = "messages:read"
	ScopeMessagesWrite = "messages:write"
	ScopeAlertsWrite   = "alerts:write"
	ScopeFilesWrite    = "files:write"
	ScopeChannelsRead  = "channels:read"
	ScopeEventsRead    = "events:read"
	ScopeCommands      = "commands"
	ScopeDMWrite       = "dm:write"
	ScopeMCP           = "mcp"
)

// AppScopes lists every scope a token can carry.
var AppScopes = []string{
	ScopeMessagesRead, ScopeMessagesWrite, ScopeAlertsWrite, ScopeFilesWrite,
	ScopeChannelsRead, ScopeEventsRead, ScopeCommands, ScopeDMWrite, ScopeMCP,
}

// AppTokenPrefix starts every app token.
const AppTokenPrefix = "bub_"

// AppToken is the public view of an app token. The secret itself is shown
// once, when the token is issued; afterwards only Prefix identifies it.
// ChannelIDs null means the channels the app's bot is a member of; a list
// restricts the token to those channels.
type AppToken struct {
	ID         string   `json:"id"`
	AppID      string   `json:"app_id"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	ChannelIDs []string `json:"channel_ids"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  *string  `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// App is an integration: a bot account plus its tokens. Tokens lists every
// token of the app, revoked ones included, in the admin API and is empty in
// GET /apps/self.
type App struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Username    string     `json:"username"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	OwnerUserID *string    `json:"owner_user_id"`
	CreatedAt   string     `json:"created_at"`
	DisabledAt  *string    `json:"disabled_at"`
	Tokens      []AppToken `json:"tokens"`
}

// AppSelf is the body of GET /apps/self: the calling token's app and the
// token itself.
type AppSelf struct {
	App   App      `json:"app"`
	Token AppToken `json:"token"`
}

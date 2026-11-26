// Package app implements SCP's operations (spec §5) over the domain rules and a Store.
package app

import (
	"time"

	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

// Version is the SCP protocol version this server implements.
const Version = "0.4"

// Resource is something a team works on (spec §4.7).
type Resource struct {
	URI   string `json:"uri"`
	Label string `json:"label,omitempty"`
}

// Reference points outside the server (spec §4.9).
type Reference struct {
	Type  string `json:"type"`
	URI   string `json:"uri"`
	Title string `json:"title,omitempty"`
}

// AgentInfo is self-declared by the client and never used for authorization (spec §4.12).
type AgentInfo struct {
	Client        string `json:"client"`
	ClientVersion string `json:"client_version,omitempty"`
	Model         string `json:"model,omitempty"`
}

// MemberRef identifies a member in results (spec §4.11).
type MemberRef struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
}

// Provenance records who wrote an object and when (spec §4.10).
type Provenance struct {
	Author    MemberRef  `json:"author"`
	Agent     *AgentInfo `json:"agent,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	UpdatedBy MemberRef  `json:"updated_by"`
}

// Settings are a workspace's settings (spec §4.1.1).
type Settings struct {
	NoteTTLDays      int               `json:"note_ttl_days"`
	NoteWarnDays     int               `json:"note_warn_days"`
	IntentTTLHours   int               `json:"intent_ttl_hours"`
	HandoffTTLDays   int               `json:"handoff_ttl_days"`
	DefaultResources []Resource        `json:"default_resources"`
	ResourceAliases  map[string]string `json:"resource_aliases"`
	SecretDetectors  SecretSettings    `json:"secret_detectors"`
}

// SecretSettings are per-detector modes and allow-list patterns (spec §9.1).
type SecretSettings struct {
	Modes     map[string]string `json:"modes"`
	AllowList []string          `json:"allow_list"`
}

// DefaultSettings are the defaults of spec §4.1.1.
func DefaultSettings() Settings {
	return Settings{
		NoteTTLDays: 30, NoteWarnDays: 5, IntentTTLHours: 24, HandoffTTLDays: 14,
		DefaultResources: []Resource{}, ResourceAliases: map[string]string{},
		SecretDetectors: SecretSettings{Modes: map[string]string{}, AllowList: []string{}},
	}
}

// Workspace is an isolated team context (spec §4.1).
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Settings  Settings  `json:"settings"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

// Member is a person in a workspace (spec §4.2).
type Member struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"-"`
	DisplayName string    `json:"display_name"`
	Handle      string    `json:"handle"`
	Role        string    `json:"role"`
	Active      bool      `json:"active"`
	Revision    int       `json:"revision"`
	JoinedAt    time.Time `json:"joined_at"`
}

// Ref returns m as a MemberRef.
func (m Member) Ref() MemberRef {
	return MemberRef{ID: m.ID, Handle: m.Handle, DisplayName: m.DisplayName}
}

// Note is a short fact the team should know (spec §4.3).
type Note struct {
	ID                string      `json:"id"`
	WorkspaceID       string      `json:"-"`
	Kind              string      `json:"kind"`
	Body              string      `json:"body"`
	Resources         []Resource  `json:"resources"`
	Refs              []Reference `json:"refs"`
	Status            string      `json:"status"`
	PromotedTo        *Reference  `json:"promoted_to,omitempty"`
	PromotionVerified *bool       `json:"promotion_verified,omitempty"`
	Renewals          int         `json:"renewals"`
	Revision          int         `json:"revision"`
	ExpiresAt         time.Time   `json:"expires_at"`
	Provenance        Provenance  `json:"provenance"`
	Seq               int64       `json:"-"`
}

// Dependency is something an intent relies on without changing (spec §4.8.1).
type Dependency struct {
	Resource Resource       `json:"resource"`
	Scopes   []domain.Scope `json:"scopes"`
}

// Intent declares that a member is about to change a scope (spec §4.4).
type Intent struct {
	ID           string         `json:"id"`
	WorkspaceID  string         `json:"-"`
	Resource     Resource       `json:"resource"`
	Scopes       []domain.Scope `json:"scopes"`
	Action       domain.Action  `json:"action"`
	Uses         []Dependency   `json:"uses"`
	Summary      string         `json:"summary"`
	Work         *Reference     `json:"work,omitempty"`
	Session      string         `json:"session,omitempty"`
	Status       string         `json:"status"`
	HeartbeatAt  time.Time      `json:"heartbeat_at"`
	LeaseMinutes int            `json:"lease_minutes,omitempty"`
	ExpiresAt    time.Time      `json:"expires_at"`
	ClosedAt     *time.Time     `json:"closed_at,omitempty"`
	Revision     int            `json:"revision"`
	Provenance   Provenance     `json:"provenance"`
	Seq          int64          `json:"-"`
}

// View is the intent as overlap detection sees it.
func (i Intent) View() domain.IntentView {
	v := domain.IntentView{
		ID: i.ID, Member: i.Provenance.Author.ID, Session: i.Session, Action: i.Action,
		Change: domain.Side{Resource: i.Resource.URI, Scopes: i.Scopes},
	}
	for _, u := range i.Uses {
		v.Uses = append(v.Uses, domain.Side{Resource: u.Resource.URI, Scopes: u.Scopes})
	}
	return v
}

// Untrusted wraps another member's or system's content (spec §8.2).
type Untrusted struct {
	Untrusted bool       `json:"untrusted"`
	Source    string     `json:"source"`
	Author    *MemberRef `json:"author,omitempty"`
	System    string     `json:"system,omitempty"`
	Value     any        `json:"value"`
}

// Warning is a non-blocking notice on a result (spec §5.5.4).
type Warning struct {
	Code   string        `json:"code"`
	Scope  *domain.Scope `json:"scope,omitempty"`
	Detail string        `json:"detail,omitempty"`
}

// Principal is the authenticated member a request runs as (spec §10.1).
type Principal struct {
	Member  Member
	ToolSet string
}

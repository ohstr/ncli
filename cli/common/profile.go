package common

import "encoding/json"

// ProfileMetadata is a kind:0 event's JSON content document -- the fields
// NIP-01 and the surrounding NIPs actually put there. Shared rather than
// redeclared per command, so "profile" and the bunker TUI agree on what a
// profile is and a new field only has to be added once.
//
// Every field is optional: a kind:0 is free-form JSON and publishers fill in
// whatever they like, so callers must treat "" as "not published" rather than
// as an error.
type ProfileMetadata struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	About       string `json:"about,omitempty"`
	Picture     string `json:"picture,omitempty"`
	Banner      string `json:"banner,omitempty"`
	Website     string `json:"website,omitempty"`
	Nip05       string `json:"nip05,omitempty"`
	// Lud16 is a lightning address ("name@domain"); Lud06 is a raw bech32
	// LNURL. A profile may carry either, both, or neither.
	Lud16 string `json:"lud16,omitempty"`
	Lud06 string `json:"lud06,omitempty"`
}

// ParseProfileMetadata decodes a kind:0 event's content.
func ParseProfileMetadata(content string) (*ProfileMetadata, error) {
	var meta ProfileMetadata
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Display returns the best human-facing label this profile offers --
// display_name, then name, then nip05 -- or "" if it offers none.
func (p *ProfileMetadata) Display() string {
	if p == nil {
		return ""
	}
	if p.DisplayName != "" {
		return p.DisplayName
	}
	if p.Name != "" {
		return p.Name
	}
	return p.Nip05
}

// LightningAddress returns the profile's lightning destination, preferring the
// human-readable lud16 address over a raw lud06 LNURL, and "" if it has
// neither.
func (p *ProfileMetadata) LightningAddress() string {
	if p == nil {
		return ""
	}
	if p.Lud16 != "" {
		return p.Lud16
	}
	return p.Lud06
}

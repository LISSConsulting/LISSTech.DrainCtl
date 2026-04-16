//go:build windows

package drainctl

import (
	"errors"
	"fmt"
	"strings"
)

// NotifyTargetUpdate describes a partial update to a single NotificationTarget.
// Pointer fields use nil to mean "preserve the existing value" so that callers
// can change one attribute without re-supplying the rest.
//
// TargetIndex selects which target of Type to update. It counts only targets of
// the same Type — index 1 with Type="webhook" addresses the second webhook,
// regardless of any ntfy or email targets in the slice. An index past the end
// of the matching subset appends a new target.
type NotifyTargetUpdate struct {
	Type          string     `json:"type"`
	URL           string     `json:"url"`
	TargetIndex   int        `json:"target_index"`
	Secret        *string    `json:"secret,omitempty"`
	From          *string    `json:"from,omitempty"`
	To            *[]string  `json:"to,omitempty"`
	Triggers      *[]Trigger `json:"triggers,omitempty"`
	RepeatMinutes *int       `json:"repeat_minutes,omitempty"`
}

// validNotifyTypes lists the accepted values for NotifyTargetUpdate.Type and
// the typ argument to RemoveNotifyTarget.
var validNotifyTypes = map[string]bool{
	"webhook": true,
	"ntfy":    true,
	"email":   true,
}

// SetNotifyTarget applies u to cfg in place. It does NOT call SaveConfig — the
// caller is responsible for persisting the change so that mutations can be
// composed before a single atomic write.
//
// If a target of u.Type exists at u.TargetIndex (counted within targets of that
// type), its fields are overwritten where u's pointer fields are non-nil. URL
// always overwrites. If u.TargetIndex equals or exceeds the count of matching
// targets, a new target is appended with DefaultTriggers when no Triggers
// override is supplied.
//
// For email targets, the proposed final state is validated before mutation:
// the URL must use smtp(s):// scheme and From + at least one To address must
// be present. Without this gate the downstream Validate() in SaveConfig would
// silently clear the URL, leaving the caller convinced the target was saved.
func SetNotifyTarget(cfg *Config, u NotifyTargetUpdate) error {
	if cfg == nil {
		return errors.New("cfg is nil")
	}
	if !validNotifyTypes[u.Type] {
		return fmt.Errorf("invalid type %q (valid: email, ntfy, webhook)", u.Type)
	}
	if strings.TrimSpace(u.URL) == "" {
		return errors.New("url is required")
	}
	if u.TargetIndex < 0 {
		return fmt.Errorf("target_index must be >= 0, got %d", u.TargetIndex)
	}

	matchingPositions := indicesOfType(cfg.Notifications, u.Type)
	appending := u.TargetIndex >= len(matchingPositions)

	// Build the proposed final target state from existing values + the update.
	// We validate this proposal before touching cfg so failed updates leave the
	// config exactly as the caller passed it in.
	var proposed NotificationTarget
	if appending {
		proposed = NotificationTarget{Type: u.Type}
	} else {
		proposed = cfg.Notifications[matchingPositions[u.TargetIndex]]
	}
	proposed.URL = u.URL
	applyUpdate(&proposed, u)
	if len(proposed.Triggers) == 0 {
		proposed.Triggers = append([]Trigger{}, DefaultTriggers...)
	}

	if u.Type == "email" {
		if err := validateEmailTarget(&proposed); err != nil {
			return err
		}
	}

	if appending {
		cfg.Notifications = append(cfg.Notifications, proposed)
	} else {
		cfg.Notifications[matchingPositions[u.TargetIndex]] = proposed
	}
	return nil
}

// validateEmailTarget enforces email-target invariants that SaveConfig would
// otherwise silently fix up by clearing the URL.
func validateEmailTarget(t *NotificationTarget) error {
	lower := strings.ToLower(t.URL)
	if !strings.HasPrefix(lower, "smtp://") && !strings.HasPrefix(lower, "smtps://") {
		return fmt.Errorf("email url must use smtp:// or smtps:// scheme, got %q", t.URL)
	}
	if t.From == "" {
		return errors.New("email target requires a 'from' address")
	}
	if len(t.To) == 0 {
		return errors.New("email target requires at least one 'to' address")
	}
	return nil
}

// RemoveNotifyTarget deletes the target of typ at the given 0-based index
// (counted within targets of that type). It does NOT call SaveConfig.
func RemoveNotifyTarget(cfg *Config, typ string, index int) error {
	if cfg == nil {
		return errors.New("cfg is nil")
	}
	if !validNotifyTypes[typ] {
		return fmt.Errorf("invalid type %q (valid: email, ntfy, webhook)", typ)
	}
	if index < 0 {
		return fmt.Errorf("target_index must be >= 0, got %d", index)
	}
	matchingPositions := indicesOfType(cfg.Notifications, typ)
	if index >= len(matchingPositions) {
		return fmt.Errorf("target_index %d out of range: %d %s target(s) configured", index, len(matchingPositions), typ)
	}
	pos := matchingPositions[index]
	cfg.Notifications = append(cfg.Notifications[:pos], cfg.Notifications[pos+1:]...)
	return nil
}

// indicesOfType returns the absolute positions in targets where Type == typ,
// preserving order. Used to translate a per-type index into a slice index.
func indicesOfType(targets []NotificationTarget, typ string) []int {
	out := make([]int, 0, len(targets))
	for i, t := range targets {
		if t.Type == typ {
			out = append(out, i)
		}
	}
	return out
}

// applyUpdate overwrites fields on t where u's pointer fields are non-nil.
// URL is handled by the caller because new vs. existing targets need different
// defaulting behavior for Triggers.
func applyUpdate(t *NotificationTarget, u NotifyTargetUpdate) {
	if u.Secret != nil {
		t.Secret = *u.Secret
	}
	if u.From != nil {
		t.From = *u.From
	}
	if u.To != nil {
		t.To = append([]string{}, (*u.To)...)
	}
	if u.Triggers != nil {
		t.Triggers = append([]Trigger{}, (*u.Triggers)...)
	}
	if u.RepeatMinutes != nil {
		t.RepeatMinutes = *u.RepeatMinutes
	}
}

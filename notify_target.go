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

// AppendNotifyTarget appends a new notification target to cfg. Required fields
// must be supplied — there is no preserved-existing fallback. For email
// targets, the URL must use smtp(s):// scheme and From + at least one To
// address must be present.
//
// Use this for "add" operations where the operator is creating a brand-new
// target. For in-place updates of an existing target, use SetNotifyTarget.
//
// Does NOT call SaveConfig — the caller persists.
func AppendNotifyTarget(cfg *Config, u NotifyTargetUpdate) error {
	if cfg == nil {
		return errors.New("cfg is nil")
	}
	if !validNotifyTypes[u.Type] {
		return fmt.Errorf("invalid type %q (valid: email, ntfy, webhook)", u.Type)
	}
	if strings.TrimSpace(u.URL) == "" {
		return errors.New("url is required")
	}

	t := NotificationTarget{Type: u.Type, URL: u.URL}
	applyUpdate(&t, u)
	if len(t.Triggers) == 0 {
		t.Triggers = append([]Trigger{}, DefaultTriggers...)
	}

	if u.Type == "email" {
		if err := validateEmailTarget(&t); err != nil {
			return err
		}
	}

	cfg.Notifications = append(cfg.Notifications, t)
	return nil
}

// SetNotifyTarget updates an existing notification target in place. Returns
// an error if no target of u.Type exists at u.TargetIndex — use
// AppendNotifyTarget to create new targets.
//
// Pointer fields in u that are nil preserve the existing value; URL always
// overwrites. Email targets are re-validated post-update so a partial change
// can't leave the target in an unusable state.
//
// Does NOT call SaveConfig.
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
	if u.TargetIndex >= len(matchingPositions) {
		return fmt.Errorf("no %s target at index %d (have %d) — use 'add-%s' to create a new one",
			u.Type, u.TargetIndex, len(matchingPositions), u.Type)
	}

	proposed := cfg.Notifications[matchingPositions[u.TargetIndex]]
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

	cfg.Notifications[matchingPositions[u.TargetIndex]] = proposed
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

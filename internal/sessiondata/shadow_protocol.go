package sessiondata

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ShadowProtocolScheme is the installer-registered protocol used to launch
	// the local, consent-preserving Remote Desktop shadow client.
	ShadowProtocolScheme = "drainctl-shadow"

	// MaxShadowProtocolURILength bounds untrusted protocol activation input.
	MaxShadowProtocolURILength = 2048
)

// ShadowTarget is the validated target encoded by a drainctl-shadow URI.
type ShadowTarget struct {
	Host      string
	SessionID uint32
}

// ShadowURI returns the single canonical protocol URI for a shadow target.
func ShadowURI(host string, sessionID uint32) (string, error) {
	if err := ValidateCanonicalHost(host); err != nil {
		return "", fmt.Errorf("invalid shadow host: %w", err)
	}
	return ShadowProtocolScheme + "://shadow?host=" + host + "&session=" + strconv.FormatUint(uint64(sessionID), 10), nil
}

// ParseShadowURI accepts only the canonical drainctl-shadow URI shape. It
// deliberately parses the raw string rather than normalizing it through
// net/url: protocol activation input must not gain meaning through decoding.
func ParseShadowURI(raw string) (ShadowTarget, error) {
	var target ShadowTarget
	if len(raw) == 0 || len(raw) > MaxShadowProtocolURILength {
		return target, fmt.Errorf("shadow URI length is invalid")
	}
	if strings.Contains(raw, "%") {
		return target, fmt.Errorf("shadow URI must not contain escapes")
	}

	const prefix = ShadowProtocolScheme + "://shadow?"
	if !strings.HasPrefix(raw, prefix) {
		return target, fmt.Errorf("shadow URI scheme, authority, or path is invalid")
	}
	query := strings.TrimPrefix(raw, prefix)
	if query == "" || strings.ContainsAny(query, "#/;") {
		return target, fmt.Errorf("shadow URI query is invalid")
	}

	parts := strings.Split(query, "&")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "host=") || !strings.HasPrefix(parts[1], "session=") {
		return target, fmt.Errorf("shadow URI query is invalid")
	}
	host := strings.TrimPrefix(parts[0], "host=")
	session := strings.TrimPrefix(parts[1], "session=")
	if host == "" || session == "" {
		return target, fmt.Errorf("shadow URI requires host and session")
	}
	if err := ValidateCanonicalHost(host); err != nil {
		return target, fmt.Errorf("invalid shadow host: %w", err)
	}
	id, err := strconv.ParseUint(session, 10, 32)
	if err != nil || session != strconv.FormatUint(id, 10) {
		return target, fmt.Errorf("shadow session is not a canonical uint32")
	}
	target.Host = host
	target.SessionID = uint32(id)
	return target, nil
}

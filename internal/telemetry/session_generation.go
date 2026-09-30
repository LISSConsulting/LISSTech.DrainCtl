//go:build windows

package telemetry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

// ReserveSessionInstanceID durably reserves the next UUIDv7 generation for a
// canonical host. The reservation survives snapshot retention and privacy
// purges, so a restarted agent cannot reuse an older generation.
func ReserveSessionInstanceID(ctx context.Context, db *DB, host string, now time.Time) (string, error) {
	if db == nil || db.writer == nil {
		return "", errSessionStoreReadOnly
	}
	if err := sessiondata.ValidateCanonicalHost(host); err != nil {
		return "", fmt.Errorf("telemetry: invalid session host: %w", err)
	}

	tx, err := db.writer.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("telemetry: reserve session generation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored []byte
	err = tx.QueryRowContext(ctx, `SELECT max_instance_id FROM session_generation_fence WHERE canonical_host=?`, host).Scan(&stored)
	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("telemetry: read session generation fence: %w", err)
	}
	var maximum *sessiondata.UUIDv7
	if err == nil {
		if len(stored) != 16 {
			return "", fmt.Errorf("telemetry: invalid stored session generation fence")
		}
		var value sessiondata.UUIDv7
		copy(value[:], stored)
		if _, err := sessiondata.ParseUUIDv7(value.String()); err != nil {
			return "", fmt.Errorf("telemetry: invalid stored session generation fence")
		}
		maximum = &value
	}

	candidate, err := newSessionUUIDv7(now)
	if err != nil {
		return "", err
	}
	if maximum != nil && sessiondata.CompareUUIDv7(candidate, *maximum) <= 0 {
		candidate, err = nextSessionUUIDv7(*maximum)
		if err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_generation_fence(canonical_host,max_instance_id) VALUES(?,?) ON CONFLICT(canonical_host) DO UPDATE SET max_instance_id=excluded.max_instance_id`, host, candidate[:]); err != nil {
		return "", fmt.Errorf("telemetry: reserve session generation fence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("telemetry: commit session generation reservation: %w", err)
	}
	return candidate.String(), nil
}

func newSessionUUIDv7(now time.Time) (sessiondata.UUIDv7, error) {
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || milliseconds >= 1<<48 {
		return sessiondata.UUIDv7{}, fmt.Errorf("telemetry: session generation timestamp out of range")
	}
	var value sessiondata.UUIDv7
	if _, err := rand.Read(value[:]); err != nil {
		return sessiondata.UUIDv7{}, fmt.Errorf("telemetry: random session generation: %w", err)
	}
	value[0] = byte(milliseconds >> 40)
	value[1] = byte(milliseconds >> 32)
	value[2] = byte(milliseconds >> 24)
	value[3] = byte(milliseconds >> 16)
	value[4] = byte(milliseconds >> 8)
	value[5] = byte(milliseconds)
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return value, nil
}

// nextSessionUUIDv7 returns the next UUIDv7 value while preserving UUIDv7's
// fixed version and variant bits.
func nextSessionUUIDv7(value sessiondata.UUIDv7) (sessiondata.UUIDv7, error) {
	result := value
	for _, index := range [...]int{15, 14, 13, 12, 11, 10, 9} {
		if result[index] != 0xff {
			result[index]++
			return result, nil
		}
		result[index] = 0
	}
	if result[8]&0x3f != 0x3f {
		result[8]++
		return result, nil
	}
	result[8] = 0x80
	if result[7] != 0xff {
		result[7]++
		return result, nil
	}
	result[7] = 0
	if result[6]&0x0f != 0x0f {
		result[6]++
		return result, nil
	}
	result[6] = 0x70
	for index := 5; index >= 0; index-- {
		if result[index] != 0xff {
			result[index]++
			return result, nil
		}
		result[index] = 0
	}
	return sessiondata.UUIDv7{}, fmt.Errorf("telemetry: session generation UUIDv7 exhausted")
}

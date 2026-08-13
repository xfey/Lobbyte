// Package textcheck provides objective text validation shared by client and Relay.
package textcheck

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const (
	MaxGraphemes  = 280
	MaxLines      = 8
	MaxTextBytes  = 8 * 1024
	MaxInputBytes = 64 * 1024
	BurstMessages = 5
)

const Cooldown = 10 * time.Second

var (
	ErrEmpty        = errors.New("message is empty")
	ErrInvalidUTF8  = errors.New("message is not valid UTF-8")
	ErrTooLong      = errors.New("message is too long")
	ErrTooManyLines = errors.New("message has too many lines")
	ErrControl      = errors.New("message contains a disallowed control character")
	ErrCoolingDown  = errors.New("message rate limit is cooling down")
)

// ValidateMessage enforces the exact objective constraints shared by both peers.
func ValidateMessage(text string) error {
	if err := ValidateCharacters(text); err != nil {
		return err
	}
	if len(text) > MaxTextBytes {
		return fmt.Errorf("%w: maximum is %d UTF-8 bytes", ErrTooLong, MaxTextBytes)
	}
	if strings.TrimSpace(text) == "" {
		return ErrEmpty
	}
	if strings.Count(text, "\n")+1 > MaxLines {
		return fmt.Errorf("%w: maximum is %d", ErrTooManyLines, MaxLines)
	}
	if uniseg.GraphemeClusterCount(text) > MaxGraphemes {
		return fmt.Errorf("%w: maximum is %d grapheme clusters", ErrTooLong, MaxGraphemes)
	}
	return nil
}

// ValidateCharacters rejects byte sequences and terminal control characters
// without applying message length, line-count, or whitespace rules. The TUI
// uses it before inserting user input so a paste is either kept intact or
// rejected intact instead of being silently sanitized by a widget.
func ValidateCharacters(text string) error {
	if !utf8.ValidString(text) {
		return ErrInvalidUTF8
	}
	for _, value := range text {
		if disallowedControl(value) {
			return fmt.Errorf("%w: U+%04X", ErrControl, value)
		}
	}
	return nil
}

func disallowedControl(value rune) bool {
	if value == '\n' {
		return false
	}
	if value <= 0x1f || (value >= 0x7f && value <= 0x9f) {
		return true
	}
	switch value {
	case 0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}

// Rate tracks one identity's accepted-message cooldown across rooms.
type Rate struct {
	accepted     int
	cooldownEnds time.Time
}

// Check reports whether a message may be accepted without changing the rate state.
func (rate Rate) Check(now time.Time) error {
	if !rate.cooldownEnds.IsZero() && now.Before(rate.cooldownEnds) {
		return fmt.Errorf("%w until %s", ErrCoolingDown, rate.cooldownEnds.Format(time.RFC3339Nano))
	}
	return nil
}

// Accept records a successfully accepted message.
func (rate *Rate) Accept(now time.Time) {
	if !rate.cooldownEnds.IsZero() && !now.Before(rate.cooldownEnds) {
		rate.accepted = 0
		rate.cooldownEnds = time.Time{}
	}
	rate.accepted++
	if rate.accepted == BurstMessages {
		rate.cooldownEnds = now.Add(Cooldown)
	}
}

// CooldownEnds returns the current cooldown deadline, if any.
func (rate Rate) CooldownEnds() time.Time {
	return rate.cooldownEnds
}

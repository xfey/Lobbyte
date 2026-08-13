package textcheck

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateMessage(t *testing.T) {
	t.Parallel()
	exactByteLimit := "a" + strings.Repeat("\u0301", 4094) + "\u20dd"

	tests := []struct {
		name string
		text string
		err  error
	}{
		{name: "plain", text: "hello"},
		{name: "emoji sequence", text: strings.Repeat("👨‍💻", MaxGraphemes)},
		{name: "combining sequence", text: strings.Repeat("e\u0301", MaxGraphemes)},
		{name: "eight lines", text: strings.Repeat("line\n", MaxLines-1) + "line"},
		{name: "empty", text: " \n ", err: ErrEmpty},
		{name: "too many graphemes", text: strings.Repeat("a", MaxGraphemes+1), err: ErrTooLong},
		{name: "too many lines", text: strings.Repeat("line\n", MaxLines), err: ErrTooManyLines},
		{name: "tab", text: "hello\tworld", err: ErrControl},
		{name: "carriage return", text: "hello\rworld", err: ErrControl},
		{name: "ansi", text: "\x1b[31mred", err: ErrControl},
		{name: "bidi", text: "hello\u202eworld", err: ErrControl},
		{name: "eight kibibytes", text: exactByteLimit},
		{name: "above byte limit", text: exactByteLimit + "\u0301", err: ErrTooLong},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMessage(test.text)
			if !errors.Is(err, test.err) {
				t.Fatalf("ValidateMessage() error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestRateStartsCooldownAfterBurstIsAccepted(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	var rate Rate
	for range BurstMessages {
		if err := rate.Check(now); err != nil {
			t.Fatalf("Check rejected a burst message: %v", err)
		}
		rate.Accept(now)
	}
	if !errors.Is(rate.Check(now), ErrCoolingDown) {
		t.Fatalf("Check accepted message %d during cooldown", BurstMessages+1)
	}
	if err := rate.Check(now.Add(Cooldown)); err != nil {
		t.Fatalf("Check rejected a message after cooldown: %v", err)
	}
}

func FuzzValidateMessage(f *testing.F) {
	f.Add("hello")
	f.Add("👨‍💻\nhello")
	f.Fuzz(func(t *testing.T, text string) {
		_ = ValidateMessage(text)
	})
}

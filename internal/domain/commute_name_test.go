package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewCommuteNameNormalizesAndValidatesText(t *testing.T) {
	name, err := NewCommuteName("  Retour à la maison  ")
	if err != nil {
		t.Fatalf("NewCommuteName() error = %v", err)
	}
	if got, want := name.Text(), "Retour à la maison"; got != want {
		t.Errorf("CommuteName.Text() = %q, want %q", got, want)
	}

	for _, text := range []string{
		" \t\n ",
		string([]byte{0xff}),
		strings.Repeat("é", 81),
	} {
		_, err := NewCommuteName(text)
		if !errors.Is(err, ErrInvalidCommuteName) {
			t.Errorf("NewCommuteName(%q) error = %v, want ErrInvalidCommuteName", text, err)
		}
	}

	name, err = NewCommuteName(strings.Repeat("é", 80))
	if err != nil {
		t.Fatalf("NewCommuteName(80 runes) error = %v", err)
	}
	if got, want := name.Text(), strings.Repeat("é", 80); got != want {
		t.Errorf("CommuteName.Text() = %q, want %q", got, want)
	}
}

package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestGeneratePasswordLengthBounds(t *testing.T) {
	cases := []struct {
		name   string
		length int
		want   int
	}{
		{"zero means the default", 0, DefaultPasswordLength},
		{"at the floor", MinPasswordLength, MinPasswordLength},
		{"at the ceiling", MaxPasswordLength, MaxPasswordLength},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GeneratePassword(tc.length, false)
			if err != nil {
				t.Fatalf("GeneratePassword(%d): %v", tc.length, err)
			}
			if len(got) != tc.want {
				t.Fatalf("length = %d, want %d", len(got), tc.want)
			}
		})
	}

	for _, length := range []int{-1, 1, MinPasswordLength - 1, MaxPasswordLength + 1} {
		if _, err := GeneratePassword(length, false); !errors.Is(err, ErrPasswordLength) {
			t.Fatalf("GeneratePassword(%d) err = %v, want ErrPasswordLength", length, err)
		}
	}
}

func TestGeneratePasswordStaysInsideItsAlphabet(t *testing.T) {
	withoutSymbols, err := GeneratePassword(4096, false)
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if got := strings.Trim(string(withoutSymbols), passwordAlphanumeric); got != "" {
		t.Fatalf("value contains %q, which is outside the alphanumeric alphabet", got)
	}

	withSymbols, err := GeneratePassword(4096, true)
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if got := strings.Trim(string(withSymbols), passwordAlphanumeric+passwordSymbols); got != "" {
		t.Fatalf("value contains %q, which is outside the full alphabet", got)
	}
	// Over 4096 draws from an 80-character alphabet, seeing no symbol at all
	// would mean the symbol range was never being selected.
	if !strings.ContainsAny(string(withSymbols), passwordSymbols) {
		t.Fatal("no symbol appeared in 4096 characters, so symbols are unreachable")
	}
}

// The rejection sampling exists to keep the low characters of the alphabet from
// turning up more often than the rest. This will not catch a subtle bias, but
// it does catch the alphabet being truncated or a whole range going unused.
func TestGeneratePasswordCoversItsAlphabet(t *testing.T) {
	const alphabet = passwordAlphanumeric + passwordSymbols
	seen := map[byte]int{}
	for i := 0; i < 20; i++ {
		value, err := GeneratePassword(4096, true)
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		for _, b := range value {
			seen[b]++
		}
	}
	for i := 0; i < len(alphabet); i++ {
		if seen[alphabet[i]] == 0 {
			t.Fatalf("character %q never appeared across 81920 draws", alphabet[i])
		}
	}
}

func TestGeneratePasswordDoesNotRepeatItself(t *testing.T) {
	first, err := GeneratePassword(32, true)
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	second, err := GeneratePassword(32, true)
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if string(first) == string(second) {
		t.Fatal("two generated passwords are identical")
	}
}

func TestZeroClearsTheBuffer(t *testing.T) {
	value, err := GeneratePassword(32, true)
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	Zero(value)
	for i, b := range value {
		if b != 0 {
			t.Fatalf("byte %d = %d after Zero", i, b)
		}
	}
}

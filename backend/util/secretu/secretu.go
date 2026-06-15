package secretu

import (
	"fmt"
	"time"
)

type SecretValue interface {
	Key() string
	Reveal() (string, error)
	MustReveal() string
	Updated() time.Time
}

type Revealer interface {
	Reveal(name string) ([]byte, error)
}

type PlainSecretValue struct {
	V         string
	K         string
	UpdatedAt time.Time
}

func (v PlainSecretValue) Key() string {
	return v.K
}

func (v PlainSecretValue) Reveal() (string, error) {
	return v.V, nil
}

func (v PlainSecretValue) MustReveal() string {
	return v.V
}

func (v PlainSecretValue) Updated() time.Time {
	return v.UpdatedAt
}

type StoredSecretValue struct {
	K         string
	Revealer  Revealer
	UpdatedAt time.Time
}

func (v StoredSecretValue) Key() string {
	return v.K
}

func (v StoredSecretValue) Reveal() (string, error) {
	if v.K == "" || v.Revealer == nil {
		return "", nil
	}
	b, err := v.Revealer.Reveal(v.K)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (v StoredSecretValue) MustReveal() string {
	value, err := v.Reveal()
	if err != nil {
		panic(fmt.Sprintf("reveal secret %s: %v", v.K, err))
	}
	return value
}

func (v StoredSecretValue) Updated() time.Time {
	return v.UpdatedAt
}

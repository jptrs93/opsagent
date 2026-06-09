package secretu

import "fmt"

type SecretValue interface {
	Key() string
	Reveal() (string, error)
	MustReveal() string
}

type Revealer interface {
	Reveal(name string) ([]byte, error)
}

type PlainSecretValue struct {
	V string
	K string
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

type StoredSecretValue struct {
	K        string
	Revealer Revealer
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

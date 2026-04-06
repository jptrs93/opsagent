// genhash generates a random master password and prints it alongside its
// argon2id hash (suitable for OPSAGENT_MASTER_PASSWORD_HASH).
//
// Usage: go run ./cmd/genhash
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/jptrs93/goutil/authu"
)

func main() {
	secret := make([]byte, 64)
	if _, err := rand.Read(secret); err != nil {
		fmt.Fprintf(os.Stderr, "generating random bytes: %v\n", err)
		os.Exit(1)
	}
	password := base64.RawURLEncoding.EncodeToString(secret)

	hash, err := authu.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hashing password: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("password: %s\nhash:     %s\n", password, hash)
}

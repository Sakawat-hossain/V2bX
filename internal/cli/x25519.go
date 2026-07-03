package cli

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// X25519 generates an X25519 key pair and prints it, for protocols that use
// Reality/VLESS key pairs. Keys are printed in unpadded base64url, the
// encoding Reality clients and panels expect.
func X25519() error {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("x25519: generate key: %w", err)
	}
	enc := base64.RawURLEncoding
	fmt.Printf("Private key: %s\n", enc.EncodeToString(priv.Bytes()))
	fmt.Printf("Public key:  %s\n", enc.EncodeToString(priv.PublicKey().Bytes()))
	return nil
}

package update

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
)

const (
	checksumAssetName          = "SHA256SUMS"
	checksumSignatureAssetName = "SHA256SUMS.sig"
)

//go:embed release_signing_public_key.pem
var releaseSigningPublicKeyPEM []byte

func verifySignedChecksums(checksums, signature []byte) error {
	if len(checksums) == 0 {
		return fmt.Errorf("signed checksums are empty")
	}
	if len(signature) == 0 {
		return fmt.Errorf("release signature is empty")
	}

	block, _ := pem.Decode(releaseSigningPublicKeyPEM)
	if block == nil {
		return fmt.Errorf("release signing public key is invalid")
	}

	var publicKey *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse release signing public key: %w", err)
		}
		rsaKey, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("release signing public key is not RSA")
		}
		publicKey = rsaKey
	case "RSA PUBLIC KEY":
		rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse release signing public key: %w", err)
		}
		publicKey = rsaKey
	default:
		return fmt.Errorf("unsupported release signing public key type %q", block.Type)
	}

	sum := sha256.Sum256(checksums)
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, sum[:], signature); err != nil {
		return fmt.Errorf("release signature verification failed: %w", err)
	}
	return nil
}

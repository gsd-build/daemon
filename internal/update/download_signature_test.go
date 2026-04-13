package update

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadVerifiesSignedChecksums(t *testing.T) {
	privateKey, publicKeyPEM := generateReleaseSigningKey(t)
	origPublicKey := releaseSigningPublicKeyPEM
	releaseSigningPublicKeyPEM = publicKeyPEM
	t.Cleanup(func() {
		releaseSigningPublicKeyPEM = origPublicKey
	})

	assetName := AssetName("v9.9.9")
	binary := []byte("signed daemon binary")
	checksums := checksumManifest(t, assetName, binary)
	signature := signChecksums(t, privateKey, checksums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact":
			_, _ = w.Write(binary)
		case "/SHA256SUMS":
			_, _ = w.Write(checksums)
		case "/SHA256SUMS.sig":
			_, _ = w.Write(signature)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	release := &Release{
		TagName: "daemon/v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: server.URL + "/artifact"},
			{Name: checksumAssetName, BrowserDownloadURL: server.URL + "/SHA256SUMS"},
			{Name: checksumSignatureAssetName, BrowserDownloadURL: server.URL + "/SHA256SUMS.sig"},
		},
	}

	destPath := filepath.Join(t.TempDir(), "gsd-cloud")
	if err := Download(release, destPath); err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile(destPath) error: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("downloaded binary mismatch: got %q want %q", got, binary)
	}
}

func TestDownloadRejectsInvalidReleaseSignature(t *testing.T) {
	privateKey, publicKeyPEM := generateReleaseSigningKey(t)
	otherPrivateKey, _ := generateReleaseSigningKey(t)
	origPublicKey := releaseSigningPublicKeyPEM
	releaseSigningPublicKeyPEM = publicKeyPEM
	t.Cleanup(func() {
		releaseSigningPublicKeyPEM = origPublicKey
	})

	assetName := AssetName("v9.9.9")
	binary := []byte("signed daemon binary")
	checksums := checksumManifest(t, assetName, binary)
	signature := signChecksums(t, otherPrivateKey, checksums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact":
			_, _ = w.Write(binary)
		case "/SHA256SUMS":
			_, _ = w.Write(checksums)
		case "/SHA256SUMS.sig":
			_, _ = w.Write(signature)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	release := &Release{
		TagName: "daemon/v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: server.URL + "/artifact"},
			{Name: checksumAssetName, BrowserDownloadURL: server.URL + "/SHA256SUMS"},
			{Name: checksumSignatureAssetName, BrowserDownloadURL: server.URL + "/SHA256SUMS.sig"},
		},
	}

	destPath := filepath.Join(t.TempDir(), "gsd-cloud")
	err := Download(release, destPath)
	if err == nil {
		t.Fatal("expected Download() to reject invalid signature")
	}
	if got := err.Error(); got == "" || !containsString(got, "signature verification failed") {
		t.Fatalf("expected signature verification failure, got %v", err)
	}

	_ = privateKey
}

func generateReleaseSigningKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error: %v", err)
	}

	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("x509.MarshalPKIXPublicKey() error: %v", err)
	}

	publicPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicDER,
	})
	return privateKey, publicPEM
}

func checksumManifest(t *testing.T, assetName string, binary []byte) []byte {
	t.Helper()

	sum := sha256.Sum256(binary)
	return []byte(fmt.Sprintf("%x  %s\n", sum[:], assetName))
}

func signChecksums(t *testing.T, privateKey *rsa.PrivateKey, checksums []byte) []byte {
	t.Helper()

	sum := sha256.Sum256(checksums)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("rsa.SignPKCS1v15() error: %v", err)
	}
	return signature
}

func containsString(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && containsStringAt(s, substr))
}

func containsStringAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

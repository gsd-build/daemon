package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"sort"
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
	extensionAssetName := PiExtensionAssetName("v9.9.9")
	binary := []byte("signed daemon binary")
	extensionArchive := piExtensionArchive(t, map[string]string{
		"index.ts":           "export default {};",
		"usage-estimator.js": "export function applyUsageFromSdkMessage() {}",
	})
	checksums := checksumManifest(t, map[string][]byte{
		assetName:          binary,
		extensionAssetName: extensionArchive,
	})
	signature := signChecksums(t, privateKey, checksums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact":
			_, _ = w.Write(binary)
		case "/extension":
			_, _ = w.Write(extensionArchive)
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
			{Name: extensionAssetName, BrowserDownloadURL: server.URL + "/extension"},
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
	extensionPath := filepath.Join(filepath.Dir(destPath), "pi-extension", "index.ts")
	if _, err := os.Stat(extensionPath); err != nil {
		t.Fatalf("expected pi extension at %s: %v", extensionPath, err)
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
	extensionAssetName := PiExtensionAssetName("v9.9.9")
	binary := []byte("signed daemon binary")
	extensionArchive := piExtensionArchive(t, map[string]string{"index.ts": "export default {};"})
	checksums := checksumManifest(t, map[string][]byte{
		assetName:          binary,
		extensionAssetName: extensionArchive,
	})
	signature := signChecksums(t, otherPrivateKey, checksums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact":
			_, _ = w.Write(binary)
		case "/extension":
			_, _ = w.Write(extensionArchive)
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
			{Name: extensionAssetName, BrowserDownloadURL: server.URL + "/extension"},
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

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	archive := piExtensionArchive(t, map[string]string{"../escape": "bad"})
	archivePath := filepath.Join(t.TempDir(), "pi-extension.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0644); err != nil {
		t.Fatal(err)
	}

	err := extractTarGz(archivePath, t.TempDir())
	if err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if !containsString(err.Error(), "unsafe pi extension archive entry") {
		t.Fatalf("expected unsafe archive error, got %v", err)
	}
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

func checksumManifest(t *testing.T, assets map[string][]byte) []byte {
	t.Helper()

	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)

	var out bytes.Buffer
	for _, name := range names {
		sum := sha256.Sum256(assets[name])
		fmt.Fprintf(&out, "%x  %s\n", sum[:], name)
	}
	return out.Bytes()
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

func piExtensionArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		body := []byte(files[name])
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatalf("WriteHeader() error: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("Write() error: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close() error: %v", err)
	}
	return buf.Bytes()
}

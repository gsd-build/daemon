package claude

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDownloadURLRejectsPlainHTTP(t *testing.T) {
	if err := validateDownloadURL("http://example.com/file.png"); err == nil {
		t.Fatal("expected http URL to be rejected")
	}
}

func TestValidateDownloadURLRejectsPrivateAddress(t *testing.T) {
	if err := validateDownloadURL("https://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("expected private metadata IP to be rejected")
	}
}

func TestValidateDownloadURLRejectsLoopback(t *testing.T) {
	if err := validateDownloadURL("https://127.0.0.1/file.png"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
}

func TestValidateDownloadURLAllowsPublicHTTPS(t *testing.T) {
	if err := validateDownloadURL("https://8.8.8.8/file.png"); err != nil {
		t.Fatalf("expected public https URL to be allowed, got %v", err)
	}
}

func TestDownloadFileDialsValidatedIPAddressForHostname(t *testing.T) {
	origResolver := downloadHostResolver
	origDialContext := downloadDialContext
	t.Cleanup(func() {
		downloadHostResolver = origResolver
		downloadDialContext = origDialContext
	})

	downloadHostResolver = func(host string) ([]net.IP, error) {
		if host != "cdn.example.com" {
			t.Fatalf("unexpected host lookup %q", host)
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}

	var dialedAddr string
	downloadDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialedAddr = addr
		return nil, errors.New("dial blocked for test")
	}

	err := downloadFile(
		"https://cdn.example.com/image.png",
		filepath.Join(t.TempDir(), "image.png"),
	)
	if err == nil {
		t.Fatal("expected download to fail when dialer is blocked")
	}
	if !strings.Contains(err.Error(), "dial blocked for test") {
		t.Fatalf("expected error to mention dial failure, got %v", err)
	}
	if dialedAddr != "93.184.216.34:443" {
		t.Fatalf("expected dial to use validated IP, got %q", dialedAddr)
	}
}

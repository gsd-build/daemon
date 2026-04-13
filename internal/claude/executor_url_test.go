package claude

import "testing"

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

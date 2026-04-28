package preview

import "testing"

func TestNormalizeTargetAcceptsLoopback(t *testing.T) {
	cases := []struct {
		host     string
		port     int
		wantHost string
	}{
		{"localhost", 3000, "localhost"},
		{"127.0.0.1", 5173, "127.0.0.1"},
		{"::1", 8080, "::1"},
		{"[::1]", 8080, "::1"},
	}
	for _, tc := range cases {
		target, err := NormalizeTarget(tc.host, tc.port)
		if err != nil {
			t.Fatalf("NormalizeTarget(%q,%d): %v", tc.host, tc.port, err)
		}
		if target.Host != tc.wantHost {
			t.Fatalf("host = %q, want %q", target.Host, tc.wantHost)
		}
		if target.Port != tc.port {
			t.Fatalf("port = %d, want %d", target.Port, tc.port)
		}
	}
}

func TestNormalizeTargetRejectsUnsafeHosts(t *testing.T) {
	for _, host := range []string{
		"192.168.1.10",
		"10.0.0.5",
		"172.16.0.2",
		"169.254.169.254",
		"example.com",
		"",
	} {
		if _, err := NormalizeTarget(host, 3000); err == nil {
			t.Fatalf("NormalizeTarget(%q) succeeded, want error", host)
		}
	}
}

func TestNormalizeTargetRejectsInvalidPorts(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		if _, err := NormalizeTarget("localhost", port); err == nil {
			t.Fatalf("NormalizeTarget port %d succeeded, want error", port)
		}
	}
}

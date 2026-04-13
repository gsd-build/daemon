package upload

import "testing"

func TestIsImageFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"screenshot.png", true},
		{"photo.jpg", true},
		{"photo.JPEG", true},
		{"icon.gif", true},
		{"logo.svg", true},
		{"image.webp", true},
		{"code.go", false},
		{"data.json", false},
		{"readme.md", false},
		{"", false},
		{"/home/user/project/screenshot.PNG", true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := IsImageFile(tc.path); got != tc.want {
				t.Errorf("IsImageFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestWsToHTTPBase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "production wss with path",
			in:   "wss://relay.gsd.build/ws/daemon",
			want: "https://relay.gsd.build",
		},
		{
			name: "production wss with query",
			in:   "wss://relay.gsd.build/ws/daemon?machineId=abc",
			want: "https://relay.gsd.build",
		},
		{
			name: "local ws with port",
			in:   "ws://localhost:8080/ws/daemon",
			want: "http://localhost:8080",
		},
		{
			name: "no path",
			in:   "wss://relay.gsd.build",
			want: "https://relay.gsd.build",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wsToHTTPBase(tc.in); got != tc.want {
				t.Errorf("wsToHTTPBase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

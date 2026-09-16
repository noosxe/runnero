package server

import "testing"

// Tests for the device-label parser behind ListSessions (docs/32 §3.5).
// Labels are best-effort display metadata; the assertions pin the common
// browsers, the Edge/Opera-inside-Chrome precedence, CLI tools and the
// graceful "Unknown device" degradation.
func TestDeviceLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ua   string
		want string
	}{
		{
			"firefox linux",
			"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
			"Firefox 130 on Linux",
		},
		{
			"chrome macos",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
			"Chrome 130 on macOS",
		},
		{
			"edge windows wins over chrome",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
			"Edge 130 on Windows",
		},
		{
			"opera wins over chrome",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 OPR/115.0.0.0",
			"Opera 115 on Linux",
		},
		{
			"safari macos via version token",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
			"Safari 18 on macOS",
		},
		{
			"chrome on android",
			"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
			"Chrome 131 on Android",
		},
		{
			"safari on iphone",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
			"Safari 18 on iOS",
		},
		{
			"headless chrome (e2e)",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/131.0.0.0 Safari/537.36",
			"Chrome 131 on Linux",
		},
		{
			"curl",
			"curl/8.5.0",
			"curl 8.5.0",
		},
		{
			"python requests",
			"python-requests/2.31.0",
			"python-requests 2.31.0",
		},
		{
			"empty user agent",
			"",
			"Unknown device",
		},
		{
			"unparseable user agent",
			"mystery-bot/1.0",
			"Unknown device",
		},
		{
			"os only",
			"mystery-bot/1.0 (Linux)",
			"Unknown device on Linux",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DeviceLabel(tc.ua); got != tc.want {
				t.Fatalf("DeviceLabel(%q) = %q, want %q", tc.ua, got, tc.want)
			}
		})
	}
}

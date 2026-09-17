package config

import (
	"strings"
	"testing"
)

func TestWebAuthnDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	cfg.normalize()
	if cfg.WebAuthnEnabled() {
		t.Fatal("webauthn must be off with an empty rp-id")
	}
	if err := cfg.validateWebAuthn(); err != nil {
		t.Fatalf("empty rp-id must validate cleanly (feature off): %v", err)
	}
}

func TestWebAuthnOriginDefaultFilled(t *testing.T) {
	cfg := &Config{WebAuthnRPID: "runnero.example.ts.net"}
	cfg.normalize()
	if got := cfg.WebAuthnOriginList(); len(got) != 1 || got[0] != "https://runnero.example.ts.net" {
		t.Fatalf("origin list = %v, want [https://runnero.example.ts.net]", got)
	}
}

func TestWebAuthnValidateCases(t *testing.T) {
	valid := func(rpID, origins string) *Config {
		cfg := &Config{WebAuthnRPID: rpID, WebAuthnOrigins: origins}
		cfg.normalize()
		return cfg
	}

	cases := []struct {
		name    string
		cfg     *Config
		wantErr string // empty = valid
	}{
		{"exact origin", valid("runnero.example.ts.net", "https://runnero.example.ts.net"), ""},
		{"subdomain origin", valid("example.com", "https://a.example.com,https://example.com:8443"), ""},
		{"localhost carve-out", valid("localhost", "http://localhost:8090"), ""},
		{"explicit http origin", valid("runnero.lan", "http://runnero.lan:8090"), ""},
		{"foreign host", valid("example.com", "https://other.org"), "is not \"example.com\" or a subdomain"},
		{"bare hostname origin", valid("example.com", "example.com"), "want an absolute origin"},
		{"path in origin", valid("example.com", "https://example.com/ui"), "want a bare origin"},
		{"wildcard origin", valid("example.com", "https://*.example.com"), "wildcard"},
		{"bad scheme", valid("example.com", "ftp://example.com"), "scheme must be http or https"},
		{"ip rp-id", valid("100.64.0.1", ""), "IP address cannot be a Relying Party ID"},
		{"rp-id with scheme", valid("https://example.com", ""), "want a plain hostname"},
		{"rp-id with wildcard", valid("*.example.com", ""), "want a plain hostname"},
		{"origins stripped to empty", valid("example.com", " , ,"), "no origins are configured"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validateWebAuthn()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestWebAuthnOriginListTrims(t *testing.T) {
	cfg := &Config{WebAuthnRPID: "example.com", WebAuthnOrigins: " https://example.com , https://a.example.com ,"}
	cfg.normalize()
	got := cfg.WebAuthnOriginList()
	if len(got) != 2 || got[0] != "https://example.com" || got[1] != "https://a.example.com" {
		t.Fatalf("origin list = %v", got)
	}
}

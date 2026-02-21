package security

import (
	"errors"
	"testing"
)

func TestParseHTTPSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		wantErr error
	}{
		{
			name:    "empty",
			rawURL:  "",
			wantErr: ErrURLRequired,
		},
		{
			name:    "invalid",
			rawURL:  ":::",
			wantErr: ErrInvalidURL,
		},
		{
			name:    "missing host",
			rawURL:  "https:///repos/org/repo/releases/latest",
			wantErr: ErrInvalidURL,
		},
		{
			name:    "http rejected",
			rawURL:  "http://api.github.com/repos/org/repo/releases/latest",
			wantErr: ErrHTTPSOnly,
		},
		{
			name:   "https accepted",
			rawURL: "https://api.github.com/repos/org/repo/releases/latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseHTTPSURL(tt.rawURL)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if parsed == nil {
				t.Fatal("expected parsed URL")
			}
		})
	}
}

func TestParseHTTPSURLWithAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rawURL       string
		allowedHosts []string
		wantErr      error
	}{
		{
			name:         "allowlisted host",
			rawURL:       "https://api.github.com/repos/org/repo/releases/latest",
			allowedHosts: []string{"api.github.com"},
		},
		{
			name:         "allowlist case insensitive and trims",
			rawURL:       "https://API.GitHub.com/repos/org/repo/releases/latest",
			allowedHosts: []string{"  api.github.com  "},
		},
		{
			name:         "disallowed host rejected",
			rawURL:       "https://example.com/releases/latest",
			allowedHosts: []string{"api.github.com"},
			wantErr:      ErrUpstreamHostNotAllowed,
		},
		{
			name:         "empty allowlist allows all https hosts",
			rawURL:       "https://example.com/releases/latest",
			allowedHosts: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseHTTPSURLWithAllowlist(tt.rawURL, tt.allowedHosts)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if parsed == nil {
				t.Fatal("expected parsed URL")
			}
		})
	}
}

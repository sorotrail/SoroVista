package source

import (
	"strings"
	"testing"
)

func TestSanitizeDetailRemovesCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		leak []string
	}{
		{
			name: "url with userinfo",
			in:   "database unreachable: postgres://sorovista:hunter2@db.example:5432/sorovista: connection refused",
			leak: []string{"hunter2", "sorovista:hunter2"},
		},
		{
			name: "dsn key-value credentials",
			in:   "failed to connect: host=db user=sorovista password=hunter2 dbname=sorovista",
			leak: []string{"hunter2"},
		},
		{
			name: "quoted password and token",
			in:   `connection error: password="s3cret" token=abc123`,
			leak: []string{"s3cret", "abc123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDetail(tt.in)
			if !strings.Contains(got, "[redacted]") {
				t.Fatalf("expected a redaction marker in %q", got)
			}
			for _, secret := range tt.leak {
				if strings.Contains(got, secret) {
					t.Fatalf("sanitized detail still contains %q: %q", secret, got)
				}
			}
		})
	}
}

package resource

import (
	"strings"
	"testing"
)

func TestSafeURLSummaryOmitsCredentialsPathAndQueryValues(t *testing.T) {
	summary := safeURLSummary("https://user:password@example.com/secret/token?api_key=sensitive")

	for _, secret := range []string{"user", "password", "secret", "token", "api_key", "sensitive"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("summary %q leaked %q", summary, secret)
		}
	}
	if summary != "https://example.com path_len=13 query=true" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestSafeURLSummaryRejectsInvalidURL(t *testing.T) {
	if summary := safeURLSummary("://invalid"); summary != "invalid-url" {
		t.Fatalf("unexpected invalid URL summary: %q", summary)
	}
}

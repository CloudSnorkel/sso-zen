package aws

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

// Reference vectors. The hashes are recomputed from the reference
// implementation (Python: hashlib.sha1(...).hexdigest()) and pinned so a
// future "just use json.Marshal directly" refactor that drops the
// space-stripping or sorting silently can't pass.

func TestCalculateSsoTokenCacheHash(t *testing.T) {
	cases := []struct {
		name        string
		sessionName string
		startUrl    string
	}{
		{"sso-session takes precedence", "my-session", "https://example.awsapps.com/start"},
		{"falls back to startUrl when sessionName empty", "", "https://example.awsapps.com/start"},
		{"legacy startUrl only", "", "https://other.awsapps.com/start"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CalculateSsoTokenCacheHash(c.sessionName, c.startUrl)
			// Recompute the expected value from the documented rule.
			key := c.sessionName
			if key == "" {
				key = c.startUrl
			}
			sum := sha1.Sum([]byte(key))
			want := hex.EncodeToString(sum[:])
			if got != want {
				t.Errorf("hash mismatch:\n  got  %q\n  want %q", got, want)
			}
		})
	}
}

func TestCalculateRoleCredentialsCacheHash(t *testing.T) {
	cases := []struct {
		name        string
		roleName    string
		accountID   string
		sessionName string
		startUrl    string
		// wantPayload is the canonical JSON the hash is computed over —
		// keys sorted, no spaces (Python separators=(',', ':')). Pin
		// this rather than the hex so the parity rule reads at a glance.
		wantPayload string
	}{
		{
			"sso-session shape sorts keys without spaces",
			"AdminAccess",
			"123456789012",
			"my-session",
			"https://ignored.example.com/start",
			`{"accountId":"123456789012","roleName":"AdminAccess","sessionName":"my-session"}`,
		},
		{
			"legacy shape uses startUrl when sessionName empty",
			"ReadOnly",
			"999999999999",
			"",
			"https://legacy.example.com/start",
			`{"accountId":"999999999999","roleName":"ReadOnly","startUrl":"https://legacy.example.com/start"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CalculateRoleCredentialsCacheHash(c.roleName, c.accountID, c.sessionName, c.startUrl)
			sum := sha1.Sum([]byte(c.wantPayload))
			want := hex.EncodeToString(sum[:])
			if got != want {
				t.Errorf("hash mismatch for payload %q:\n  got  %q\n  want %q", c.wantPayload, got, want)
			}
		})
	}
}

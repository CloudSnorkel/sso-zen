package aws

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestActiveSessions_ConfigParsing covers both AWS config formats
// (sso-session block with profiles, and legacy profiles with sso_start_url
// inline). The provider emits one row per (sso-session, profile) tuple
// post-D1f, so the expected count matches the profile count.
func TestActiveSessions_ConfigParsing(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".aws")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configBody := `
[sso-session my-sso]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1

[profile dev]
sso_session = my-sso
sso_account_id = 111111111111
sso_role_name = AdminAccess
region = us-east-1

[profile prod]
sso_session = my-sso
sso_account_id = 222222222222
sso_role_name = ReadOnly
region = us-east-1

[profile legacy]
sso_start_url = https://legacy.awsapps.com/start
sso_region = eu-west-1
sso_account_id = 333333333333
sso_role_name = LegacyRole
region = eu-west-1
`

	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Provider{
		providerID:   "aws-sso",
		providerName: "AWS SSO",
		configDir:    configDir,
		awsSessions:  make(map[string]*Session),
	}

	sessions, err := p.ActiveSessions(context.Background())
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}

	if got, want := len(sessions), 3; got != want {
		t.Fatalf("session count: got %d, want %d", got, want)
	}

	// Index by profile name for assertion.
	by := map[string]*Session{}
	for _, s := range sessions {
		a, ok := p.AWSSessionFor(s)
		if !ok {
			t.Fatalf("AWSSessionFor returned not-ok for %s", s.DisplayName)
		}
		by[a.Profile] = a
	}

	if dev := by["dev"]; dev == nil {
		t.Errorf("missing dev profile")
	} else {
		if dev.SessionName != "my-sso" {
			t.Errorf("dev.SessionName = %q, want %q", dev.SessionName, "my-sso")
		}
		if dev.AccountID != "111111111111" {
			t.Errorf("dev.AccountID = %q", dev.AccountID)
		}
		if dev.Role != "AdminAccess" {
			t.Errorf("dev.Role = %q", dev.Role)
		}
		if dev.StartURL != "https://example.awsapps.com/start" {
			t.Errorf("dev.StartURL = %q", dev.StartURL)
		}
	}

	if legacy := by["legacy"]; legacy == nil {
		t.Errorf("missing legacy profile")
	} else {
		if legacy.SessionName != "" {
			t.Errorf("legacy.SessionName = %q, want empty (no sso-session block)", legacy.SessionName)
		}
		if legacy.StartURL != "https://legacy.awsapps.com/start" {
			t.Errorf("legacy.StartURL = %q", legacy.StartURL)
		}
		if legacy.Role != "LegacyRole" {
			t.Errorf("legacy.Role = %q", legacy.Role)
		}
	}

	// All emitted sessions should belong to this provider.
	for _, s := range sessions {
		if s.Provider == nil || s.Provider.ID() != "aws-sso" {
			t.Errorf("session %q: wrong provider", s.DisplayName)
		}
	}

	// Sanity: profile names match what we expect.
	got := make([]string, 0, len(by))
	for k := range by {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"dev", "legacy", "prod"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("profile names: got %v, want %v", got, want)
			break
		}
	}
}

// TestActiveSessions_EmptyConfig verifies a config with no SSO sections
// returns an empty list without erroring.
func TestActiveSessions_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".aws")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Provider{configDir: configDir, awsSessions: make(map[string]*Session)}
	sessions, err := p.ActiveSessions(context.Background())
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions from empty config, want 0", len(sessions))
	}
}

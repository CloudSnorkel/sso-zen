package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sso-zen/settings"
	"sso-zen/sso"

	"gopkg.in/ini.v1"
)

// Provider implements sso.Provider for AWS SSO. runner is injected at
// construction so the provider doesn't depend on the UI package
// (providers should be UI-agnostic).
type Provider struct {
	providerID    string
	providerName  string
	configDir     string
	runner        CommandRunner
	settingsStore *settings.Store
	// Map to store AWS-specific session data
	awsSessions   map[string]*Session // sessionID -> Session
	sessionsMutex sync.RWMutex
}

// Session extends sso.Session with AWS-specific fields. After D1f each
// Session represents one (sso-session, profile) tuple, so Profile /
// Role / AccountID are scalar (not slices). Use Provider.AWSSessionFor
// to recover this typed view from an sso.Session.
type Session struct {
	*sso.Session
	// StartURL is the SSO start URL (required)
	StartURL string
	// SessionName is the SSO session name (empty for legacy profiles without sso-session)
	SessionName string
	// Profile is the AWS CLI profile this row represents.
	Profile string
	// Role is the IAM Identity Center role name for the profile.
	Role string
	// AccountID is the AWS account ID for the profile.
	AccountID string
	// Region is the SSO region for the profile.
	Region string
}

// AWSSessionFor returns the AWS-specific session view for the given
// sso.Session, or (nil, false) if the session isn't ours.
func (p *Provider) AWSSessionFor(s *sso.Session) (*Session, bool) {
	if s == nil {
		return nil, false
	}
	p.sessionsMutex.RLock()
	defer p.sessionsMutex.RUnlock()
	a, ok := p.awsSessions[s.SessionID]
	return a, ok
}

// NewProvider creates a new AWS SSO provider. The home directory is
// resolved once here and stashed as configDir; cache lookups read from
// it instead of re-resolving (and ignoring) the error each time. The
// CommandRunner runs the interactive `aws sso login` flow; the provider
// itself stays UI-agnostic.
func NewProvider(runner CommandRunner) (*Provider, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	return &Provider{
		providerID:   "aws-sso",
		providerName: "AWS SSO",
		runner:       runner,
		configDir:    filepath.Join(homeDir, ".aws"),
		awsSessions:  make(map[string]*Session),
	}, nil
}

func (p *Provider) ID() string {
	return p.providerID
}

func (p *Provider) Name() string {
	return p.providerName
}

// PathsToWatch returns the on-disk locations the AWS provider cares
// about, for the global session monitor's file watcher. Implements the
// optional monitor.Watchable interface.
func (p *Provider) PathsToWatch() []string {
	return []string{
		filepath.Join(p.configDir, "config"),
		filepath.Join(p.configDir, "sso", "cache"),
	}
}

// SsoTokenCacheExists checks if the SSO token cache exists for the given session
func (p *Provider) SsoTokenCacheExists(session *sso.Session) bool {
	// Look up the AWS-specific session data
	p.sessionsMutex.RLock()
	awsSession, exists := p.awsSessions[session.SessionID]
	p.sessionsMutex.RUnlock()

	if !exists {
		slog.Debug("AWS session data not found", "sessionID", session.SessionID)
		return false
	}

	if awsSession.StartURL == "" {
		slog.Debug("No Start URL in AWS session", "sessionID", session.SessionID)
		return false
	}

	// Check if SSO token cache exists
	return SsoTokenCacheExists(p.configDir, awsSession.SessionName, awsSession.StartURL)
}

// ActiveSessions retrieves active AWS SSO sessions
func (p *Provider) ActiveSessions(ctx context.Context) ([]*sso.Session, error) {
	slog.Debug("Getting active AWS SSO sessions")

	// Get AWS SSO sessions directly from config
	sessions, err := p.getAwsSsoSessions(ctx)
	if err != nil {
		slog.Error("Failed to get AWS SSO sessions", "error", err)
		return nil, err
	}

	// Clean up old sessions from the map and add new ones
	p.sessionsMutex.Lock()
	// Create a set of current session IDs
	currentSessionIDs := make(map[string]bool)
	for _, session := range sessions {
		currentSessionIDs[session.SessionID] = true
	}
	// Remove sessions that no longer exist
	for sessionID := range p.awsSessions {
		if !currentSessionIDs[sessionID] {
			delete(p.awsSessions, sessionID)
		}
	}
	p.sessionsMutex.Unlock()

	slog.Debug("Found active sessions", "count", len(sessions))
	return sessions, nil
}

// Login logs into an AWS SSO session. Both attempts share a single
// visible console window: sts:GetCallerIdentity first (the SDK
// silently swaps fresh creds via the refresh token if needed) and, if
// that fails, the interactive aws sso login. Both commands run with
// AWS_CSM_ENABLED=false so our own monitor doesn't observe these
// initiated-by-us API calls and fire a misleading "session expired"
// toast back at the user.
func (p *Provider) Login(ctx context.Context, session *sso.Session) error {
	slog.Debug("Logging into AWS SSO session", "session", session.DisplayName)

	awsSession, ok := p.AWSSessionFor(session)
	if !ok || awsSession.Profile == "" {
		return fmt.Errorf("no AWS profile registered for session %s", session.DisplayName)
	}
	profileName := awsSession.Profile

	env := awsEnvNoCsm()
	stsCmd := Command{
		Banner: fmt.Sprintf("$ aws sts get-caller-identity --profile %s", profileName),
		Name:   "aws",
		Args:   []string{"sts", "get-caller-identity", "--profile", profileName},
		Env:    env,
	}
	// Option A: just sts — succeeds when existing creds work or the
	// SDK can swap fresh ones in via the refresh token. This is also
	// the path that mints role creds for the row, so on success the
	// CLI cache has fresh creds for our Refresh to read.
	//
	// Option B (fallback): aws sso login, then sts again. sso login
	// alone refreshes only the SSO token — it doesn't write role
	// credentials. Without a follow-up sts, the post-Login Refresh
	// finds no role creds in ~/.aws/cli/cache and the row sits at
	// "Expires: Unknown" until the user clicks Login a second time.
	err := p.runner.RunOptions(ctx, fmt.Sprintf("AWS SSO Login - %s", profileName), []Option{
		{stsCmd},
		{
			Command{
				Banner: fmt.Sprintf("\nCredentials need re-authentication. Running aws sso login.\n$ aws sso login --profile %s", profileName),
				Name:   "aws",
				Args:   []string{"sso", "login", "--profile", profileName},
				Env:    env,
			},
			Command{
				Banner: fmt.Sprintf("\nMinting role credentials.\n$ aws sts get-caller-identity --profile %s", profileName),
				Name:   "aws",
				Args:   []string{"sts", "get-caller-identity", "--profile", profileName},
				Env:    env,
			},
		},
	})
	if err != nil {
		slog.Error("Login failed", "profile", profileName, "error", err)
		return fmt.Errorf("login failed: %w", err)
	}

	slog.Info("Successfully logged into session", "profile", profileName)
	return nil
}

// IsSsoTokenCacheValid checks if SSO token cache is valid (not expired)
func (p *Provider) IsSsoTokenCacheValid(sessionName, startUrl string) (bool, time.Time) {
	hash := CalculateSsoTokenCacheHash(sessionName, startUrl)
	cacheFile := filepath.Join(p.configDir, "sso", "cache", hash+".json")

	data, err := os.ReadFile(cacheFile)
	if err != nil {
		slog.Debug("Failed to read SSO token cache", "hash", hash, "error", err)
		return false, time.Time{}
	}

	var cacheEntry struct {
		ExpiresAt string `json:"expiresAt"`
	}

	if err := json.Unmarshal(data, &cacheEntry); err != nil {
		slog.Debug("Failed to parse SSO token cache", "hash", hash, "error", err)
		return false, time.Time{}
	}

	expiresAt, err := time.Parse(time.RFC3339, cacheEntry.ExpiresAt)
	if err != nil {
		slog.Debug("Failed to parse SSO token expiration time", "hash", hash, "expiresAt", cacheEntry.ExpiresAt, "error", err)
		return false, time.Time{}
	}

	now := time.Now()
	isValid := expiresAt.After(now)
	slog.Debug("SSO token cache validity check", "hash", hash, "expiresAt", expiresAt, "isValid", isValid)
	return isValid, expiresAt
}

// roleCredentialsCacheExists checks if role credentials cache file exists based on hash
func (p *Provider) roleCredentialsCacheExists(roleName, accountId, sessionName, startUrl string) bool {
	hash := CalculateRoleCredentialsCacheHash(roleName, accountId, sessionName, startUrl)
	cacheFile := filepath.Join(p.configDir, "cli", "cache", hash+".json")

	_, err := os.Stat(cacheFile)
	if err != nil {
		slog.Debug("Role credentials cache does not exist", "hash", hash, "file", cacheFile)
		return false
	}

	slog.Debug("Role credentials cache exists", "hash", hash, "file", cacheFile)
	return true
}

// isRoleCredentialsCacheValid checks if role credentials cache is valid (not expired)
func (p *Provider) isRoleCredentialsCacheValid(roleName, accountId, sessionName, startUrl string) (bool, time.Time) {
	hash := CalculateRoleCredentialsCacheHash(roleName, accountId, sessionName, startUrl)
	cacheFile := filepath.Join(p.configDir, "cli", "cache", hash+".json")

	data, err := os.ReadFile(cacheFile)
	if err != nil {
		slog.Debug("Failed to read role credentials cache", "hash", hash, "error", err)
		return false, time.Time{}
	}

	var cacheEntry struct {
		Credentials struct {
			Expiration string `json:"Expiration"`
		} `json:"Credentials"`
	}

	if err := json.Unmarshal(data, &cacheEntry); err != nil {
		slog.Debug("Failed to parse role credentials cache", "hash", hash, "error", err)
		return false, time.Time{}
	}

	expiresAt, err := time.Parse(time.RFC3339, cacheEntry.Credentials.Expiration)
	if err != nil {
		slog.Debug("Failed to parse role credentials expiration time", "hash", hash, "expiration", cacheEntry.Credentials.Expiration, "error", err)
		return false, time.Time{}
	}

	// Apply 15-minute expiry window (credentials are considered expired if they expire within 15 minutes)
	expiryWindow := 15 * time.Minute
	now := time.Now()
	isValid := expiresAt.After(now.Add(expiryWindow))
	slog.Debug("Role credentials cache validity check", "hash", hash, "expiresAt", expiresAt, "isValid", isValid)
	return isValid, expiresAt
}

// getAwsSsoSessions retrieves AWS SSO sessions directly from config
func (p *Provider) getAwsSsoSessions(ctx context.Context) ([]*sso.Session, error) {
	configFile := filepath.Join(p.configDir, "config")

	// Load INI file
	cfg, err := ini.Load(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config file: %w", err)
	}

	// Map to store session data and associated profiles
	sessionData := make(map[string]*sessionInfo)
	var sessions []*sso.Session

	// First pass: Look for session sections
	for _, section := range cfg.Sections() {
		// Skip the default section (empty name)
		if section.Name() == ini.DefaultSection {
			continue
		}

		// Only process session sections
		if strings.HasPrefix(section.Name(), "sso-session ") {
			sessionName := strings.TrimSpace(strings.TrimPrefix(section.Name(), "sso-session "))

			// Create session info
			info := &sessionInfo{
				sessionName: sessionName,
				ssoStartURL: section.Key("sso_start_url").String(),
				region:      section.Key("sso_region").String(),
				profiles:    make([]string, 0),
				roles:       make([]string, 0),
				profileDetails: make(map[string]struct {
					accountId string
					roleName  string
				}),
			}

			sessionData[sessionName] = info
		}
	}

	// Second pass: Look for profile sections and associate them with sessions
	for _, section := range cfg.Sections() {
		// Skip the default section (empty name)
		if section.Name() == ini.DefaultSection {
			continue
		}

		// Only process profile sections
		if strings.HasPrefix(section.Name(), "profile ") {
			profileName := strings.TrimSpace(strings.TrimPrefix(section.Name(), "profile "))

			// Check if this profile has sso_session specified
			if ssoSession := section.Key("sso_session").String(); ssoSession != "" {
				// Look for the session in our map
				if info, exists := sessionData[ssoSession]; exists {
					// Add this profile to the session
					info.profiles = append(info.profiles, profileName)
					accountID := section.Key("sso_account_id").String()
					roleName := section.Key("sso_role_name").String()
					role := fmt.Sprintf("%s/%s", accountID, roleName)
					info.roles = append(info.roles, role)
					// Store profile details for cache checking
					info.profileDetails[profileName] = struct {
						accountId string
						roleName  string
					}{accountId: accountID, roleName: roleName}
				}
			} else {
				// Profile without sso_session - check if it has SSO config directly
				if ssoStartURL := section.Key("sso_start_url").String(); ssoStartURL != "" {
					// Create a unique session name for this standalone profile
					// For legacy profiles, sessionName is empty (use startUrl for hash)
					accountID := section.Key("sso_account_id").String()
					roleName := section.Key("sso_role_name").String()
					role := fmt.Sprintf("%s/%s", accountID, roleName)
					profileDetails := make(map[string]struct {
						accountId string
						roleName  string
					})
					profileDetails[profileName] = struct {
						accountId string
						roleName  string
					}{accountId: accountID, roleName: roleName}
					info := &sessionInfo{
						sessionName:    "", // Empty for legacy profiles (no sso-session)
						ssoStartURL:    ssoStartURL,
						region:         section.Key("region").String(),
						profiles:       []string{profileName},
						roles:          []string{role},
						profileDetails: profileDetails,
					}
					sessionData[profileName] = info
				}
			}
		}
	}

	// Third pass: emit one sso.Session per (sso-session, profile)
	// tuple. Each row's expiry is the role credentials' expiry (or the
	// SSO token's, if role creds haven't been minted yet) — that's the
	// expiry the user actually has to act on. Aggregating to one row per
	// SSO session hid this and produced the "Expired with still-working
	// CLI" false positive D1d's probe couldn't fully fix on its own.
	for _, info := range sessionData {
		if info.ssoStartURL == "" {
			continue
		}
		if len(info.profiles) == 0 {
			continue
		}

		// SSO token state is shared across all rows in this session.
		ssoTokenExists := SsoTokenCacheExists(p.configDir, info.sessionName, info.ssoStartURL)
		ssoTokenValid, ssoTokenExpiresAt := p.IsSsoTokenCacheValid(info.sessionName, info.ssoStartURL)

		// Used in row IDs to keep them unique across SSO sessions even
		// when the same profile name is reused (rare, but possible).
		sessionKey := info.sessionName
		if sessionKey == "" {
			sessionKey = info.ssoStartURL
		}

		for _, profileName := range info.profiles {
			profileDetail, exists := info.profileDetails[profileName]
			if !exists {
				continue
			}

			roleCredsExists := p.roleCredentialsCacheExists(
				profileDetail.roleName,
				profileDetail.accountId,
				info.sessionName,
				info.ssoStartURL,
			)
			roleCredsValid, roleCredsExpiresAt := p.isRoleCredentialsCacheValid(
				profileDetail.roleName,
				profileDetail.accountId,
				info.sessionName,
				info.ssoStartURL,
			)

			var status sso.Status
			var expiresAt time.Time
			switch {
			case roleCredsExists && roleCredsValid:
				status = sso.StatusActive
				expiresAt = roleCredsExpiresAt
			case ssoTokenExists && ssoTokenValid:
				// No role creds yet (or expired), but the SSO token can
				// mint fresh ones on demand. Leave expiresAt zero — the
				// SSO token's expiry isn't the role's, and showing it
				// would mislead the user into thinking "this role
				// expires in 8h" when nothing about the role itself has
				// been minted. The UI formats zero as "Expires: Unknown"
				// and D1a's timer is naturally skipped (no delay > 0).
				status = sso.StatusActive
				expiresAt = time.Time{}
			default:
				status = sso.StatusExpired
				if !roleCredsExpiresAt.IsZero() {
					expiresAt = roleCredsExpiresAt
				} else {
					expiresAt = ssoTokenExpiresAt
				}
			}

			rowID := fmt.Sprintf("aws-sso:%s:%s", sessionKey, profileName)
			coreSession := sso.NewSession(rowID, profileName, p, status, expiresAt)

			awsSession := &Session{
				Session:     coreSession,
				StartURL:    info.ssoStartURL,
				SessionName: info.sessionName,
				Profile:     profileName,
				Role:        profileDetail.roleName,
				AccountID:   profileDetail.accountId,
				Region:      info.region,
			}
			p.sessionsMutex.Lock()
			p.awsSessions[rowID] = awsSession
			p.sessionsMutex.Unlock()

			// Detail view still reads the property bag; populate it
			// from the typed fields. Production code paths should use
			// Provider.AWSSessionFor (or the typed accessors on
			// Session) instead of these magic-key reads.
			coreSession.Metadata["Region"] = info.region
			coreSession.Metadata["Account ID"] = profileDetail.accountId
			coreSession.Metadata["Role"] = profileDetail.roleName
			coreSession.Metadata["Profile"] = profileName
			if info.sessionName != "" {
				coreSession.Metadata["SSO Session"] = info.sessionName
			}

			sessions = append(sessions, coreSession)
		}
	}

	return sessions, nil
}

// sessionInfo holds information about an SSO session and its associated profiles
type sessionInfo struct {
	sessionName string
	ssoStartURL string
	region      string
	profiles    []string
	roles       []string
	// profileDetails maps profile name to its accountId and roleName
	profileDetails map[string]struct {
		accountId string
		roleName  string
	}
}

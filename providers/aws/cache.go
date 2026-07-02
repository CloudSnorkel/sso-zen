package aws

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CalculateSsoTokenCacheHash calculates the SHA-1 hash for SSO token cache filename
// Hash is based on sessionName (if using sso-session) or startUrl (if using legacy format)
// This is a standalone function that can be called without an Provider instance
func CalculateSsoTokenCacheHash(sessionName, startUrl string) string {
	// If sessionName is provided (sso-session format), use it; otherwise use startUrl
	key := sessionName
	if key == "" {
		key = startUrl
	}
	hash := sha1.Sum([]byte(key))
	return fmt.Sprintf("%x", hash)
}

// SsoTokenCacheExists checks if the SSO token cache file exists for the
// given session under awsConfigDir (typically ~/.aws). Resolve the home
// directory once at the caller (e.g. Provider.configDir) and pass
// it in so this stays a pure helper.
func SsoTokenCacheExists(awsConfigDir, sessionName, startUrl string) bool {
	hash := CalculateSsoTokenCacheHash(sessionName, startUrl)
	cacheFile := filepath.Join(awsConfigDir, "sso", "cache", hash+".json")
	_, err := os.Stat(cacheFile)
	return err == nil
}

// CalculateRoleCredentialsCacheHash calculates the SHA-1 hash for role credentials cache filename
// Hash is based on JSON: {"roleName": "...", "accountId": "...", "startUrl": "..."} or {"roleName": "...", "accountId": "...", "sessionName": "..."}
// Go's json.Marshal automatically sorts map keys, but we need to remove spaces to match Python's separators=(',', ':')
// This is a standalone function that can be called without an Provider instance
func CalculateRoleCredentialsCacheHash(roleName, accountId, sessionName, startUrl string) string {
	// Build the args map - json.Marshal will automatically sort keys for maps
	args := make(map[string]string)
	args["roleName"] = roleName
	args["accountId"] = accountId
	if sessionName != "" {
		args["sessionName"] = sessionName
	} else {
		args["startUrl"] = startUrl
	}

	// Marshal to JSON (keys will be automatically sorted)
	jsonBytes, err := json.Marshal(args)
	if err != nil {
		return ""
	}

	// Remove spaces to match Python's separators=(',', ':') format
	jsonStr := strings.ReplaceAll(string(jsonBytes), " ", "")

	// Calculate SHA-1 hash
	hash := sha1.Sum([]byte(jsonStr))
	return fmt.Sprintf("%x", hash)
}

package aws

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"sso-zen/sso"
)

// awsEnvNoCsm returns os.Environ with AWS_CSM_ENABLED=false appended,
// so commands the app initiates don't trip our own CSM monitor when
// they fail mid-credential-refresh.
func awsEnvNoCsm() []string {
	return append(os.Environ(), "AWS_CSM_ENABLED=false")
}

// Probe runs `aws sts get-caller-identity` against the session's primary
// profile. Success means current credentials work — possibly after the
// CLI silently used the refresh token to renew them. Failure means the
// user needs to log in again.
func (p *Provider) Probe(ctx context.Context, session *sso.Session) error {
	awsSession, ok := p.AWSSessionFor(session)
	if !ok || awsSession.Profile == "" {
		return fmt.Errorf("no AWS profile registered for session %s", session.DisplayName)
	}
	profileName := awsSession.Profile

	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity", "--profile", profileName)
	cmd.Env = awsEnvNoCsm()
	hideConsoleWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sts get-caller-identity failed for %s: %w: %s", profileName, err, string(out))
	}
	slog.Debug("Probe succeeded", "profile", profileName)
	return nil
}

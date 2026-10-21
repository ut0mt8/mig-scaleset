// Package ghclient builds the scale set API client from the CLI auth flags.
package ghclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/actions/scaleset"
	"github.com/spf13/pflag"

	"github.com/ut0mt8/mig-scaleset/internal/settings"
)

// Auth holds either a PAT or GitHub App credentials.
type Auth struct {
	Token string
	// The App ID or its client ID, GitHub takes both.
	AppID             string
	AppInstallationID int64
	AppPrivateKey     string
	AppPrivateKeyFile string
}

// EnvAliases are the usual GitHub env vars, read when the MIG_SCALER_ ones
// are not set.
var EnvAliases = map[string]string{
	"github-token":        "GITHUB_TOKEN",
	"app-id":              "GITHUB_APP_ID",
	"app-installation-id": "GITHUB_APP_INSTALLATION_ID",
	"app-private-key":     "GITHUB_APP_PRIVATE_KEY",
}

// AddFlags registers the auth flags; read them back with AuthFrom.
func AddFlags(f *pflag.FlagSet) {
	f.String("github-token", "", "GitHub PAT")
	f.String("app-id", "", "GitHub App ID or client ID")
	f.String("app-client-id", "", "same as --app-id")
	f.Int64("app-installation-id", 0, "GitHub App installation ID")
	f.String("app-private-key", "", "GitHub App private key, PEM or base64 PEM")
	f.String("app-private-key-file", "", "GitHub App private key file")

	if err := f.MarkDeprecated("app-client-id", "use --app-id"); err != nil {
		panic(err)
	}
}

func AuthFrom(r *settings.Reader) Auth {
	appID := r.String("app-id")
	if appID == "" {
		appID = r.String("app-client-id")
	}

	return Auth{
		Token:             r.String("github-token"),
		AppID:             appID,
		AppInstallationID: r.Int64("app-installation-id"),
		AppPrivateKey:     r.String("app-private-key"),
		AppPrivateKeyFile: r.String("app-private-key-file"),
	}
}

// New returns a client for the org, repo or enterprise at url.
func (a Auth) New(url string) (*scaleset.Client, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}

	if a.AppID != "" {
		key, err := a.privateKey()
		if err != nil {
			return nil, err
		}

		return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
			GitHubConfigURL: url,
			GitHubAppAuth: scaleset.GitHubAppAuth{
				ClientID:       a.AppID,
				InstallationID: a.AppInstallationID,
				PrivateKey:     key,
			},
		})
	}

	if a.Token == "" {
		return nil, errors.New("set --github-token, $GITHUB_TOKEN or the GitHub App flags")
	}

	return scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     url,
		PersonalAccessToken: a.Token,
	})
}

func (a Auth) validate() error {
	hasApp := a.AppID != "" || a.AppInstallationID != 0
	completeApp := a.AppID != "" && a.AppInstallationID != 0
	bothKeys := a.AppPrivateKey != "" && a.AppPrivateKeyFile != ""

	switch {
	case hasApp && !completeApp:
		return errors.New("GitHub App auth needs both --app-id and --app-installation-id")
	case bothKeys:
		return errors.New("use --app-private-key or --app-private-key-file, not both")
	}
	return nil
}

// privateKey reads the key from the flag or the file. A base64 encoded
// PEM, as often stored in env vars, is decoded.
func (a Auth) privateKey() (string, error) {
	key := a.AppPrivateKey
	if a.AppPrivateKeyFile != "" {
		raw, err := os.ReadFile(a.AppPrivateKeyFile)
		if err != nil {
			return "", fmt.Errorf("reading GitHub App key: %w", err)
		}
		key = string(raw)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("GitHub App auth needs --app-private-key or --app-private-key-file")
	}
	if strings.HasPrefix(key, "-----BEGIN") {
		return key, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", errors.New("GitHub App key is neither PEM nor base64 encoded PEM")
	}
	return string(decoded), nil
}

// RunnerGroupID resolves a runner group name, the default group is 1.
func RunnerGroupID(ctx context.Context, gh *scaleset.Client, name string) (int, error) {
	if name == "" || name == scaleset.DefaultRunnerGroup {
		return 1, nil
	}

	group, err := gh.GetRunnerGroupByName(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("getting runner group %q: %w", name, err)
	}
	return group.ID, nil
}

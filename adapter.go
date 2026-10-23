// AGPL-3.0-or-later

package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	smeldr "smeldr.dev/core"
)

// Checker is the part of a smeldr.dev/core App the adapter calls: the pull
// request check (core T918). *smeldr.App implements it.
type Checker interface {
	CheckChange(ctx context.Context, in smeldr.ChangeInput) (smeldr.ChangeCheckResult, error)
	RecordMerge(ctx context.Context, in smeldr.MergeInput) (smeldr.MergeResult, error)
	ChangeCheckOf(ctx context.Context, ident string) (smeldr.ChangeCheckResult, error)
}

// Config configures an [Adapter]. Every secret comes from the operator (an
// environment variable, a secret store); the adapter never stores or logs
// one.
type Config struct {
	// Checker is the core App.
	Checker Checker
	// Actor is the identity core calls run as: a token's user that holds the
	// check_change capability (and reads every Area, so the comment is whole).
	Actor smeldr.User
	// WebhookSecret verifies GitHub's X-Hub-Signature-256.
	WebhookSecret []byte
	// CoreWebhookSecret verifies core's X-Smeldr-Signature on the core events
	// endpoint ([Adapter.CoreEvents]).
	CoreWebhookSecret []byte
	// AppID is the GitHub App's ID.
	AppID string
	// PrivateKeyPEM is the GitHub App's private key, PEM (PKCS#1 or PKCS#8).
	PrivateKeyPEM []byte
	// APIBase is GitHub's API root; default https://api.github.com.
	APIBase string
	// HTTPClient makes the calls to GitHub; default a client with a 20 s
	// timeout.
	HTTPClient *http.Client
	// StatusContext names the commit status; default "smeldr/check".
	StatusContext string
	// CheckURL, when set, builds the status's target link from a Check
	// address, for example https://example.com/checks/%s.
	CheckURL string
}

// Adapter turns GitHub webhook events into core's pull request check and
// posts the verdict back as a commit status and one comment it keeps up to
// date. Mount [Adapter.ServeHTTP] as the GitHub App's webhook endpoint and
// [Adapter.CoreEvents] as a core webhook for changecheck.transitioned, so an
// approval moves the status.
type Adapter struct {
	cfg  Config
	key  *rsa.PrivateKey
	now  func() time.Time
	mu   sync.Mutex
	toks map[int64]installationToken
	// installs remembers each repository's installation, learnt from its
	// webhooks, so a core event can be posted back.
	installs map[string]int64
}

// installationToken is a cached GitHub App installation token.
type installationToken struct {
	token   string
	expires time.Time
}

// New checks cfg and returns an Adapter.
func New(cfg Config) (*Adapter, error) {
	switch {
	case cfg.Checker == nil:
		return nil, errors.New("github: Config.Checker is required")
	case cfg.Actor.ID == "":
		return nil, errors.New("github: Config.Actor is required: a user that holds check_change")
	case len(cfg.WebhookSecret) == 0:
		return nil, errors.New("github: Config.WebhookSecret is required")
	case cfg.AppID == "":
		return nil, errors.New("github: Config.AppID is required")
	}
	key, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.StatusContext == "" {
		cfg.StatusContext = "smeldr/check"
	}
	return &Adapter{cfg: cfg, key: key, now: time.Now, toks: map[int64]installationToken{}, installs: map[string]int64{}}, nil
}

// parsePrivateKey reads an RSA key from PEM, PKCS#1 or PKCS#8.
func parsePrivateKey(p []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, errors.New("github: Config.PrivateKeyPEM holds no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: Config.PrivateKeyPEM: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: Config.PrivateKeyPEM is not an RSA key")
	}
	return rk, nil
}

// coreCtx is the context core calls run in: the adapter's actor.
func (a *Adapter) coreCtx() smeldr.Context {
	return smeldr.NewContextWithUser(a.cfg.Actor)
}

// remember records the installation of a repository.
func (a *Adapter) remember(repo string, installation int64) {
	if installation == 0 {
		return
	}
	a.mu.Lock()
	a.installs[strings.ToLower(repo)] = installation
	a.mu.Unlock()
}

// installation returns the remembered installation of repo.
func (a *Adapter) installation(repo string) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.installs[strings.ToLower(repo)]
	return id, ok
}

// logError logs a failed step without any secret.
func logError(ctx context.Context, msg string, err error, args ...any) {
	slog.ErrorContext(ctx, "smeldr.dev/github: "+msg, append(args, "error", err)...)
}

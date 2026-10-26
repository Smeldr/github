package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxFiles is the most changed files GitHub lists for a pull request.
const maxFiles = 3000

// appJWT is the GitHub App's JSON Web Token (RS256, ten minutes, issued a
// minute in the past for clock drift).
func (a *Adapter) appJWT() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.cfg.AppID})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign the App JWT: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// token returns an installation token, cached until a minute before it
// expires.
func (a *Adapter) token(ctx context.Context, installation int64) (string, error) {
	a.mu.Lock()
	t, ok := a.toks[installation]
	a.mu.Unlock()
	if ok && a.now().Before(t.expires.Add(-time.Minute)) {
		return t.token, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, "Bearer "+jwt, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installation), nil, &out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.toks[installation] = installationToken{token: out.Token, expires: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// call makes one GitHub API request and decodes a 2xx answer into out (when
// not nil). Any other status is an error that names the status, never the
// body or a token.
func (a *Adapter) call(ctx context.Context, auth, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.APIBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if msg := gitHubErrorMessage(raw); msg != "" {
			return fmt.Errorf("github: %s %s: %s: %s", method, path, resp.Status, msg)
		}
		return fmt.Errorf("github: %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// maxErrorMessage bounds the GitHub error message an error carries.
const maxErrorMessage = 300

// gitHubErrorMessage returns the message field of a GitHub error body, on one
// line and at most maxErrorMessage characters, or "" when the body is not
// JSON or has none. Only that field is taken: GitHub's error bodies describe
// the failure (a permission, a validation, not found) and never echo the
// request's headers or token (core T950).
func gitHubErrorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	msg := strings.Join(strings.Fields(e.Message), " ")
	if r := []rune(msg); len(r) > maxErrorMessage {
		msg = string(r[:maxErrorMessage]) + "..."
	}
	return msg
}

// api calls GitHub as the installation.
func (a *Adapter) api(ctx context.Context, installation int64, method, path string, in, out any) error {
	tok, err := a.token(ctx, installation)
	if err != nil {
		return err
	}
	return a.call(ctx, "token "+tok, method, path, in, out)
}

// installationOf returns the App's installation on repo: the one learnt from
// its webhooks, or else looked up with the App JWT (after a restart) and
// remembered.
func (a *Adapter) installationOf(ctx context.Context, repo string) (int64, error) {
	if id, ok := a.installation(repo); ok {
		return id, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, "Bearer "+jwt, http.MethodGet, fmt.Sprintf("/repos/%s/installation", repo), nil, &out); err != nil {
		return 0, fmt.Errorf("the App's installation on %s: %w", repo, err)
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("the App has no installation on %s", repo)
	}
	a.remember(repo, out.ID)
	return out.ID, nil
}

// changedFiles lists a pull request's files, a page of 100 at a time, up to
// the 3000 GitHub lists.
func (a *Adapter) changedFiles(ctx context.Context, installation int64, repo string, number int) ([]string, error) {
	var paths []string
	for page := 1; len(paths) < maxFiles; page++ {
		var files []struct {
			Filename string `json:"filename"`
		}
		if err := a.api(ctx, installation, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, number, page), nil, &files); err != nil {
			return nil, err
		}
		for _, f := range files {
			paths = append(paths, f.Filename)
		}
		if len(files) < 100 {
			break
		}
	}
	// Pages of 100 stop at exactly maxFiles.
	return paths, nil
}

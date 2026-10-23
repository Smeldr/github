package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	smeldr "smeldr.dev/core"
)

const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeChecker records the core calls and answers with what it is given.
type fakeChecker struct {
	mu      sync.Mutex
	checked []smeldr.ChangeInput
	merged  []smeldr.MergeInput
	result  smeldr.ChangeCheckResult
	err     error
}

func (f *fakeChecker) CheckChange(_ context.Context, in smeldr.ChangeInput) (smeldr.ChangeCheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, in)
	return f.result, f.err
}

func (f *fakeChecker) RecordMerge(_ context.Context, in smeldr.MergeInput) (smeldr.MergeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merged = append(f.merged, in)
	return smeldr.MergeResult{Covered: true}, f.err
}

func (f *fakeChecker) ChangeCheckOf(_ context.Context, _ string) (smeldr.ChangeCheckResult, error) {
	return f.result, f.err
}

// fakeGitHub is GitHub's API as the adapter uses it, recording each call.
type fakeGitHub struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string]string
	tokens   int
	files    int
	comments []map[string]any
	fail     string // a path prefix that answers 500
	// installation is what GET /repos/{repo}/installation answers.
	installation int64
}

func (g *fakeGitHub) handler(t *testing.T, pub *rsa.PublicKey) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		call := r.Method + " " + r.URL.Path
		g.calls = append(g.calls, call)
		if g.bodies == nil {
			g.bodies = map[string]string{}
		}
		g.bodies[call] = string(body)
		fail := g.fail
		g.mu.Unlock()
		if fail != "" && strings.HasPrefix(r.URL.Path, fail) {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/"):
			verifyJWT(t, pub, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			g.mu.Lock()
			g.tokens++
			g.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "inst-token", "expires_at": time.Now().Add(time.Hour)})
		case strings.HasSuffix(r.URL.Path, "/files"):
			if r.Header.Get("Authorization") != "token inst-token" {
				t.Errorf("files without the installation token: %q", r.Header.Get("Authorization"))
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			var files []map[string]string
			for i := (page - 1) * 100; i < g.files && i < page*100; i++ {
				files = append(files, map[string]string{"filename": fmt.Sprintf("f%d.go", i)})
			}
			_ = json.NewEncoder(w).Encode(files)
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(g.comments)
		case strings.HasSuffix(r.URL.Path, "/installation"):
			verifyJWT(t, pub, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			_ = json.NewEncoder(w).Encode(map[string]any{"id": g.installation})
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("{}"))
		}
	})
}

func (g *fakeGitHub) called(call string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if c == call {
			return true
		}
	}
	return false
}

// verifyJWT checks the App JWT's RS256 signature and claims.
func verifyJWT(t *testing.T, pub *rsa.PublicKey, jwt string) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("JWT signature: %v", err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	_ = json.Unmarshal(claims, &c)
	if c["iss"] != "123" {
		t.Errorf("JWT iss = %v", c["iss"])
	}
}

type fixture struct {
	a       *Adapter
	core    *fakeChecker
	gh      *fakeGitHub
	secret  []byte
	coreSec []byte
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	f := fixture{core: &fakeChecker{}, gh: &fakeGitHub{files: 150}, secret: []byte("hook-secret"), coreSec: []byte("core-secret")}
	srv := httptest.NewServer(f.gh.handler(t, &key.PublicKey))
	t.Cleanup(srv.Close)
	f.a, err = New(Config{Checker: f.core, Actor: smeldr.User{ID: "adapter"}, WebhookSecret: f.secret, CoreWebhookSecret: f.coreSec,
		AppID: "123", PrivateKeyPEM: pemKey, APIBase: srv.URL + "/", CheckURL: "https://example.com/checks/%s"})
	if err != nil {
		t.Fatal(err)
	}
	f.core.result = awaiting()
	return f
}

func awaiting() smeldr.ChangeCheckResult {
	return smeldr.ChangeCheckResult{Covered: true,
		Check: &smeldr.ChangeCheck{Node: smeldr.Node{ID: "c1", Status: smeldr.ChangeCheckAwaitingApproval}, RecordAddress: smeldr.RecordAddress{Address: "CHK3"},
			Repository: "acme/app", Number: 7, HeadCommit: head, FilesTotal: 150, FilesChecked: 150},
		Areas:     []smeldr.ChangeCheckArea{{Address: "ARE1", Name: "Billing", ApprovalMode: "requires-approval", Verdict: "awaiting-approval"}},
		Decisions: []smeldr.AppliedDecision{{Address: "D7", Title: "Money in cents"}},
	}
}

func signed(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (f fixture) deliver(t *testing.T, event, file string, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	r.Header.Set("X-GitHub-Event", event)
	if sign {
		r.Header.Set("X-Hub-Signature-256", signed(f.secret, body))
	} else {
		r.Header.Set("X-Hub-Signature-256", "sha256=00")
	}
	w := httptest.NewRecorder()
	f.a.ServeHTTP(w, r)
	return w
}

func TestNew(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	ok := Config{Checker: &fakeChecker{}, Actor: smeldr.User{ID: "a"}, WebhookSecret: []byte("s"), AppID: "1", PrivateKeyPEM: pemKey}
	if _, err := New(ok); err != nil {
		t.Fatalf("PKCS#8 key: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no checker": func(c *Config) { c.Checker = nil },
		"no actor":   func(c *Config) { c.Actor = smeldr.User{} },
		"no secret":  func(c *Config) { c.WebhookSecret = nil },
		"no app":     func(c *Config) { c.AppID = "" },
		"no pem":     func(c *Config) { c.PrivateKeyPEM = []byte("nope") },
		"bad key": func(c *Config) {
			c.PrivateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
		},
		"not an RSA key": func(c *Config) {
			c.PrivateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})
		},
	} {
		c := ok
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestPullRequestOpened(t *testing.T) {
	f := newFixture(t)
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(f.core.checked) != 1 {
		t.Fatalf("CheckChange calls = %d", len(f.core.checked))
	}
	in := f.core.checked[0]
	if in.Repository != "acme/app" || in.Number != 7 || in.HeadCommit != head || len(in.Paths) != 150 || in.FilesTotal != 150 {
		t.Errorf("CheckChange input = %+v (paths %d)", in, len(in.Paths))
	}
	status := f.gh.bodies["POST /repos/acme/app/statuses/"+head]
	if !strings.Contains(status, `"state":"pending"`) || !strings.Contains(status, "https://example.com/checks/CHK3") || !strings.Contains(status, `"context":"smeldr/check"`) {
		t.Errorf("status = %s", status)
	}
	comment := f.gh.bodies["POST /repos/acme/app/issues/7/comments"]
	for _, want := range []string{"CHK3", "ARE1 Billing", "D7 Money in cents", "awaiting-approval"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment is missing %q: %s", want, comment)
		}
	}
	// A second event reuses the cached token and updates the comment in place.
	f.gh.comments = []map[string]any{
		// A comment by someone else that plants the marker is left alone.
		{"id": 54, "body": commentMarker + "\nplanted", "user": map[string]any{"type": "User"}},
		{"id": 55, "body": commentMarker + "\nold", "user": map[string]any{"type": "Bot"}, "performed_via_github_app": map[string]any{"id": 123}},
	}
	f.core.result.Check.Status = smeldr.ChangeCheckApproved
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusNoContent {
		t.Fatalf("second: %d", w.Code)
	}
	if f.gh.tokens != 1 {
		t.Errorf("token fetched %d times, want 1 (cached)", f.gh.tokens)
	}
	if !f.gh.called("PATCH /repos/acme/app/issues/comments/55") {
		t.Errorf("comment not updated in place: %v", f.gh.calls)
	}
	if f.gh.called("PATCH /repos/acme/app/issues/comments/54") {
		t.Error("a planted marker comment was edited")
	}
	if !strings.Contains(f.gh.bodies["POST /repos/acme/app/statuses/"+head], `"state":"success"`) {
		t.Error("approved is not success")
	}
}

func TestPullRequestMergedAndOthers(t *testing.T) {
	f := newFixture(t)
	if w := f.deliver(t, "pull_request", "pull_request_merged.json", true); w.Code != http.StatusNoContent || len(f.core.merged) != 1 {
		t.Fatalf("merged: %d, %v", w.Code, f.core.merged)
	}
	if m := f.core.merged[0]; m.MergeCommit != strings.Repeat("c", 40) || m.HeadCommit != head || m.Number != 7 {
		t.Errorf("RecordMerge input = %+v", m)
	}
	// Bad signature: 401, nothing called.
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", false); w.Code != http.StatusUnauthorized || len(f.core.checked) != 0 {
		t.Errorf("bad signature: %d, %d calls", w.Code, len(f.core.checked))
	}
	// Another event: 204.
	if w := f.deliver(t, "push", "pull_request_opened.json", true); w.Code != http.StatusNoContent {
		t.Errorf("push: %d", w.Code)
	}
	// A pull_request action the adapter ignores, and a body that is no event.
	for _, body := range []string{`{"action":"labeled","number":7,"repository":{"full_name":"acme/app"}}`, `{"action":"opened"}`, `not json`} {
		r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
		r.Header.Set("X-GitHub-Event", "pull_request")
		r.Header.Set("X-Hub-Signature-256", signed(f.secret, []byte(body)))
		w := httptest.NewRecorder()
		f.a.ServeHTTP(w, r)
		want := http.StatusBadRequest
		if strings.Contains(body, "labeled") {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Errorf("%s: %d, want %d", body, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	f.a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hook", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestPullRequestFailures(t *testing.T) {
	// Core refuses: 502, no status posted.
	f := newFixture(t)
	f.core.err = errors.New("core down")
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway {
		t.Errorf("core error: %d", w.Code)
	}
	if w := f.deliver(t, "pull_request", "pull_request_merged.json", true); w.Code != http.StatusBadGateway {
		t.Errorf("merge error: %d", w.Code)
	}
	// GitHub's file list fails: 502, core not called.
	g := newFixture(t)
	g.gh.fail = "/repos/acme/app/pulls/"
	if w := g.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway || len(g.core.checked) != 0 {
		t.Errorf("files error: %d, %d calls", w.Code, len(g.core.checked))
	}
	// The token cannot be had.
	h := newFixture(t)
	h.gh.fail = "/app/"
	if w := h.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway {
		t.Errorf("token error: %d", w.Code)
	}
	// The status cannot be posted.
	k := newFixture(t)
	k.gh.fail = "/repos/acme/app/statuses/"
	if w := k.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway {
		t.Errorf("status error: %d", w.Code)
	}
	// Many files: paged and capped at 3000.
	m := newFixture(t)
	m.gh.files = 3200
	if w := m.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusNoContent || len(m.core.checked[0].Paths) != 3000 || m.core.checked[0].FilesTotal != 3000 {
		t.Errorf("many files: %d, %d paths", w.Code, len(m.core.checked[0].Paths))
	}
}

func (f fixture) coreEvent(t *testing.T, body string, ts time.Time, secret []byte) int {
	t.Helper()
	stamp := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(stamp + "."))
	mac.Write([]byte(body))
	r := httptest.NewRequest(http.MethodPost, "/core", strings.NewReader(body))
	r.Header.Set("X-Smeldr-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-Smeldr-Timestamp", stamp)
	w := httptest.NewRecorder()
	f.a.CoreEvents().ServeHTTP(w, r)
	return w.Code
}

func TestCoreEvents(t *testing.T) {
	f := newFixture(t)
	event := `{"id":"e1","event":"changecheck.transitioned","data":{"type":"changecheck","id":"c1","to_state":"approved"}}`
	// Before any webhook (after a restart) the installation is looked up.
	f.gh.installation = 9001
	if code := f.coreEvent(t, event, time.Now(), f.coreSec); code != http.StatusNoContent || !f.gh.called("GET /repos/acme/app/installation") {
		t.Errorf("installation looked up: %d, %v", code, f.gh.calls)
	}
	if id, ok := f.a.installation("acme/app"); !ok || id != 9001 {
		t.Errorf("installation remembered: %d %v", id, ok)
	}
	f.core.result.Check.Status = smeldr.ChangeCheckNotApproved
	if code := f.coreEvent(t, event, time.Now(), f.coreSec); code != http.StatusNoContent {
		t.Fatalf("event: %d", code)
	}
	if !strings.Contains(f.gh.bodies["POST /repos/acme/app/statuses/"+head], `"state":"failure"`) {
		t.Error("not approved is not failure")
	}
	for name, tc := range map[string]struct {
		body   string
		ts     time.Time
		secret []byte
		want   int
	}{
		"bad signature":  {event, time.Now(), []byte("other"), http.StatusUnauthorized},
		"stale":          {event, time.Now().Add(-time.Hour), f.coreSec, http.StatusUnauthorized},
		"another event":  {`{"event":"task.created","data":{}}`, time.Now(), f.coreSec, http.StatusNoContent},
		"not json":       {`nope`, time.Now(), f.coreSec, http.StatusBadRequest},
		"no Check in it": {`{"event":"changecheck.transitioned","data":{}}`, time.Now(), f.coreSec, http.StatusBadRequest},
	} {
		if code := f.coreEvent(t, tc.body, tc.ts, tc.secret); code != tc.want {
			t.Errorf("%s: %d, want %d", name, code, tc.want)
		}
	}
	// A superseded Check posts nothing; a core error is 502.
	f.core.result.Check.Status = smeldr.ChangeCheckSuperseded
	if err := f.a.Refresh(context.Background(), "c1"); err != nil {
		t.Errorf("superseded: %v", err)
	}
	f.core.err = errors.New("down")
	if code := f.coreEvent(t, event, time.Now(), f.coreSec); code != http.StatusBadGateway {
		t.Errorf("core error: %d", code)
	}
	// Without a core secret every request is refused; GET is not allowed.
	f.a.cfg.CoreWebhookSecret = nil
	if code := f.coreEvent(t, event, time.Now(), f.coreSec); code != http.StatusUnauthorized {
		t.Errorf("no secret: %d", code)
	}
	w := httptest.NewRecorder()
	f.a.CoreEvents().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/core", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestStatusAndComment(t *testing.T) {
	for status, want := range map[string]string{
		smeldr.ChangeCheckApproved: "success", smeldr.ChangeCheckNotApproved: "failure",
		smeldr.ChangeCheckAwaitingApproval: "pending", smeldr.ChangeCheckChecking: "pending", smeldr.ChangeCheckCommented: "success",
	} {
		res := awaiting()
		res.Check.Status = smeldr.Status(status)
		if got, _ := statusOf(res); got != want {
			t.Errorf("%s: %s, want %s", status, got, want)
		}
	}
	none := smeldr.ChangeCheckResult{Covered: false}
	if state, desc := statusOf(none); state != "success" || !strings.Contains(desc, "No Area") {
		t.Errorf("uncovered: %s %s", state, desc)
	}
	if body := commentBody(none); !strings.Contains(body, "touches nothing") {
		t.Errorf("uncovered comment: %s", body)
	}
	partly := awaiting()
	partly.Check.CheckedPartly = true
	if body := commentBody(partly); !strings.Contains(body, "checked partly") {
		t.Errorf("partly: %s", body)
	}
}

func TestSignatures(t *testing.T) {
	if validGitHubSignature([]byte("s"), []byte("b"), "md5=00") || validGitHubSignature([]byte("s"), []byte("b"), "sha256=zz") {
		t.Error("a malformed GitHub signature verified")
	}
	f := newFixture(t)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if f.a.validCoreSignature([]byte("b"), "md5=00", now) || f.a.validCoreSignature([]byte("b"), "sha256=zz", now) || f.a.validCoreSignature([]byte("b"), "sha256=00", "x") {
		t.Error("a malformed core signature verified")
	}
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestTransportFailures(t *testing.T) {
	// Reading the comments fails after the status was posted.
	f := newFixture(t)
	f.gh.fail = "/repos/acme/app/issues/"
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway {
		t.Errorf("comments error: %d", w.Code)
	}
	// GitHub does not answer at all, and an API root that is no URL.
	for _, base := range []string{"http://127.0.0.1:1", "http://[::1"} {
		g := newFixture(t)
		g.a.cfg.APIBase = base
		if w := g.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusBadGateway {
			t.Errorf("%s: %d", base, w.Code)
		}
	}
	// A body that cannot be read.
	for _, h := range []http.Handler{f.a, f.a.CoreEvents()} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", errReader{}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("unreadable body: %d", w.Code)
		}
	}
}

// S3158 fix 1: a pull request that touches nothing covered passes silently.
func TestUncoveredPullRequest(t *testing.T) {
	f := newFixture(t)
	f.core.result = smeldr.ChangeCheckResult{Covered: false}
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(f.gh.bodies["POST /repos/acme/app/statuses/"+head], `"state":"success"`) {
		t.Error("no success status")
	}
	if f.gh.called("POST /repos/acme/app/issues/7/comments") {
		t.Error("an uncovered pull request got a comment")
	}
	// An earlier covered commit left a comment: it is updated, not left stale.
	f.gh.comments = []map[string]any{{"id": 56, "body": commentMarker + "\nCHK3 awaiting", "user": map[string]any{"type": "Bot"}, "performed_via_github_app": map[string]any{"id": 123}}}
	if w := f.deliver(t, "pull_request", "pull_request_opened.json", true); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if body := f.gh.bodies["PATCH /repos/acme/app/issues/comments/56"]; !strings.Contains(body, "touches nothing") {
		t.Errorf("stale comment not updated: %q", body)
	}
}

// S3158 fix 3: the installation lookup fails or finds none.
func TestInstallationLookup(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.installationOf(context.Background(), "acme/app"); err == nil {
		t.Error("no installation: no error")
	}
	f.gh.fail = "/repos/acme/app/installation"
	if _, err := f.a.installationOf(context.Background(), "acme/app"); err == nil {
		t.Error("lookup error: no error")
	}
}

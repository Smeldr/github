package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCall_ErrorMessage (core T950): a non-2xx answer carries GitHub's
// message, on one line and bounded, and nothing from the request.
func TestCall_ErrorMessage(t *testing.T) {
	long := strings.Repeat("x", 1000)
	for _, tc := range []struct {
		name, ctype, body string
		status           int
		want, notWant    string
	}{
		{"permission", "application/json", `{"message":"Resource not accessible by integration","documentation_url":"https://docs.github.com"}`, 403,
			"403 Forbidden: Resource not accessible by integration", "docs.github.com"},
		{"not JSON", "text/html", "<html>bad gateway</html>", 502, "502 Bad Gateway", "html"},
		{"no message", "application/json", `{"errors":[]}`, 422, "422 Unprocessable Entity", ": :"},
		{"long, multi-line", "application/json", `{"message":"line one\nline\ttwo ` + long + `"}`, 400, "400 Bad Request: line one line two xxx", long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			a := &Adapter{cfg: Config{APIBase: srv.URL, HTTPClient: srv.Client()}}
			err := a.call(context.Background(), "token ghs_secret-token-value", http.MethodPost, "/repos/o/r/issues/2/comments", map[string]string{"body": "hi"}, nil)
			if err == nil {
				t.Fatal("want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "github: POST /repos/o/r/issues/2/comments: "+tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
			if strings.Contains(msg, tc.notWant) || strings.Contains(msg, "ghs_secret") || strings.Contains(msg, "\n") {
				t.Errorf("error = %q holds what it must not", msg)
			}
			if len(msg) > 400 {
				t.Errorf("error is %d characters long", len(msg))
			}
		})
	}
}

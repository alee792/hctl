package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestProductionClientDisablesConnectionReuse(t *testing.T) {
	client := NewClient(nil)
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || !transport.DisableKeepAlives {
		t.Fatalf("production transport may retry an idempotent request: %#v", client.http.Transport)
	}
}

func TestGitHubOperationsUseFixedAnonymousRequestsAndBoundedOutputs(t *testing.T) {
	responses := []string{
		`{"full_name":"acme/widgets","description":"public widgets","html_url":"https://github.com/acme/widgets","default_branch":"main","archived":false,"fork":false,"open_issues_count":3,"updated_at":"2026-08-06T00:00:00Z"}`,
		`[` + issueJSON(1, "first", strings.Repeat("é", 5000)) + `,` + issueJSON(2, "second", "body") + `,` + issueJSON(3, "third", "body") + `]`,
		issueJSON(7, "one issue", "details"),
	}
	wantURLs := []string{
		"https://api.github.com/repos/acme/widgets",
		"https://api.github.com/repos/acme/widgets/issues?per_page=2&state=closed",
		"https://api.github.com/repos/acme/widgets/issues/7",
	}
	call := 0
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != wantURLs[call] {
			t.Fatalf("request %d = %s %s", call, request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("anonymous request included authorization")
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || request.Header.Get("User-Agent") != "hctl" {
			t.Fatalf("request headers = %#v", request.Header)
		}
		body := responses[call]
		call++
		return response(http.StatusOK, body), nil
	})})

	repository, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
	if err != nil || !strings.Contains(string(repository), `"full_name":"acme/widgets"`) {
		t.Fatalf("repository = %s, %v", repository, err)
	}
	issues, err := client.Call(context.Background(), ListIssues, json.RawMessage(`{"owner":"acme","repo":"widgets","state":"closed","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Issues []issueOutput `json:"issues"`
	}
	if err := json.Unmarshal(issues, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Issues) != 2 || len([]byte(listed.Issues[0].Body)) > 8192 || !listed.Issues[0].Truncated || !json.Valid(issues) {
		t.Fatalf("bounded issue list = %#v", listed)
	}
	issue, err := client.Call(context.Background(), GetIssue, json.RawMessage(`{"owner":"acme","repo":"widgets","number":7}`))
	if err != nil || !strings.Contains(string(issue), `"number":7`) {
		t.Fatalf("issue = %s, %v", issue, err)
	}
	if call != 3 {
		t.Fatalf("requests = %d, want 3", call)
	}
}

func TestGitHubInputFailsClosedWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, `{}`), nil
	})})
	tests := []struct {
		name      string
		operation string
		input     string
	}{
		{"unknown field", GetRepository, `{"owner":"acme","repo":"widgets","token":"secret"}`},
		{"owner slash", GetRepository, `{"owner":"acme/bad","repo":"widgets"}`},
		{"bad state", ListIssues, `{"owner":"acme","repo":"widgets","state":"merged"}`},
		{"empty state", ListIssues, `{"owner":"acme","repo":"widgets","state":""}`},
		{"zero limit", ListIssues, `{"owner":"acme","repo":"widgets","limit":0}`},
		{"large limit", ListIssues, `{"owner":"acme","repo":"widgets","limit":21}`},
		{"bad number", GetIssue, `{"owner":"acme","repo":"widgets","number":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Call(context.Background(), test.operation, json.RawMessage(test.input)); err == nil || !strings.HasPrefix(err.Error(), "invalid GitHub") {
				t.Fatalf("invalid input error = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input made %d requests", calls.Load())
	}
}

func TestGitHubClassifiesFailuresWithoutBodyOrRetry(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "GitHub resource not found"},
		{http.StatusForbidden, "GitHub request rate limited"},
		{http.StatusTooManyRequests, "GitHub request rate limited"},
		{http.StatusUnauthorized, "GitHub request unauthorized"},
		{http.StatusBadRequest, "GitHub request failed"},
		{http.StatusInternalServerError, "GitHub service unavailable"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			calls := 0
			client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(test.status, `private upstream diagnostics and secret`), nil
			})})
			_, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
			if err == nil || err.Error() != test.want || strings.Contains(err.Error(), "secret") {
				t.Fatalf("status error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("status %d was attempted %d times", test.status, calls)
			}
		})
	}
}

func TestGitHubRejectsRedirectTimeoutAndOversizedResponse(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		calls := 0
		client := NewClient(&http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				result := response(http.StatusFound, "redirect body")
				result.Header.Set("Location", "https://example.com/stolen")
				return result, nil
			}),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		})
		_, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
		if err == nil || err.Error() != "GitHub request failed" || calls != 1 {
			t.Fatalf("redirect = %v, calls %d", err, calls)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		client := NewClient(&http.Client{
			Timeout: 10 * time.Millisecond,
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}),
		})
		_, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
		if err == nil || err.Error() != "GitHub request timed out" {
			t.Fatalf("timeout = %v", err)
		}
	})

	t.Run("response bound", func(t *testing.T) {
		client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, strings.Repeat("x", maxResponseBytes+1)), nil
		})})
		_, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
		if err == nil || err.Error() != "GitHub response exceeded byte limit" {
			t.Fatalf("oversized response = %v", err)
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"full_name":7}`), nil
		})})
		_, err := client.Call(context.Background(), GetRepository, json.RawMessage(`{"owner":"acme","repo":"widgets"}`))
		if err == nil || err.Error() != "GitHub returned an invalid response" {
			t.Fatalf("invalid response = %v", err)
		}
	})
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func issueJSON(number int, title, body string) string {
	value := map[string]any{
		"number": number, "title": title, "state": "open", "body": body,
		"html_url": "https://github.com/acme/widgets/issues/1", "user": map[string]any{"login": "octocat"},
		"labels": []any{map[string]any{"name": "bug"}}, "created_at": "2026-08-05T00:00:00Z", "updated_at": "2026-08-06T00:00:00Z",
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v83/github"
)

func TestEtagTransport(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri: "/repos/test/blah",
			ExpectedHeaders: map[string]string{
				"If-None-Match": "something",
			},

			ResponseBody: `{"id": 1234}`,
			StatusCode:   200,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewEtagTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxEtag, "something")
	r, _, err := client.Repositories.Get(ctx, "test", "blah")
	if err != nil {
		t.Fatal(err)
	}

	if r.GetID() != 1234 {
		t.Fatalf("Expected ID to be 1234, got: %d", r.GetID())
	}
}

func githubApiMock(responseSequence []*mockResponse) *httptest.Server {
	position := github.Ptr(0)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Server", "GitHub.com")

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[DEBUG] Error: %s", err)
		}
		log.Printf("[DEBUG] Mock server received %s request to %q; headers:\n%s\nrequest body: %q\n",
			r.Method, r.RequestURI, r.Header, string(bodyBytes))

		i := *position
		if len(responseSequence) < i+1 {
			w.WriteHeader(400)
			return
		}

		tc := responseSequence[i]

		headersMatch := func(h http.Header, expectedHeaders map[string]string) bool {
			for key, value := range expectedHeaders {
				if h.Get(key) != value {
					return false
				}
			}
			return true
		}

		if r.RequestURI != tc.ExpectedUri {
			log.Printf("[DEBUG] Error: expected URI: %q, given: %q", tc.ExpectedUri, r.RequestURI)
			w.WriteHeader(400)
			return
		}
		if !headersMatch(r.Header, tc.ExpectedHeaders) {
			log.Printf("[DEBUG] Error: expected headers: %q, given: %q", tc.ExpectedHeaders, r.Header)
			w.WriteHeader(400)
			return
		}
		if tc.ExpectedMethod != "" && r.Method != tc.ExpectedMethod {
			log.Printf("[DEBUG] Error: expected method: %q, given: %q", tc.ExpectedMethod, r.Method)
			w.WriteHeader(400)
			return
		}
		if len(tc.ExpectedBody) > 0 && string(bodyBytes) != string(tc.ExpectedBody) {
			log.Printf("[DEBUG] Error: expected body: %q, given: %q",
				string(tc.ExpectedBody), string(bodyBytes))
			w.WriteHeader(400)
			return
		}

		for key, value := range tc.ResponseHeaders {
			w.Header().Set(key, value)
		}
		w.WriteHeader(tc.StatusCode)
		fmt.Fprintln(w, tc.ResponseBody)

		// Treat response as disposable
		position = github.Ptr(i + 1)
	}))
}

func TestRateLimitTransport_abuseLimit_get(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri: "/repos/test/blah",
			ResponseBody: `{
  "message": "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
  "documentation_url": "https://developer.github.com/v3/#abuse-rate-limits"
}`,
			StatusCode: 403,
			ResponseHeaders: map[string]string{
				"Retry-After": "0.1",
			},
		},
		{
			ExpectedUri: "/repos/test/blah",
			ResponseBody: `{
  "message": "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
  "documentation_url": "https://developer.github.com/v3/#abuse-rate-limits"
}`,
			StatusCode: 403,
			ResponseHeaders: map[string]string{
				"Retry-After": "0.1",
			},
		},
		{
			ExpectedUri:  "/repos/test/blah",
			ResponseBody: `{"id": 1234}`,
			StatusCode:   200,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRateLimitTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxId, t.Name())
	r, _, err := client.Repositories.Get(ctx, "test", "blah")
	if err != nil {
		t.Fatal(err)
	}

	if r.GetID() != 1234 {
		t.Fatalf("Expected ID to be 1234, got: %d", r.GetID())
	}
}

func TestRateLimitTransport_abuseLimit_get_cancelled(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri: "/repos/test/blah",
			ResponseBody: `{
  "message": "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
  "documentation_url": "https://developer.github.com/v3/#abuse-rate-limits"
}`,
			StatusCode: 403,
			ResponseHeaders: map[string]string{
				"Retry-After": "10",
			},
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRateLimitTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := client.Repositories.Get(ctx, "test", "blah")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Expected context deadline exceeded, got: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Waited for longer than expected: %s", time.Since(start))
	}
}

func TestRateLimitTransport_abuseLimit_post(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
  "documentation_url": "https://developer.github.com/v3/#abuse-rate-limits"
}`,
			StatusCode: 403,
			ResponseHeaders: map[string]string{
				"Retry-After": "0.1",
			},
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{"id": 1234}`,
			StatusCode:   200,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRateLimitTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxId, t.Name())
	r, _, err := client.Repositories.Create(ctx, "tada", &github.Repository{
		Name:        github.Ptr("radek-example-48"),
		Description: github.Ptr(""),
	})
	if err != nil {
		t.Fatal(err)
	}

	if r.GetID() != 1234 {
		t.Fatalf("Expected ID to be 1234, got: %d", r.GetID())
	}
}

func TestRateLimitTransport_abuseLimit_post_error(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
  "documentation_url": "https://developer.github.com/v3/#abuse-rate-limits"
}`,
			StatusCode: 403,
			ResponseHeaders: map[string]string{
				"Retry-After": "0.1",
			},
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "Repository creation failed.",
  "errors": [
    {
      "resource": "Repository",
      "code": "custom",
      "field": "name",
      "message": "name already exists on this account"
    }
  ],
  "documentation_url": "https://developer.github.com/v3/repos/#create"
}
`,
			StatusCode: 422,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRateLimitTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxId, t.Name())
	_, _, err := client.Repositories.Create(ctx, "tada", &github.Repository{
		Name:        github.Ptr("radek-example-48"),
		Description: github.Ptr(""),
	})
	if err == nil {
		t.Fatal("Expected 422 error, got nil")
	}

	var ghErr *github.ErrorResponse
	ok := errors.As(err, &ghErr)
	if !ok {
		t.Fatalf("Expected github.ErrorResponse, got: %#v", err)
	}

	expectedMessage := "Repository creation failed."
	if ghErr.Message != expectedMessage {
		t.Fatalf("Expected message %q, got: %q", expectedMessage, ghErr.Message)
	}
}

func TestRateLimitTransport_smart_lock(t *testing.T) {
	t.Run("With parallelRequests true it does not lock the rate limit transport", func(t *testing.T) {
		rlt := NewRateLimitTransport(http.DefaultTransport, WithParallelRequests(true))

		isSuccess := make(chan bool)
		go func() {
			rlt.m.Lock()
			rlt.smartLock(true)
			rlt.m.Unlock()
			isSuccess <- true
		}()
		select {
		case <-isSuccess:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Expected to succeed instantly, waited 100 milliseconds unsuccessfully")
		}
	})

	t.Run("With parallelRequests true it should not unlock the rate limit transport", func(t *testing.T) {
		rlt := NewRateLimitTransport(http.DefaultTransport, WithParallelRequests(true))

		isSuccess := make(chan bool)
		go func() {
			rlt.m.Lock()
			rlt.smartLock(false)
			rlt.m.Unlock()
			isSuccess <- true
		}()
		select {
		case <-isSuccess:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Expected to succeed instantly, waited 100 milliseconds unsuccessfully")
		}
	})

	t.Run("With parallelRequests false with a lock present it should get stuck waiting", func(t *testing.T) {
		rlt := NewRateLimitTransport(http.DefaultTransport, WithParallelRequests(false))

		isSuccess := make(chan bool)
		go func() {
			rlt.m.Lock()
			rlt.smartLock(true)
			isSuccess <- true
		}()
		select {
		case <-isSuccess:
			t.Fatalf("Expected get stuck waiting but it acquired the lock successfully")
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("With parallelRequests false and a lock present it should be able to unlock the rate limit transport", func(t *testing.T) {
		rlt := NewRateLimitTransport(http.DefaultTransport, WithParallelRequests(false))

		isSuccess := make(chan bool)
		go func() {
			rlt.m.Lock()
			rlt.smartLock(false)
			rlt.m.Lock()
			defer rlt.m.Unlock()
			isSuccess <- true
		}()
		select {
		case <-isSuccess:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Expected to succeed instantly, waited 100 milliseconds unsuccessfully")
		}
	})
}

func TestRetryTransport_retry_post_error(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "internal server error"
}`,
			StatusCode: 500,
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "internal server error"
}`,
			StatusCode: 500,
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "internal server error"
}`,
			StatusCode: 201,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRetryTransport(http.DefaultTransport, WithMaxRetries(1))

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxId, t.Name())
	_, _, err := client.Repositories.Create(ctx, "tada", &github.Repository{
		Name:        github.Ptr("radek-example-48"),
		Description: github.Ptr(""),
	})
	if err == nil {
		t.Fatal("Expected error not to be nil")
	}

	var ghErr *github.ErrorResponse
	ok := errors.As(err, &ghErr)
	if !ok {
		t.Fatalf("Expected github.ErrorResponse, got: %#v", err)
	}

	expectedMessage := "internal server error"
	if ghErr.Message != expectedMessage {
		t.Fatalf("Expected message %q, got: %q", expectedMessage, ghErr.Message)
	}
}

func TestRetryTransport_retry_post_success(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "internal server error"
}`,
			StatusCode: 500,
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "internal server error"
}`,
			StatusCode: 500,
		},
		{
			ExpectedUri:    "/orgs/tada/repos",
			ExpectedMethod: "POST",
			ExpectedBody: []byte(`{"name":"radek-example-48","description":""}
`),
			ResponseBody: `{
  "message": "Resource created"
}`,
			StatusCode: 201,
		},
	})
	defer ts.Close()

	httpClient := http.DefaultClient
	httpClient.Transport = NewRetryTransport(http.DefaultTransport, WithMaxRetries(2), WithRetryDelay(time.Second))

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(context.Background(), ctxId, t.Name())
	_, _, err := client.Repositories.Create(ctx, "tada", &github.Repository{
		Name:        github.Ptr("radek-example-48"),
		Description: github.Ptr(""),
	})
	if err != nil {
		t.Fatalf("Expected error to be nil, got %v", err)
	}

	var ghErr *github.ErrorResponse
	ok := errors.As(err, &ghErr)
	if ok {
		t.Fatalf("Expected successful github call, got: %q", ghErr.Message)
	}
}

type mockResponse struct {
	ExpectedUri     string
	ExpectedMethod  string
	ExpectedHeaders map[string]string
	ExpectedBody    []byte

	StatusCode      int
	ResponseHeaders map[string]string
	ResponseBody    string
}

func TestIsRulesetEndpoint(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/repos/owner/repo/rulesets", true},
		{"/repos/owner/repo/rulesets/123", true},
		{"/orgs/myorg/rulesets", true},
		{"/orgs/myorg/rulesets/456", true},
		{"/api/v3/repos/owner/repo/rulesets", true},
		{"/api/v3/orgs/myorg/rulesets/789", true},
		{"/repos/owner/repo/pulls", false},
		{"/orgs/myorg/teams", false},
		{"/users/someone", false},
	}

	for _, tt := range tests {
		got := isRulesetEndpoint(tt.path)
		if got != tt.expected {
			t.Errorf("isRulesetEndpoint(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestFixReviewerIDs(t *testing.T) {
	t.Run("converts string reviewer id to number", func(t *testing.T) {
		input := `{"id":13056455,"reviewer":{"id":"3827094","type":"Team"}}`
		result := string(fixReviewerIDs([]byte(input)))

		if strings.Contains(result, `"id":"3827094"`) {
			t.Errorf("Expected quoted id to be unquoted, got: %s", result)
		}
		if !strings.Contains(result, `"id":3827094`) {
			t.Errorf("Expected unquoted id 3827094, got: %s", result)
		}
		// The ruleset's own numeric id should be unchanged
		if !strings.Contains(result, `"id":13056455`) {
			t.Errorf("Expected ruleset id to remain unchanged, got: %s", result)
		}
	})

	t.Run("preserves already-numeric reviewer id", func(t *testing.T) {
		input := `{"reviewer":{"id":12345,"type":"Team"}}`
		result := string(fixReviewerIDs([]byte(input)))
		if result != input {
			t.Errorf("Expected no change, got: %s", result)
		}
	})

	t.Run("handles multiple reviewers with string ids", func(t *testing.T) {
		input := `{"required_reviewers":[{"reviewer":{"id":"111","type":"Team"}},{"reviewer":{"id":"222","type":"Team"}}]}`
		result := string(fixReviewerIDs([]byte(input)))

		if strings.Contains(result, `"id":"`) {
			t.Errorf("Expected all quoted ids to be unquoted, got: %s", result)
		}
		if !strings.Contains(result, `"id":111`) || !strings.Contains(result, `"id":222`) {
			t.Errorf("Expected both ids as numbers, got: %s", result)
		}
	})

	t.Run("does not change non-numeric string ids", func(t *testing.T) {
		input := `{"id":"not-a-number"}`
		result := string(fixReviewerIDs([]byte(input)))
		if result != input {
			t.Errorf("Expected no change for non-numeric id, got: %s", result)
		}
	})

	t.Run("returns unchanged when no quoted ids", func(t *testing.T) {
		input := `{"id":123,"name":"test-ruleset"}`
		result := string(fixReviewerIDs([]byte(input)))
		if result != input {
			t.Errorf("Expected no change, got: %s", result)
		}
	})
}

func TestRulesetResponseFixTransport(t *testing.T) {
	// Simulate the real GitHub API response where reviewer.id is a string
	rulesetResponse := `{
		"id": 13056455,
		"name": "test-rule",
		"enforcement": "active",
		"rules": [
			{
				"type": "pull_request",
				"parameters": {
					"dismiss_stale_reviews_on_push": true,
					"require_code_owner_review": false,
					"require_last_push_approval": true,
					"required_approving_review_count": 1,
					"required_review_thread_resolution": false,
					"required_reviewers": [
						{
							"minimum_approvals": 1,
							"file_patterns": ["*"],
							"reviewer": {
								"id": "3827094",
								"type": "Team"
							}
						}
					]
				}
			}
		]
	}`

	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:  "/repos/test/repo/rulesets/123?includes_parents=false",
			ResponseBody: rulesetResponse,
			StatusCode:   200,
		},
	})
	defer ts.Close()

	httpClient := &http.Client{}
	httpClient.Transport = NewRulesetResponseFixTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	ruleset, _, err := client.Repositories.GetRuleset(context.Background(), "test", "repo", 123, false)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if ruleset.GetID() != 13056455 {
		t.Errorf("Expected ruleset ID 13056455, got %d", ruleset.GetID())
	}

	if ruleset.Rules == nil || ruleset.Rules.PullRequest == nil {
		t.Fatal("Expected pull request rules to be present")
	}

	if len(ruleset.Rules.PullRequest.RequiredReviewers) != 1 {
		t.Fatalf("Expected 1 required reviewer, got %d", len(ruleset.Rules.PullRequest.RequiredReviewers))
	}

	reviewer := ruleset.Rules.PullRequest.RequiredReviewers[0]
	if reviewer.Reviewer == nil {
		t.Fatal("Expected reviewer to be present")
	}
	if reviewer.Reviewer.GetID() != 3827094 {
		t.Errorf("Expected reviewer ID 3827094, got %d", reviewer.Reviewer.GetID())
	}
}

func TestRulesetResponseFixTransport_nonRulesetEndpoint(t *testing.T) {
	ts := githubApiMock([]*mockResponse{
		{
			ExpectedUri:  "/repos/test/blah",
			ResponseBody: `{"id": 5678}`,
			StatusCode:   200,
		},
	})
	defer ts.Close()

	httpClient := &http.Client{}
	httpClient.Transport = NewRulesetResponseFixTransport(http.DefaultTransport)

	client := github.NewClient(httpClient)
	u, _ := url.Parse(ts.URL + "/")
	client.BaseURL = u

	r, _, err := client.Repositories.Get(context.Background(), "test", "blah")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if r.GetID() != 5678 {
		t.Errorf("Expected ID 5678, got %d", r.GetID())
	}
}

package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GetRepository = "github__get-repository"
	ListIssues    = "github__list-issues"
	GetIssue      = "github__get-issue"

	maxResponseBytes = 1 << 20
	maxItems         = 20
)

var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

type Client struct {
	http *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableKeepAlives = true
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("redirects are not allowed")
			},
		}
	}
	return &Client{http: httpClient}
}

func IsTool(name string) bool {
	return name == GetRepository || name == ListIssues || name == GetIssue
}

func Definitions(description string) []any {
	owner := map[string]any{"type": "string", "minLength": 1, "maxLength": 39}
	repo := map[string]any{"type": "string", "minLength": 1, "maxLength": 100}
	base := func(properties map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	definition := func(name, operation string, input, output map[string]any) any {
		return map[string]any{
			"name":         name,
			"description":  description + "\n\n" + operation,
			"inputSchema":  input,
			"outputSchema": output,
			"annotations":  map[string]any{"readOnlyHint": true, "idempotentHint": true, "openWorldHint": true},
		}
	}
	repositoryOutput := base(map[string]any{
		"full_name":         map[string]any{"type": "string"},
		"description":       map[string]any{"type": "string"},
		"html_url":          map[string]any{"type": "string"},
		"default_branch":    map[string]any{"type": "string"},
		"archived":          map[string]any{"type": "boolean"},
		"fork":              map[string]any{"type": "boolean"},
		"open_issues_count": map[string]any{"type": "integer"},
		"updated_at":        map[string]any{"type": "string"},
		"truncated":         map[string]any{"type": "boolean"},
	}, []string{"full_name", "description", "html_url", "default_branch", "archived", "fork", "open_issues_count", "updated_at", "truncated"})
	issue := base(map[string]any{
		"number":          map[string]any{"type": "integer"},
		"title":           map[string]any{"type": "string"},
		"state":           map[string]any{"type": "string"},
		"body":            map[string]any{"type": "string"},
		"html_url":        map[string]any{"type": "string"},
		"author":          map[string]any{"type": "string"},
		"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20},
		"is_pull_request": map[string]any{"type": "boolean"},
		"created_at":      map[string]any{"type": "string"},
		"updated_at":      map[string]any{"type": "string"},
		"truncated":       map[string]any{"type": "boolean"},
	}, []string{"number", "title", "state", "body", "html_url", "author", "labels", "is_pull_request", "created_at", "updated_at", "truncated"})
	return []any{
		definition(GetRepository, "Get one public repository.", base(map[string]any{"owner": owner, "repo": repo}, []string{"owner", "repo"}), repositoryOutput),
		definition(ListIssues, "List public issues and pull requests.", base(map[string]any{
			"owner": owner,
			"repo":  repo,
			"state": map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxItems},
		}, []string{"owner", "repo"}), base(map[string]any{"issues": map[string]any{"type": "array", "items": issue, "maxItems": maxItems}}, []string{"issues"})),
		definition(GetIssue, "Get one public issue or pull request.", base(map[string]any{"owner": owner, "repo": repo, "number": map[string]any{"type": "integer", "minimum": 1}}, []string{"owner", "repo", "number"}), issue),
	}
}

func (client *Client) Call(ctx context.Context, name string, arguments json.RawMessage) ([]byte, error) {
	switch name {
	case GetRepository:
		var input repositoryInput
		if err := decodeInput(arguments, &input); err != nil || !validRepository(input.Owner, input.Repo) {
			return nil, errors.New("invalid GitHub repository input")
		}
		var response repositoryResponse
		if err := client.get(ctx, repositoryPath(input.Owner, input.Repo), &response); err != nil {
			return nil, err
		}
		if err := response.validate(); err != nil {
			return nil, err
		}
		return json.Marshal(response.output())
	case ListIssues:
		var input listIssuesInput
		if err := decodeInput(arguments, &input); err != nil || !validRepository(input.Owner, input.Repo) || !input.valid() {
			return nil, errors.New("invalid GitHub issue-list input")
		}
		state, limit := input.values()
		query := url.Values{"state": {state}, "per_page": {fmt.Sprint(limit)}}
		var response []issueResponse
		if err := client.get(ctx, repositoryPath(input.Owner, input.Repo)+"/issues?"+query.Encode(), &response); err != nil {
			return nil, err
		}
		if len(response) > limit {
			response = response[:limit]
		}
		issues := make([]issueOutput, len(response))
		for index := range response {
			if err := response[index].validate(); err != nil {
				return nil, err
			}
			issues[index] = response[index].output()
		}
		return json.Marshal(map[string]any{"issues": issues})
	case GetIssue:
		var input issueInput
		if err := decodeInput(arguments, &input); err != nil || !validRepository(input.Owner, input.Repo) || input.Number < 1 {
			return nil, errors.New("invalid GitHub issue input")
		}
		var response issueResponse
		if err := client.get(ctx, repositoryPath(input.Owner, input.Repo)+"/issues/"+fmt.Sprint(input.Number), &response); err != nil {
			return nil, err
		}
		if err := response.validate(); err != nil {
			return nil, err
		}
		return json.Marshal(response.output())
	default:
		return nil, errors.New("unknown GitHub operation")
	}
}

func (client *Client) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return errors.New("cannot create GitHub request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "hctl")
	response, err := client.http.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return errors.New("GitHub request timed out")
		}
		return errors.New("GitHub request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("cannot read GitHub response")
	}
	if len(body) > maxResponseBytes {
		return errors.New("GitHub response exceeded byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return errors.New("GitHub returned an invalid response")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("GitHub returned an invalid response")
	}
	return nil
}

func statusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return errors.New("GitHub resource not found")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return errors.New("GitHub request rate limited")
	case http.StatusUnauthorized:
		return errors.New("GitHub request unauthorized")
	default:
		if status >= 500 {
			return errors.New("GitHub service unavailable")
		}
		return errors.New("GitHub request failed")
	}
}

type repositoryInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

type listIssuesInput struct {
	repositoryInput
	State *string `json:"state,omitempty"`
	Limit *int    `json:"limit,omitempty"`
}

func (input listIssuesInput) valid() bool {
	if input.State != nil && *input.State != "open" && *input.State != "closed" && *input.State != "all" {
		return false
	}
	return input.Limit == nil || *input.Limit >= 1 && *input.Limit <= maxItems
}

func (input listIssuesInput) values() (string, int) {
	state, limit := "open", 10
	if input.State != nil {
		state = *input.State
	}
	if input.Limit != nil {
		limit = *input.Limit
	}
	return state, limit
}

type issueInput struct {
	repositoryInput
	Number int `json:"number"`
}

type repositoryResponse struct {
	FullName        string  `json:"full_name"`
	Description     *string `json:"description"`
	HTMLURL         string  `json:"html_url"`
	DefaultBranch   string  `json:"default_branch"`
	Archived        *bool   `json:"archived"`
	Fork            *bool   `json:"fork"`
	OpenIssuesCount *int    `json:"open_issues_count"`
	UpdatedAt       string  `json:"updated_at"`
}

type repositoryOutput struct {
	FullName        string `json:"full_name"`
	Description     string `json:"description"`
	HTMLURL         string `json:"html_url"`
	DefaultBranch   string `json:"default_branch"`
	Archived        bool   `json:"archived"`
	Fork            bool   `json:"fork"`
	OpenIssuesCount int    `json:"open_issues_count"`
	UpdatedAt       string `json:"updated_at"`
	Truncated       bool   `json:"truncated"`
}

func (response repositoryResponse) validate() error {
	if response.FullName == "" || response.HTMLURL == "" || response.DefaultBranch == "" || response.Archived == nil || response.Fork == nil || response.OpenIssuesCount == nil || *response.OpenIssuesCount < 0 || response.UpdatedAt == "" {
		return errors.New("GitHub returned an invalid repository response")
	}
	return nil
}

func (response repositoryResponse) output() repositoryOutput {
	description := ""
	truncated := false
	if response.Description != nil {
		description, truncated = truncateWithFlag(*response.Description, 2048)
	}
	fullName, fullNameTruncated := truncateWithFlag(response.FullName, 256)
	htmlURL, htmlURLTruncated := truncateWithFlag(response.HTMLURL, 2048)
	defaultBranch, defaultBranchTruncated := truncateWithFlag(response.DefaultBranch, 256)
	updatedAt, updatedAtTruncated := truncateWithFlag(response.UpdatedAt, 64)
	return repositoryOutput{
		FullName: fullName, Description: description, HTMLURL: htmlURL,
		DefaultBranch: defaultBranch, Archived: *response.Archived, Fork: *response.Fork,
		OpenIssuesCount: *response.OpenIssuesCount, UpdatedAt: updatedAt,
		Truncated: truncated || fullNameTruncated || htmlURLTruncated || defaultBranchTruncated || updatedAtTruncated,
	}
}

type issueResponse struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Body    *string `json:"body"`
	HTMLURL string  `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type issueOutput struct {
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	State         string   `json:"state"`
	Body          string   `json:"body"`
	HTMLURL       string   `json:"html_url"`
	Author        string   `json:"author"`
	Labels        []string `json:"labels"`
	IsPullRequest bool     `json:"is_pull_request"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	Truncated     bool     `json:"truncated"`
}

func (response issueResponse) validate() error {
	if response.Number < 1 || response.Title == "" || (response.State != "open" && response.State != "closed") || response.HTMLURL == "" || response.User.Login == "" || response.Labels == nil || response.CreatedAt == "" || response.UpdatedAt == "" {
		return errors.New("GitHub returned an invalid issue response")
	}
	return nil
}

func (response issueResponse) output() issueOutput {
	body := ""
	truncated := false
	if response.Body != nil {
		body, truncated = truncateWithFlag(*response.Body, 8192)
	}
	labels := make([]string, 0, min(len(response.Labels), maxItems))
	for _, label := range response.Labels {
		if len(labels) == maxItems {
			truncated = true
			break
		}
		name, labelTruncated := truncateWithFlag(label.Name, 256)
		truncated = truncated || labelTruncated
		labels = append(labels, name)
	}
	title, titleTruncated := truncateWithFlag(response.Title, 512)
	htmlURL, htmlURLTruncated := truncateWithFlag(response.HTMLURL, 2048)
	author, authorTruncated := truncateWithFlag(response.User.Login, 256)
	createdAt, createdAtTruncated := truncateWithFlag(response.CreatedAt, 64)
	updatedAt, updatedAtTruncated := truncateWithFlag(response.UpdatedAt, 64)
	return issueOutput{
		Number: response.Number, Title: title, State: response.State, Body: body,
		HTMLURL: htmlURL, Author: author, Labels: labels,
		IsPullRequest: len(response.PullRequest) > 0 && string(response.PullRequest) != "null",
		CreatedAt:     createdAt, UpdatedAt: updatedAt,
		Truncated: truncated || titleTruncated || htmlURLTruncated || authorTruncated || createdAtTruncated || updatedAtTruncated,
	}
}

func repositoryPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func validRepository(owner, repo string) bool {
	return ownerName.MatchString(owner) && repoName.MatchString(repo) && repo != "." && repo != ".."
}

func decodeInput(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

func truncateWithFlag(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value), true
}

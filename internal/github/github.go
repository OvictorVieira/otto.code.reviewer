// Package github talks to GitHub through the user's own `gh` CLI login, so
// Otto never stores a token and every review is posted as the user.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
)

// PR is the pull request data a review needs.
type PR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	BaseRefName string `json:"baseRefName"`
	HeadRefOid  string `json:"headRefOid"`
}

func gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err,
			strings.TrimSpace(stderr.String()+" "+stdout.String()))
	}
	return stdout.Bytes(), nil
}

// Login returns the authenticated GitHub user.
func Login(ctx context.Context) (string, error) {
	out, err := gh(ctx, nil, "api", "user", "--jq", ".login")
	return strings.TrimSpace(string(out)), err
}

// GetPR loads one pull request.
func GetPR(ctx context.Context, repo string, number int) (PR, error) {
	out, err := gh(ctx, nil, "pr", "view", fmt.Sprint(number), "-R", repo,
		"--json", "number,title,body,url,baseRefName,headRefOid")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	return pr, json.Unmarshal(out, &pr)
}

// Diff returns the unified diff of a pull request.
func Diff(ctx context.Context, repo string, number int) (string, error) {
	out, err := gh(ctx, nil, "pr", "diff", fmt.Sprint(number), "-R", repo)
	return string(out), err
}

// PostReview creates a pull request review from a JSON payload.
func PostReview(ctx context.Context, repo string, number int, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = gh(ctx, b, "api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number), "--input", "-")
	return err
}

// Comment posts a plain comment on the pull request conversation.
func Comment(ctx context.Context, repo string, number int, body string) error {
	_, err := gh(ctx, nil, "pr", "comment", fmt.Sprint(number), "-R", repo, "--body", body)
	return err
}

// React adds a reaction (eyes, +1, confused, ...) to an issue comment.
func React(ctx context.Context, repo string, commentID int64, content string) error {
	_, err := gh(ctx, nil, "api", "-X", "POST", fmt.Sprintf("repos/%s/issues/comments/%d/reactions", repo, commentID),
		"-f", "content="+content)
	return err
}

// CommentsSince lists conversation comments of every issue and pull request
// in repo updated since the given time.
func CommentsSince(ctx context.Context, repo string, since time.Time) ([]trigger.Comment, error) {
	path := fmt.Sprintf("repos/%s/issues/comments?per_page=100&sort=updated&direction=asc&since=%s",
		repo, since.UTC().Format(time.RFC3339))
	out, err := gh(ctx, nil, "api", "--paginate", path,
		"--jq", `.[] | {id, body, html_url, created_at, login: .user.login}`)
	if err != nil {
		return nil, err
	}
	var comments []trigger.Comment
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var c struct {
			ID        int64     `json:"id"`
			Body      string    `json:"body"`
			HTMLURL   string    `json:"html_url"`
			CreatedAt time.Time `json:"created_at"`
			Login     string    `json:"login"`
		}
		if err := dec.Decode(&c); err != nil {
			return nil, err
		}
		comments = append(comments, trigger.Comment{
			ID: c.ID, Repo: repo, Author: c.Login, Body: c.Body, HTMLURL: c.HTMLURL, CreatedAt: c.CreatedAt,
		})
	}
	return comments, nil
}

// Repos lists the non-archived repositories the user can access, most
// recently pushed first.
func Repos(ctx context.Context) ([]string, error) {
	out, err := gh(ctx, nil, "api", "--paginate",
		"user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member",
		"--jq", `.[] | select(.archived | not) | .full_name`)
	if err != nil {
		return nil, err
	}
	var repos []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			repos = append(repos, line)
		}
	}
	return repos, nil
}

// CLI implements the app's GitHub port with the functions above.
type CLI struct{}

func (CLI) Login(ctx context.Context) (string, error)                    { return Login(ctx) }
func (CLI) GetPR(ctx context.Context, repo string, n int) (PR, error)    { return GetPR(ctx, repo, n) }
func (CLI) Diff(ctx context.Context, repo string, n int) (string, error) { return Diff(ctx, repo, n) }
func (CLI) Repos(ctx context.Context) ([]string, error)                  { return Repos(ctx) }
func (CLI) Comment(ctx context.Context, repo string, n int, body string) error {
	return Comment(ctx, repo, n, body)
}
func (CLI) PostReview(ctx context.Context, repo string, n int, payload any) error {
	return PostReview(ctx, repo, n, payload)
}
func (CLI) React(ctx context.Context, repo string, id int64, content string) error {
	return React(ctx, repo, id, content)
}
func (CLI) CommentsSince(ctx context.Context, repo string, since time.Time) ([]trigger.Comment, error) {
	return CommentsSince(ctx, repo, since)
}

// Package github talks to GitHub through the user's own `gh` CLI login, so
// Otto never stores a token and every review is posted as the user.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/ci"
	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
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

type tokenKey struct{}

// WithToken makes every gh call made with the returned context act as the
// holder of token (for example a GitHub App installation) instead of the
// user's own `gh` login.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenFrom returns the token set by WithToken, or "" for the user's own login.
func TokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

func gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	if tok := TokenFrom(ctx); tok != "" {
		cmd.Env = append(os.Environ(), "GH_TOKEN="+tok)
	}
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

// RateLimited reports whether err is GitHub refusing a call because the account's API budget
// for the hour is spent. Every further call then fails too, so callers stop until it resets.
func RateLimited(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "rate limit")
}

// Transient reports whether err is a GitHub failure that goes away on its own: the rate limit,
// a timeout or an unavailable server. A request that failed this way is retried, not reported.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	if RateLimited(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"deadline exceeded", "timeout", "timed out", "connection reset", "connection refused",
		"eof", "bad gateway", "service unavailable", "gateway timeout", "http 502", "http 503", "http 504", "could not resolve host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// FailureMarker tags the comment Otto posts when a review could not finish, so a later
// successful review can remove it.
const FailureMarker = "<!-- otto-review-failure:v1 -->"

// failureTitles also find failure comments posted before the marker existed.
var failureTitles = []string{"### Otto não conseguiu concluir este review", "### Otto could not finish this review"}

// ClearFailures removes what earlier failed attempts left on a pull request: Otto's failure
// comments and the 😕 reactions on trigger comments. Only comments and reactions made by
// login or by a bot are touched, and only Otto's own failure comments are deleted.
func ClearFailures(ctx context.Context, repo string, number int, login string) error {
	out, err := gh(ctx, nil, "api", "--paginate", fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repo, number),
		"--jq", `.[] | {id, body, login: .user.login, type: .user.type, confused: .reactions.confused}`)
	if err != nil {
		return err
	}
	ours := func(who, kind string) bool { return kind == "Bot" || strings.EqualFold(who, login) }
	dec := json.NewDecoder(bytes.NewReader(out))
	var errs []error
	for dec.More() {
		var c struct {
			ID       int64  `json:"id"`
			Body     string `json:"body"`
			Login    string `json:"login"`
			Type     string `json:"type"`
			Confused int    `json:"confused"`
		}
		if err := dec.Decode(&c); err != nil {
			return err
		}
		if ours(c.Login, c.Type) && isFailure(c.Body) {
			if _, err := gh(ctx, nil, "api", "-X", "DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", repo, c.ID)); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if c.Confused == 0 {
			continue
		}
		rs, err := gh(ctx, nil, "api", fmt.Sprintf("repos/%s/issues/comments/%d/reactions?content=confused", repo, c.ID),
			"--jq", `.[] | {id, login: .user.login, type: .user.type}`)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rdec := json.NewDecoder(bytes.NewReader(rs))
		for rdec.More() {
			var r struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
				Type  string `json:"type"`
			}
			if err := rdec.Decode(&r); err != nil {
				return err
			}
			if !ours(r.Login, r.Type) {
				continue
			}
			if _, err := gh(ctx, nil, "api", "-X", "DELETE", fmt.Sprintf("repos/%s/issues/comments/%d/reactions/%d", repo, c.ID, r.ID)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func isFailure(body string) bool {
	if strings.Contains(body, FailureMarker) {
		return true
	}
	for _, t := range failureTitles {
		if strings.HasPrefix(strings.TrimSpace(body), t) {
			return true
		}
	}
	return false
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

const threadsQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){
reviewThreads(first:100){nodes{isResolved path line comments(first:30){nodes{databaseId body author{login}
reactionGroups{content reactors(first:20){totalCount nodes{... on User{login} ... on Bot{login}}}}}}}}}}}`

// Threads lists the review threads of a pull request with their reactions and
// replies. It is what Otto learns from.
func Threads(ctx context.Context, repo string, number int) ([]learn.Thread, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("not a repository: %q", repo)
	}
	out, err := gh(ctx, nil, "api", "graphql", "-f", "query="+threadsQuery,
		"-F", "owner="+owner, "-F", "name="+name, "-F", fmt.Sprintf("number=%d", number))
	if err != nil {
		return nil, err
	}
	return parseThreads(out)
}

func parseThreads(out []byte) ([]learn.Thread, error) {
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool   `json:"isResolved"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64  `json:"databaseId"`
									Body       string `json:"body"`
									Author     struct {
										Login string `json:"login"`
									} `json:"author"`
									ReactionGroups []struct {
										Content  string `json:"content"`
										Reactors struct {
											TotalCount int `json:"totalCount"`
											Nodes      []struct {
												Login string `json:"login"`
											} `json:"nodes"`
										} `json:"reactors"`
									} `json:"reactionGroups"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	var threads []learn.Thread
	for _, n := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
		t := learn.Thread{Path: n.Path, Line: n.Line, Resolved: n.IsResolved}
		for _, c := range n.Comments.Nodes {
			tc := learn.ThreadComment{ID: c.DatabaseID, Author: c.Author.Login, Body: c.Body}
			for _, g := range c.ReactionGroups {
				var who []string
				for _, n := range g.Reactors.Nodes {
					who = append(who, n.Login)
				}
				switch g.Content {
				case "THUMBS_UP":
					tc.Up, tc.UpBy = g.Reactors.TotalCount, who
				case "THUMBS_DOWN":
					tc.Down, tc.DownBy = g.Reactors.TotalCount, who
				}
			}
			t.Comments = append(t.Comments, tc)
		}
		threads = append(threads, t)
	}
	return threads, nil
}

// RecentPRs lists the numbers of the most recently updated pull requests, open or closed,
// newest first, keeping only the ones updated at or after since.
func RecentPRs(ctx context.Context, repo string, since time.Time, limit int) ([]int, error) {
	out, err := gh(ctx, nil, "api",
		fmt.Sprintf("repos/%s/pulls?state=all&sort=updated&direction=desc&per_page=%d", repo, limit),
		"--jq", `.[] | {number, updated_at}`)
	if err != nil {
		return nil, err
	}
	var nums []int
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p struct {
			Number    int       `json:"number"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		if !p.UpdatedAt.Before(since) {
			nums = append(nums, p.Number)
		}
	}
	return nums, nil
}

// Issue reads the title and body of an issue (or pull request) in repo.
func Issue(ctx context.Context, repo string, number int) (string, string, error) {
	out, err := gh(ctx, nil, "issue", "view", fmt.Sprint(number), "-R", repo, "--json", "title,body")
	if err != nil {
		return "", "", err
	}
	var i struct{ Title, Body string }
	return i.Title, i.Body, json.Unmarshal(out, &i)
}

// Checks lists the CI check runs of a commit.
func Checks(ctx context.Context, repo, sha string) ([]ci.Check, error) {
	out, err := gh(ctx, nil, "api", "--paginate", fmt.Sprintf("repos/%s/commits/%s/check-runs?per_page=100", repo, sha),
		"--jq", `.check_runs[] | {id, name, status, conclusion: (.conclusion // ""), url: .html_url, app: (.app.slug // "")}`)
	if err != nil {
		return nil, err
	}
	var checks []ci.Check
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var c struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"url"`
			App        string `json:"app"`
		}
		if err := dec.Decode(&c); err != nil {
			return nil, err
		}
		checks = append(checks, ci.Check{ID: c.ID, Name: c.Name, Status: c.Status, Conclusion: c.Conclusion,
			URL: c.URL, Actions: c.App == "github-actions"})
	}
	return checks, nil
}

// JobLog downloads the log of a GitHub Actions job (its id is the check run id).
func JobLog(ctx context.Context, repo string, jobID int64) (string, error) {
	// CI logs carry color codes; gh refuses to print them unless told to (they are stripped later).
	out, err := gh(ctx, nil, "api", "--allow-escape-sequences", fmt.Sprintf("repos/%s/actions/jobs/%d/logs", repo, jobID))
	return string(out), err
}

// Repo is a repository the user can access and when something was last pushed to it.
type Repo struct {
	Name   string    `json:"full_name"`
	Pushed time.Time `json:"pushed_at"`
}

// Repos lists the non-archived repositories the user can access, most
// recently pushed first.
func Repos(ctx context.Context) ([]Repo, error) {
	out, err := gh(ctx, nil, "api", "--paginate",
		"user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member",
		"--jq", `.[] | select(.archived | not) | {full_name, pushed_at}`)
	if err != nil {
		return nil, err
	}
	var repos []Repo
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var r Repo
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, nil
}

// CLI implements the app's GitHub port with the functions above.
type CLI struct{}

func (CLI) Login(ctx context.Context) (string, error)                    { return Login(ctx) }
func (CLI) GetPR(ctx context.Context, repo string, n int) (PR, error)    { return GetPR(ctx, repo, n) }
func (CLI) Diff(ctx context.Context, repo string, n int) (string, error) { return Diff(ctx, repo, n) }
func (CLI) Repos(ctx context.Context) ([]Repo, error)                    { return Repos(ctx) }
func (CLI) Threads(ctx context.Context, repo string, n int) ([]learn.Thread, error) {
	return Threads(ctx, repo, n)
}
func (CLI) RecentPRs(ctx context.Context, repo string, since time.Time, limit int) ([]int, error) {
	return RecentPRs(ctx, repo, since, limit)
}
func (CLI) Issue(ctx context.Context, repo string, n int) (string, string, error) {
	return Issue(ctx, repo, n)
}
func (CLI) Checks(ctx context.Context, repo, sha string) ([]ci.Check, error) {
	return Checks(ctx, repo, sha)
}
func (CLI) JobLog(ctx context.Context, repo string, id int64) (string, error) {
	return JobLog(ctx, repo, id)
}
func (CLI) Comment(ctx context.Context, repo string, n int, body string) error {
	return Comment(ctx, repo, n, body)
}
func (CLI) PostReview(ctx context.Context, repo string, n int, payload any) error {
	return PostReview(ctx, repo, n, payload)
}
func (CLI) ClearFailures(ctx context.Context, repo string, n int, login string) error {
	return ClearFailures(ctx, repo, n, login)
}
func (CLI) React(ctx context.Context, repo string, id int64, content string) error {
	return React(ctx, repo, id, content)
}
func (CLI) CommentsSince(ctx context.Context, repo string, since time.Time) ([]trigger.Comment, error) {
	return CommentsSince(ctx, repo, since)
}

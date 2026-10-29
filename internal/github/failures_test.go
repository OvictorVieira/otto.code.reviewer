package github

import (
	"errors"
	"testing"
)

func TestFailureCommentsAreRecognizedWithOrWithoutTheMarker(t *testing.T) {
	for _, body := range []string{
		"### Otto não conseguiu concluir este review\n\nOcorreu um erro inesperado.",
		"### Otto could not finish this review\n\n...",
		"anything\n\n" + FailureMarker,
	} {
		if !isFailure(body) {
			t.Errorf("not recognized: %q", body)
		}
	}
	for _, body := range []string{"@otto-assist review", "LGTM, Otto could not finish this review earlier"} {
		if isFailure(body) {
			t.Errorf("recognized a comment that is not a failure notice: %q", body)
		}
	}
}

func TestTransientGitHubErrors(t *testing.T) {
	for _, msg := range []string{
		"gh pr diff: exit status 1: GraphQL: API rate limit already exceeded for user ID 1.",
		"gh api: exit status 1: HTTP 502: Bad Gateway",
		"gh pr view: context deadline exceeded",
	} {
		if !Transient(errors.New(msg)) {
			t.Errorf("not transient: %s", msg)
		}
	}
	if Transient(errors.New("gh pr view: exit status 1: Could not resolve to a PullRequest with the number of 9")) {
		t.Error("a missing pull request is not temporary")
	}
}

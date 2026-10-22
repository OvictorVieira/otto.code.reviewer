package engine

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FailureKind says why one engine could not produce a review, in terms a person can act on.
type FailureKind string

const (
	FailQuota      FailureKind = "quota"
	FailAuth       FailureKind = "auth"
	FailMissing    FailureKind = "missing"
	FailBusy       FailureKind = "busy"
	FailPermission FailureKind = "permission"
	FailNetwork    FailureKind = "network"
	FailTimeout    FailureKind = "timeout"
	FailInvalid    FailureKind = "invalid"
	FailOther      FailureKind = "other"
)

// EngineFailure is what is safe to tell the pull request about one failed engine:
// its name, the kind of problem, and at most a short reset note the provider gave.
// Raw engine output never goes in here; it can carry code from the diff.
type EngineFailure struct {
	Engine  string
	Kind    FailureKind
	ResetAt string
}

// EngineError is the failure of one named engine.
type EngineError struct {
	Name string
	Err  error
}

func (e *EngineError) Error() string { return e.Name + ": " + e.Err.Error() }
func (e *EngineError) Unwrap() error { return e.Err }

// Failures explains, engine by engine, why a pool run failed. It returns nothing
// for an error that did not come from the engines.
func Failures(err error) []EngineFailure {
	var all allFailedError
	if !errors.As(err, &all) {
		return nil
	}
	var out []EngineFailure
	for _, failure := range all.failures {
		var engineErr *EngineError
		if errors.As(failure, &engineErr) {
			out = append(out, classify(engineErr))
		}
	}
	return out
}

var (
	tryAgainAt = regexp.MustCompile(`(?i)try again at ([^\n]+?)\.?\s*(?:\n|$)`)
	// Claude's subscription limit names its reset as a Unix time after a pipe.
	claudeLimitReset = regexp.MustCompile(`(?i)usage limit reached\|(\d{9,11})`)
)

func classify(e *EngineError) EngineFailure {
	text := e.Err.Error()
	lower := strings.ToLower(text)
	failure := EngineFailure{Engine: e.Name, Kind: FailOther}
	var quota *QuotaError
	switch {
	case errors.As(e.Err, &quota) || isQuotaMessage(text):
		failure.Kind = FailQuota
		failure.ResetAt = resetNote(quota, text)
	case containsAny(lower, "not logged in", "please run /login", "log in", "login required", "unauthorized", "authentication", "not authenticated", "invalid api key", "invalid x-api-key", "token has expired", "invalid authentication"):
		failure.Kind = FailAuth
	case containsAny(lower, "rejected permission", "auto-rejecting", "permission denied"):
		failure.Kind = FailPermission
	case containsAny(lower, "executable file not found"):
		failure.Kind = FailMissing
	case containsAny(lower, "overloaded", "service unavailable", "repeated retries", "at capacity"):
		failure.Kind = FailBusy
	case containsAny(lower, "dial tcp", "no such host", "connection lost", "connection refused", "connection reset", "network is unreachable", "i/o timeout"):
		failure.Kind = FailNetwork
	case errors.Is(e.Err, context.DeadlineExceeded) || containsAny(lower, "deadline exceeded", "timed out", "timeout"):
		failure.Kind = FailTimeout
	case containsAny(lower, "no review json", "not valid json", "invalid review"):
		failure.Kind = FailInvalid
	}
	return failure
}

// resetNote keeps only what the provider said about when the quota returns: the
// "try again at" time, or a short single-line note. A longer dump is dropped.
func resetNote(quota *QuotaError, text string) string {
	if quota != nil {
		if note := strings.TrimSpace(quota.Details); note != "" {
			if m := tryAgainAt.FindStringSubmatch(note); m != nil {
				return strings.TrimSpace(m[1])
			}
			if !strings.Contains(note, "\n") && len(note) <= 120 {
				return note
			}
			return ""
		}
	}
	if m := claudeLimitReset.FindStringSubmatch(text); m != nil {
		if unix, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(unix, 0).Local().Format("Jan 2, 2006 3:04 PM MST")
		}
	}
	if m := tryAgainAt.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func containsAny(s string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

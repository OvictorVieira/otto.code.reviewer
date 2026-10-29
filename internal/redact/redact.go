// Package redact hides credentials before text leaves the machine: CI log excerpts sent to a model,
// and every review Otto posts. A pull request can try to make a model reveal a secret it read; this
// is the last line of defense if it does.
package redact

import "regexp"

var (
	privateKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	tokens     = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
		regexp.MustCompile(`\bnapi_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), // JWT
	}
	// keyValue keeps the name and hides the value: "password: hunter2xx" -> "password: [REDACTED]".
	keyValue = regexp.MustCompile(`(?i)\b(bearer|token|password|passwd|secret|api[_-]?key|access[_-]?key|client[_-]?secret)(["'\s:=]+)[^\s"'<>]{8,}`)
)

// Secrets replaces anything that looks like a credential with [REDACTED].
func Secrets(s string) string {
	s = privateKey.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	for _, re := range tokens {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return keyValue.ReplaceAllString(s, "${1}${2}[REDACTED]")
}

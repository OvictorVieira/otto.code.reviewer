// Package format renders a review in the same layout Codex uses on GitHub:
// colored P0-P3 badges, inline comments and a summary review body.
package format

import (
	"errors"
	"fmt"
	"github.com/OvictorVieira/otto.code.reviewer/internal/redact"
	"slices"
	"strings"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

var badgeColor = map[int]string{0: "red", 1: "orange", 2: "yellow", 3: "lightgrey"}

var checkIcon = map[string]string{"passed": "✅", "failed": "❌", "skipped": "⏭️"}

var specIcon = map[string]string{"met": "🟢", "partial": "🟡", "missing": "🔴", "wrong": "🔴"}

func note(n string) string {
	if n == "" {
		return ""
	}
	return " — " + n
}

// Meta describes which engine produced the review.
type Meta struct {
	Engine  string
	Model   string
	Effort  string
	Trigger string // defaults to "/otto review"
	Size    string // how big Otto judged the pull request, and why
	// Parts is how many reviewers shared a large pull request (0 or 1 = one reviewer).
	Parts int
	// Unreviewed are files no engine could review; the review is partial when it is not empty.
	Unreviewed []string
	// IntegrationSkipped says the pass across parts did not produce an answer.
	IntegrationSkipped bool
	// Requester is who asked for the review. It is shown only when Otto posts
	// as its own identity; a review posted as the user needs no attribution.
	Requester string
}

// Badge returns the small colored severity badge.
func Badge(priority int) string {
	color, ok := badgeColor[priority]
	if !ok {
		priority, color = 2, badgeColor[2]
	}
	return fmt.Sprintf("<sub><sub>![P%d Badge](https://img.shields.io/badge/P%d-%s?style=flat)</sub></sub>", priority, priority, color)
}

// InlineBody is the markdown of one inline comment.
func InlineBody(f review.Finding) string {
	return fmt.Sprintf("**%s  %s**\n\n%s\n\nUseful? React with 👍 / 👎.\n\n<!-- otto-review-finding:v1 -->",
		Badge(f.Priority), f.Title, f.Body)
}

// Comment is one inline comment in the GitHub "create review" payload.
type Comment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// Review is the GitHub "create review" payload.
type Review struct {
	CommitID string    `json:"commit_id"`
	Body     string    `json:"body"`
	Event    string    `json:"event"`
	Comments []Comment `json:"comments"`
}

// Payload builds the review posted to GitHub. The event is always COMMENT so
// it works on the user's own pull requests too.
func Payload(commitSHA string, p review.Placement, r review.Result, m Meta) Review {
	out := Review{
		CommitID: commitSHA,
		Body:     redact.Secrets(Summary(commitSHA, p, r, m)),
		Event:    "COMMENT", // never APPROVE or REQUEST_CHANGES: a pull request cannot talk Otto into a verdict that gates a merge
		Comments: []Comment{},
	}
	for _, f := range p.Inline {
		c := Comment{Path: f.Path, Line: f.End, Side: "RIGHT", Body: redact.Secrets(InlineBody(f))}
		if f.Start < f.End {
			c.StartLine, c.StartSide = f.Start, "RIGHT"
		}
		out.Comments = append(out.Comments, c)
	}
	return out
}

// Summary is the top-level review body.
func Summary(commitSHA string, p review.Placement, r review.Result, m Meta) string {
	trigger := m.Trigger
	if trigger == "" {
		trigger = "/otto review"
	}
	var b strings.Builder
	b.WriteString("<!-- otto-review-receipt:v1 -->\n\n")

	if len(p.Inline)+len(p.Outside) == 0 {
		b.WriteString("Otto Review: Didn't find any major issues. Bravo.\n\n")
	} else {
		b.WriteString("### 💡 Otto Review\n\nHere are some automated review suggestions for this pull request.\n\n")
	}

	fmt.Fprintf(&b, "**Reviewed commit:** `%s`\n", short(commitSHA))
	if len(m.Unreviewed) > 0 {
		fmt.Fprintf(&b, "\n> ⚠️ **Partial review.** No engine could review %d file(s): %s\n", len(m.Unreviewed), strings.Join(m.Unreviewed, ", "))
	}
	b.WriteString("\n<details> <summary>ℹ️ About Otto in GitHub</summary>\n<br/>\n\n")
	fmt.Fprintf(&b, "**Engine:** %s\n", engineLabel(m))
	if m.Size != "" {
		fmt.Fprintf(&b, "**Size:** %s\n", m.Size)
	}
	if m.Parts > 1 {
		pass := "plus a pass across the parts"
		if m.IntegrationSkipped {
			pass = "the pass across the parts did not complete"
		}
		fmt.Fprintf(&b, "**Reviewed in:** %d parts in parallel, %s\n", m.Parts, pass)
	}
	if m.Requester != "" {
		fmt.Fprintf(&b, "**Requested by:** @%s\n", m.Requester)
	}
	verdict := "patch is correct"
	if !r.Correct {
		verdict = "patch is incorrect"
	}
	fmt.Fprintf(&b, "**Verdict:** %s", verdict)
	if r.Explanation != "" {
		fmt.Fprintf(&b, ". %s", r.Explanation)
	}
	b.WriteString("\n")

	if len(r.Verification) > 0 {
		b.WriteString("\n#### Verification\n\n")
		for _, c := range r.Verification {
			fmt.Fprintf(&b, "- %s `%s`%s\n", checkIcon[c.Result], c.Command, note(c.Note))
		}
	}

	if len(r.Spec) > 0 {
		b.WriteString("\n#### Spec\n\n")
		for _, c := range r.Spec {
			fmt.Fprintf(&b, "- %s %s (%s)%s\n", specIcon[c.Status], c.Requirement, c.Status, note(c.Note))
		}
	}

	if len(p.Outside) > 0 {
		b.WriteString("\n#### Findings outside the diff\n\n")
		for _, f := range p.Outside {
			fmt.Fprintf(&b, "- %s **%s** (`%s:%d`)\n  %s\n", Badge(f.Priority), f.Title, f.Path, f.Start, f.Body)
		}
	}

	fmt.Fprintf(&b, "\nOtto is an open-source reviewer that runs on the author's own machine, using the AI access on their account. "+
		"Comment `%s` on a pull request to request a review.\n\n</details>\n", trigger)
	return b.String()
}

// QuotaFailure renders a provider quota failure for a manual review request.
func QuotaFailure(err error) string {
	var quota *engine.QuotaError
	_ = errors.As(err, &quota)
	details := "The provider did not include a reset time. Check your account's usage page for when requests become available again."
	if quota != nil && quota.Details != "" {
		details = strings.TrimSpace(quota.Details)
	}
	return fmt.Sprintf("### 💡 Otto Review\n\n"+
		"Otto stopped the review because an AI usage limit was reached. Any reset information reported by the provider is below; Otto does not guess a reset time.\n\n"+
		"<details> <summary>Engine status</summary>\n<br/>\n\n```\n%s\n```\n\n</details>\n", details)
}

var failureWords = map[bool]struct {
	title, intro, unexpected, reset string
	todo, logNote, retry            string
	kinds                           map[engine.FailureKind]string
	hints                           map[engine.FailureKind]string
	genericHint                     string
}{
	true: {
		title:      "### Otto não conseguiu concluir este review",
		intro:      "Nenhum dos motores configurados conseguiu analisar o pull request:",
		unexpected: "Ocorreu um erro inesperado antes ou durante a análise.",
		reset:      " (informado pelo provedor: %s)",
		todo:       "**O que fazer:**",
		logNote:    "Os detalhes técnicos ficam no log do Otto, na máquina onde ele roda.",
		retry:      "Depois comente `%s` para tentar de novo.",
		kinds: map[engine.FailureKind]string{
			engine.FailQuota: "limite de uso atingido", engine.FailAuth: "não está autenticado",
			engine.FailMissing: "não está instalado ou não foi encontrado", engine.FailBusy: "o provedor está sobrecarregado ou o motor tentou de novo várias vezes sem sucesso",
			engine.FailPermission: "não teve permissão para ler um arquivo de que precisava", engine.FailNetwork: "falha de conexão",
			engine.FailTimeout: "demorou demais e foi interrompido", engine.FailInvalid: "respondeu em um formato que o Otto não conseguiu usar",
			engine.FailOther: "falhou por um motivo inesperado",
		},
		hints: map[engine.FailureKind]string{
			engine.FailQuota:   "Espere o limite renovar ou adicione outro motor com `otto setup`.",
			engine.FailAuth:    "Entre na conta do motor (rode o comando dele no terminal) ou escolha outro com `otto setup`.",
			engine.FailMissing: "Instale o motor ou tire-o da lista com `otto setup`.",
			engine.FailBusy:    "Isso costuma ser passageiro: tente de novo em alguns minutos.",
			engine.FailNetwork: "Isso costuma ser passageiro: tente de novo em alguns minutos.",
			engine.FailTimeout: "Isso costuma ser passageiro: tente de novo em alguns minutos.",
			engine.FailInvalid: "O modelo não seguiu o formato pedido, o que é mais comum em modelos pequenos ou gratuitos. Tente de novo ou escolha um modelo mais forte com `otto setup`.",
		},
		genericHint: "Veja o motivo no log do Otto e, se repetir, escolha outro motor ou modelo com `otto setup`.",
	},
	false: {
		title:      "### Otto could not finish this review",
		intro:      "None of the configured engines could analyze the pull request:",
		unexpected: "An unexpected error happened before or during the analysis.",
		reset:      " (reported by the provider: %s)",
		todo:       "**What to do:**",
		logNote:    "Technical details are in Otto's log, on the machine where it runs.",
		retry:      "Then comment `%s` to try again.",
		kinds: map[engine.FailureKind]string{
			engine.FailQuota: "usage limit reached", engine.FailAuth: "not signed in",
			engine.FailMissing: "not installed or not found", engine.FailBusy: "the provider is overloaded or the engine kept retrying without success",
			engine.FailPermission: "was not allowed to read a file it needed", engine.FailNetwork: "connection problem",
			engine.FailTimeout: "took too long and was stopped", engine.FailInvalid: "answered in a format Otto could not use",
			engine.FailOther: "failed for an unexpected reason",
		},
		hints: map[engine.FailureKind]string{
			engine.FailQuota:   "Wait for the limit to reset or add another engine with `otto setup`.",
			engine.FailAuth:    "Sign in to the engine (run its command in a terminal) or pick another with `otto setup`.",
			engine.FailMissing: "Install the engine or remove it from the list with `otto setup`.",
			engine.FailBusy:    "This is usually temporary: try again in a few minutes.",
			engine.FailNetwork: "This is usually temporary: try again in a few minutes.",
			engine.FailTimeout: "This is usually temporary: try again in a few minutes.",
			engine.FailInvalid: "The model did not follow the requested format, which is more common with small or free models. Try again or pick a stronger model with `otto setup`.",
		},
		genericHint: "See the reason in Otto's log and, if it repeats, pick another engine or model with `otto setup`.",
	},
}

// Failure explains why a review could not finish. It names each engine and the kind of problem,
// says what to do about each kind, and never repeats raw engine output, which can contain code
// from the diff. The detail stays in Otto's own log.
func Failure(failures []engine.EngineFailure, trigger, language string) string {
	words := failureWords[strings.Contains(strings.ToLower(language), "portuguese")]
	var b strings.Builder
	b.WriteString(words.title + "\n\n")
	var hints []string
	addHint := func(hint string) {
		if !slices.Contains(hints, hint) {
			hints = append(hints, hint)
		}
	}
	if len(failures) == 0 {
		b.WriteString(words.unexpected + "\n\n")
		addHint(words.genericHint)
	} else {
		b.WriteString(words.intro + "\n\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "- **%s**: %s", f.Engine, words.kinds[f.Kind])
			if f.ResetAt != "" {
				fmt.Fprintf(&b, words.reset, f.ResetAt)
			}
			b.WriteString("\n")
			if hint, ok := words.hints[f.Kind]; ok {
				addHint(hint)
			} else {
				addHint(words.genericHint)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s %s\n\n%s\n\n", words.todo, strings.Join(hints, " "), words.logNote)
	fmt.Fprintf(&b, words.retry, trigger)
	return b.String()
}

func engineLabel(m Meta) string {
	label := fmt.Sprintf("`%s`", m.Engine)
	if m.Model != "" {
		label += fmt.Sprintf(" · model `%s`", m.Model)
	}
	if m.Effort != "" {
		label += fmt.Sprintf(" · effort `%s`", m.Effort)
	}
	return label
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

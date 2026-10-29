package engine

import (
	"context"
	"fmt"
	"strings"
)

// maxRepairs is how many times Otto asks the same engine to fix an answer it could not use before
// the next engine is tried. Engines that finish with a plan, a question or a broken JSON object are
// common enough that giving up on the first bad answer would throw a good review away.
const maxRepairs = 2

// maxPrevious is how much of the rejected answer is sent back to be reformatted.
const maxPrevious = 24_000

const nudge = "Reply now with ONLY the final review as the single JSON object described in the instructions. " +
	"Do not ask for confirmation, do not add a plan or any other text, and do not use tools."

func reasonOf(err error) string {
	r := firstLine(err.Error())
	if len(r) > 300 {
		r = r[:300] + "…"
	}
	return r
}

func repairPrompt(contract, previous, reason string) string {
	if len(previous) > maxPrevious {
		previous = previous[:maxPrevious] + "\n…(cut)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your previous answer to a code review request could not be used: %s.\n\n", reason)
	b.WriteString("Reply with ONLY the final review as the single JSON object described below: no prose, no markdown fences, " +
		"no questions, no tools. Keep every finding from your previous answer and do not invent new ones. " +
		"If it had no findings, return an empty findings list with your verdict.\n\n")
	b.WriteString("# Required JSON object\n\n" + contract + "\n\n# Your previous answer\n\n" + previous + "\n")
	return b.String()
}

// repairAnswer asks an engine whose answer was rejected to correct it, instead of failing the review.
//
//   - An engine that can safely continue a pinned session is told why the answer was rejected and
//     asked again there, up to maxRepairs times.
//   - Other engines are first given their rejected answer to reformat into the required JSON
//     (cheap: no code exploration), and if that is rejected too, the original request is run once
//     more with an instruction to answer only with the JSON.
//
// When every correction fails the original rejection is returned and the pool tries the next engine.
func repairAnswer(ctx context.Context, e Engine, req Request, previous string, rejected error,
	accept func(string) error, logf func(string, ...any), label string) (string, error) {
	if isQuotaMessage(previous) {
		return "", rejected // the engine is out of quota: asking again only wastes time, let the next engine take over
	}
	reason := reasonOf(rejected)
	try := func(r Request, how string) (string, bool) {
		if ctx.Err() != nil {
			return "", false
		}
		logf("%s: answer rejected (%s); %s", label, reason, how)
		text, err := e.Run(ctx, r)
		if err != nil {
			logf("%s: the correction failed: %s", label, reasonOf(err))
			return "", false
		}
		if err := accept(text); err != nil {
			reason = reasonOf(err)
			previous = text
			return "", false
		}
		return text, true
	}

	if c, ok := e.(Continuer); ok && c.CanContinue() {
		for i := 0; i < maxRepairs; i++ {
			r := req
			r.Followup = nudge + " Your last answer was rejected: " + reason
			if text, ok := try(r, fmt.Sprintf("asking again in the same session (%d/%d)", i+1, maxRepairs)); ok {
				return text, nil
			}
		}
		return "", rejected
	}

	if strings.TrimSpace(previous) != "" && strings.TrimSpace(req.Contract) != "" {
		r := req
		r.Prompt, r.Followup = repairPrompt(req.Contract, previous, reason), ""
		if text, ok := try(r, "asking it to reformat its answer"); ok {
			return text, nil
		}
	}
	r := req
	r.Prompt = req.Prompt + "\n\n" + nudge + " Your last answer was rejected: " + reason + "\n"
	if text, ok := try(r, "asking again with the original request"); ok {
		return text, nil
	}
	return "", rejected
}

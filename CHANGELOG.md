# Changelog

## v0.2.2

- A review that fails for a temporary GitHub reason (rate limit, timeout, outage) is retried silently, after 5, 10, 20 and then every 30 minutes, instead of posting a failure comment. It is reported only if it still fails after the lookback window (24 hours by default). Engine failures are reported as before.
- A successful review removes the failure comments and 😕 reactions that earlier attempts left on the pull request.
- `otto queue` lists the requests Otto has seen but not finished, with retry times and the last failure.

## v0.2.1

- Watching all repositories no longer spends the GitHub API budget: repositories without a push in the last 7 days are polled every 10 minutes instead of every cycle, and learning reads only pull requests updated since its last sweep.
- When GitHub reports the rate limit, polling stops for the cycle and learning pauses for 15 minutes instead of failing on every repository.

## v0.2.0

- Reviews are read-only and use CI checks at the reviewed commit as execution evidence. Running checks may be awaited; failed checks include redacted log excerpts.
- Review effort and model selection follow the size and risk of each pull request. Large changes are split into parallel parts and checked again for interactions across parts.
- Otto traces requested behavior through the code and points out new behavior without relevant regression coverage.
- Review triggers accept common Markdown formatting and a request on following lines.
- The setup wizard offers free OpenCode models selected from a planted-bug evaluation. Run `otto eval models` to repeat the evaluation or compare other models.
- Pull request text is marked as untrusted, model tools are restricted to read-only operation, published text is redacted, and learning accepts feedback only from allowed maintainers.
- Otto learns from maintainer replies even without reactions, repairs malformed model answers, and tries the next configured engine when an attempt fails.

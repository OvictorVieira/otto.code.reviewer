# Changelog

## v0.2.0

- Reviews are read-only and use CI checks at the reviewed commit as execution evidence. Running checks may be awaited; failed checks include redacted log excerpts.
- Review effort and model selection follow the size and risk of each pull request. Large changes are split into parallel parts and checked again for interactions across parts.
- Otto traces requested behavior through the code and points out new behavior without relevant regression coverage.
- Review triggers accept common Markdown formatting and a request on following lines.
- The setup wizard offers free OpenCode models selected from a planted-bug evaluation. Run `otto eval models` to repeat the evaluation or compare other models.
- Pull request text is marked as untrusted, model tools are restricted to read-only operation, published text is redacted, and learning accepts feedback only from allowed maintainers.
- Otto learns from maintainer replies even without reactions, repairs malformed model answers, and tries the next configured engine when an attempt fails.

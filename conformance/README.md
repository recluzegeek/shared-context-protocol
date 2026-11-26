# SCP conformance vectors

Language-neutral test data for the parts of the specification every implementation must
reproduce exactly. They decide what "correct" means; the reference server in `../server` is just
the first implementation that passes them.

| File | Spec | Checks |
| --- | --- | --- |
| `vectors/overlap.json` | §6 | Every scope kind's matching rule, every severity cell, the three relations, the broad and topic caps, rename targets, same-member sessions |
| `vectors/scopes.json` | §6.2, §6.2.1 | Scope normalization and which scopes are broad |
| `vectors/canonical.json` | §4.7.1 | Git remote canonicalization: ports, case-insensitive hosts, credentials stripped |
| `vectors/search.json` | §5.4.3 | The tokenizer and the minimum prefix match |
| `vectors/secrets.json` | §9.1, Appendix B | The minimum secret detectors, including documentation that must pass |

Each file describes its own format in a `description` field. To use them, load each file in your
implementation's tests and run every case; the Go runner is
`server/internal/domain/conformance_test.go`.

Not covered yet ([spec §20](../spec/specification.md#20-open-issues)): a wire-level suite that
runs against any server URL (operations, errors, wrapping, isolation), and client tests for the
`trust` and `overlap-presentation` profiles.

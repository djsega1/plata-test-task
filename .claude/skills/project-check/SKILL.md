---
name: project-check
description: Reviews the current diff (or, with --full, the whole repository) against docs/design.md and CLAUDE.md, test coverage against the design doc's own done-criteria, and comment quality — delegates correctness and simplification findings to code-review and simplify instead of re-deriving them. Pass --fix to auto-apply the mechanical findings.
---

# Project compliance check

This skill does not hunt for bugs or simplification opportunities itself — `code-review` and
`simplify` already do that well, and duplicating that logic here would just be two places to keep in
sync. This skill's job is everything specific to *this* project that those two don't know about:
does the code match `docs/design.md`, does it follow `CLAUDE.md`'s structural rules, do the tests
actually cover what the design doc says "done" means for the step being touched, and is every new or
changed comment pulling its weight in the style this codebase has already committed to.

Neither `docs/design.md` nor `CLAUDE.md` is treated as fixed in shape below — both evolve, sections
get added, renumbered or renamed, rules get amended. Every step that reads one of them re-reads it
fresh and finds what it needs by role and content, never by a section number or heading text memorized
here. Nothing in this skill's own checklists is authoritative over what the documents actually say
today; the checklists are a starting point for what to look for, not a substitute for reading them.

## 1. Scope

Look at the arguments this skill was invoked with:

- **`--full`** → whole-repo mode. The target is every tracked file under `internal/`, `cmd/`, `pkg/`,
  plus `docs/design.md` and `CLAUDE.md` themselves — not just what changed.
- **no `--full`** → diff mode (the default). The target is the current diff against the repo's
  default branch (same notion of "current diff" `code-review` uses with no target — check what's
  changed relative to `main`/`origin/main`, working tree included).
- **`--fix`** may appear alongside either of the above. Strip it before reading the rest; keep it in
  mind for step 9.
- Any other argument: ask the user what it means rather than guessing — don't silently fold an
  unrecognized flag into either mode.

State once, up front, which mode you're running in and what the target is, so the report is legible
without re-deriving it.

## 2. Mechanical gate

Run `task check` (falls back to `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`,
`go test ./...` run individually if `task` isn't on PATH — check the project's documented commands
for the current exact invocations, including any build tags, rather than assuming these). If anything
fails, record it as a finding — category `build-gate` — but still run every step below anyway, so one
report covers everything instead of stopping early.

## 3. Delegate correctness and simplification

Invoke the `code-review` skill over the same target (`medium` effort for a normal diff, `high` if the
target is large or whole-repo mode). Invoke the `simplify` skill the same way. Take their findings
as-is — don't re-review for the same things yourself, don't second-guess their verdicts — and carry
them into the merged report in step 8 under their own categories.

## 4. Design-doc conformance

Read the whole design doc — good ones stay short on purpose, this is cheap. Find its sections by what
they're *for*, not by a number: it will have something that states assumptions/scope, something that
explains upstream/external dependency choices, something that lays out the architecture, something
that specifies the API or interface contract, something that specifies the data model or schema,
something that tracks the implementation plan and what's still open, and — if the project is being
built incrementally with a human reviewer — likely something that honestly states what has and hasn't
actually been verified (live systems exercised vs. only faked/mocked).

For every behavior the target touches (contract shapes, schema, architecture decisions, config
defaults and env var names, timing numbers, error codes, external-dependency choices):

- cross-check it against what the design doc documents;
- flag disagreement in **either direction** — code silently doing something the doc no longer
  describes is exactly as much a finding as code violating a documented decision (this project's own
  stated rule: a stale design doc is worse than none);
- if the doc tracks implementation status per step/feature, check it's accurate for every step the
  target touches — "done" only where that entry's own stated done-criterion is actually satisfied;
- if the doc has a verification-limitations section, check it still honestly represents what has and
  hasn't been run against a live dependency, given what the target actually does.

Category: `design-doc-drift`.

## 5. Project-conventions conformance

The project's own instructions file (loaded as project instructions already, but re-derive this
checklist by reading it fresh rather than from memory of it) states structural rules beyond generic Go
style. Re-read it and check the target against every rule that could plausibly apply — the list below
is this skill's own memory aid for what kind of thing to look for, drawn from what the rules currently
say, not a ceiling on what to check or a guarantee any of these still apply verbatim:

- are ports (interfaces over external systems) declared in the package that consumes them, not the
  package that only holds domain types;
- does the domain package stay free of imports from adapters or use-case code;
- does a database migration avoid encoding domain vocabulary or value ranges in a `CHECK` — only
  structural/concurrency constraints;
- do generated IDs use the ID scheme the project has standardized on, not some other one a library
  defaults to;
- is money handled in a precision-safe type, never a plain float, all the way out to the API;
- does an adapter normalise upstream failures into the project's own classified error type, never let
  a bare untyped error escape it;
- do tests avoid the sleep/wait patterns the project's own lint config forbids — a clean lint pass
  from step 2 already gates this, don't re-derive it by hand;
- do integration tests stay behind whatever build tag/opt-in mechanism the project uses, skipping
  cleanly without the external resource they need;
- does a new dependency in the module file come with a stated reason, not just a silent version bump;
- do comments and identifiers follow the project's stated language convention (don't flag non-code
  prose files against a rule that's scoped to source code, if that's how the rule is actually written).

Category: `convention-violation`.

## 6. Tests against the design doc's own done-criteria

Find wherever the design doc states what "done" means per implementation step or feature (however
that's organized right now). Identify which entry/entries the target belongs to, by the files it
touches. For each: read its stated done-criterion and check the test suite scenario by scenario — not
"tests exist somewhere near this," but every concrete behavior that entry names has a test actually
asserting it. Flag any named scenario with nothing covering it.

Category: `test-coverage-gap`.

## 7. Comment quality

Only new or changed comments in diff mode; every comment in whole-repo mode. Hold each one to the
standard this codebase already set for itself — read a handful of the more elaborate existing
comments in the packages the target touches (or, if unsure where, in the core business-logic package)
to calibrate the house voice before judging: if long, reasoned WHY-comments are already the norm here,
length and thoroughness are not the problem. Flag instead:

- text in a comment that isn't in the language the project's conventions call for source-code
  comments to be in;
- a comment that only restates the adjacent identifier or code (WHAT, not WHY) and could be deleted
  without a careful reader losing anything;
- a comment naming the current task/PR/issue instead of the durable reasoning behind the code;
- a comment describing behavior the same target changed elsewhere, now stale or contradicting the
  code next to it.

Category: `comment-quality`.

## 8. Report

Merge every finding from steps 3–7 (plus the build-gate finding from step 2 if it failed) into one
`ReportFindings` call, most severe first. No separate prose report alongside it.

## 9. `--fix`

Only if `--fix` was passed. Apply fixes for what's safely mechanical: a design-doc line that's
unambiguously stale once you know the actual current state, a comment rewritten into the required
language, deleting a WHAT-not-WHY comment. Leave everything else — missing tests, structural
`convention-violation` findings, any `design-doc-drift` where it isn't obvious whether the code or the
doc is the one that should change — as report-only even under `--fix`: those need a human call on
which side is right. Re-report with `outcome` set per `ReportFindings`' contract.

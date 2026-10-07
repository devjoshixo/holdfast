# Holdfast — agent brief

**Read `../AGENTS.md` first** (also mirrored at `../Goal/AGENTS.md`). It carries the working rules;
this file is only what is specific to Holdfast.

## What this is
A Go + PostgreSQL seat-booking engine whose single claim is: **concurrent requests can never
double-sell a seat, and never lie about the outcome.** Every claim is backed by a test that would
fail if it were false.

## Read before touching anything
**`docs/BUILD-GUIDE.md`** — how to help on a hand-typed project, what to teach before M4/M5/M6, and
the traps. Read section 0 first.

**`docs/holdfast-reference.html`** — the field manual. 19 sections, every file documented: the four
guarantees, all three concurrency mechanisms with what a *losing* request costs in each, the harness
design including the three times it measured the wrong thing, the measured crossover, and the
claim-first idempotency pattern. Open it in a browser.

Then `PLAN.md` for the milestone spec.

## State
**PAUSED at M4** until `interview-prep-kit/docs/v2-results.md` publishes or **1 Dec 2026**, whichever
comes first. It resumes at **three slots a week, never one** — one slot a week is the format mismatch
that stalled Raft, because a short session goes entirely to reloading context.

Re-entry point: `internal/booking/hold.go:115`, step 2 of `HoldSeat`. His own design comments sit
directly above the `panic("todo")` and **are** the plan. Leave them there.

## Do not
- **Do not answer the open prediction** in field-manual §14 (what the seat-taken write stores on the
  success path). It is deliberately unanswered and is the first thing he does on re-entry.
- Do not propose resuming early.
- Do not reconstruct the design from memory — read the manual.

## Still owed
Retire `BookSeatCAS` or keep it alongside the hold path — **decide before writing `ConfirmSeat`**,
not during. And a flagged-but-unverified finding: the shared harness's retry assertion may not hold
for `BookSeatRowLock`, which never inspects `request_id`. Run it before quoting it.

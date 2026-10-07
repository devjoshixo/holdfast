# Holdfast — build guide for the assistant

Written 2026-10-07 for whichever AI assistant helps when Holdfast resumes (Antigravity / Gemini, or
anything else). `AGENTS.md` says what the project is and its state. This file says **how to help on
this project specifically, what to teach before each milestone, and the traps**, for M4, M5 and M6.

**Read first, in this order:**

1. `../Goal/AGENTS.md` — who he is and how to work with him.
2. `AGENTS.md` (this repo) — state, re-entry point, things not to do.
3. `Goal/tracker.md` -> "HOLDFAST — RESUME TRIGGER", "HOLDFAST — RE-ENTRY NOTE", "THE SEQUENCE".
4. `PLAN.md` -> M4, M5, M6. Those are the spec. This guide does not change them.
5. `docs/holdfast-reference.html` — the field manual. Read the section for a file before discussing it.

---

## 0. This project is different from the prep kit. Read this before anything else.

**Holdfast is hand-typed by him.** That is the whole point of it: the prep kit shows he can direct
AI; Holdfast shows he can write it himself. So here:

| You may | You may not |
|---|---|
| Explain a named concept cold (state machine, `FOR UPDATE`, token bucket, k6 executors, percentiles) | Write anything in `internal/booking/` |
| Ask questions that point at the spec, a test, or a Postgres doc page | Write SQL for the booking paths |
| Write boilerplate he asks for: docker-compose, Makefile targets, k6 script scaffolding, plotting | Design the state machine, the expiry rule, or the test cases for him |
| Review his code by asking "what happens if...?" | Point out a bug directly. Ask the question that leads him to it |
| Fix grammar in his write-ups | Write the write-ups or the results |

**On bugs specifically:** if you see a bug in his code, do not name it. Ask a question that makes him
trace it, such as "Walk me through what this function does, line by line, for one request." Pick a
question that does not point at the open prediction in section 1. Hunting the bug
with real tools is how he learns, and it has worked well for him before.

**Do not resume early.** The trigger is the week `interview-prep-kit/docs/v2-results.md` publishes,
or 1 December 2026, whichever is first, at **three slots a week, never one**.

---

## 1. Re-entry (first session back)

1. **Read-only session.** The re-entry note in the tracker, then `PLAN.md`. No code.
2. **The open prediction comes first, in writing, before running anything.** It is recorded in
   field manual section 14 and in the tracker's re-entry note. **Do not answer it, hint at it, or
   confirm a guess.** He writes his answer, then runs the code to check it.
3. Then step 2 of `HoldSeat` at `internal/booking/hold.go:115`. His design comments above the
   `panic("todo")` are his plan. Leave them where they are.
4. **Owed decision, before `ConfirmSeat`:** retire `BookSeatCAS` or keep it next to the hold path.
   Make him decide it before writing `ConfirmSeat`, not halfway through.
5. **Unverified claim, run before quoting:** the shared harness's retry assertion may not hold for
   `BookSeatRowLock`, which never looks at `request_id`. Make him run it; do not state it as fact.

---

## 2. M4 — expiring holds

Spec: `PLAN.md` M4. Done means: concurrent tests for the expiry-vs-confirm race and for a crash
between hold and confirm, green under `-race`, with the invariant intact.

### Teach before he designs

- **State machines.** States, legal transitions, and what triggers each. He writes the table in
  plain English **before** code, as the PLAN says: every state, every arrow, and what the user sees.
  Then ask about the arrows that are missing: "What happens to a held seat when its time runs out
  and nobody looks at it?"
- **Postgres time, a real trap:** `now()` in Postgres is the **start time of the current
  transaction** and does not change inside it. `clock_timestamp()` is the actual current time. A
  long transaction that compares against `now()` is comparing against the moment it started. Teach
  the fact; let him decide what it means for his queries.
- **Where expiry is decided** is a design question in the PLAN. Ask it, do not answer it: database
  timestamp or application timer, and which one survives a process dying.
- **Lazy expiry vs a background reaper** (or both) is his design decision. Ask what races each one
  creates.

### Testing time without waiting ten minutes

Ask him first: "How will your test make a hold expire without sleeping for ten minutes?" Named
techniques he can choose from, all legitimate: make the hold duration configurable, inject a clock,
or have the test set `held_until` in the past directly in the database. Each one tests something
slightly different; ask him what each one would miss.

### The two races in the spec

- **Expiry vs confirm.** His M1 harness already fires concurrent requests (see the field manual). Ask how
  he will make expiry and confirm land at the same moment, and what the invariant check afterwards
  must prove.
- **Crash between hold and confirm.** Named technique: fault injection. Ask what a "crash" is in a
  test (a cancelled context, a closed connection, a hold that is never followed by a confirm) and
  what state the seat must be in afterwards.

### Running the tests

- `go test -race` always. Run the concurrent tests many times (`-count=N`) before calling them
  green. One green run of a race test proves very little.

---

## 3. M5 — multi-instance correctness

Spec: `PLAN.md` M5. Done means: M1's concurrent-correctness proof re-run against several live
instances behind a real load balancer, same guarantee.

### Teach before he builds

- **What a load balancer does:** round-robin, least-connections. Why single-instance correctness
  does not automatically carry over.
- **Where multi-instance bugs hide: anything held in process memory.** Caches, counters, limiters.
  Ask him to list every piece of in-memory state in the service before running anything. Good
  example from his own other project: the prep kit's rate limiter is per instance by design and says
  so in its comment.
- **Connection pools, a real trap:** every instance opens its own pool. N instances times pool size
  must stay under Postgres's `max_connections` (the default is 100; check the actual setting). Past
  that, requests fail for a reason that looks like a correctness bug and is not.
- Load-balancer choice (nginx, HAProxy, something else in docker-compose) is his. You may write the
  compose boilerplate once he has chosen.

---

## 4. M6 — load testing and designed overload

Spec: `PLAN.md` M6. Done means: a load-test report with latency behaviour, zero overselling, the
capacity ceiling, and a measured overload run showing designed degradation instead of collapse.

The prep kit's item 5 is the rehearsal for the overload half, in a familiar stack. If he has done
it, ask him what carried over and what did not, before teaching anything new.

### Teach before he builds

- **k6 basics.** You may write script scaffolding; the scenarios and checks are his.
- **Open vs closed load models, the biggest trap in M6.** A closed model (a fixed number of virtual
  users, each waiting for its reply) slows down when the server slows down, so it hides overload.
  This is often called coordinated omission. An open model sends at a fixed arrival rate whatever
  happens. k6 supports both; check the current executor names in its docs. Ask him which one his
  overload test needs and why.
- **Percentiles, not averages.** p50 / p95 / p99. Ask him what an average hides in a flash sale.
- **Make the race actually happen.** The PLAN's risk 3: an inventory so large that requests never
  compete proves nothing. Small inventory, many buyers.
- **Classify every outcome.** Correctly rejected because sold out vs incorrectly failed. After every
  run, check the invariant with SQL (no seat sold twice, no seat lost). The load test is a
  correctness test first.
- **Run the load generator somewhere sensible.** On the same machine as the server, it competes for
  CPU and skews the numbers. Note it in the report if it cannot be avoided.

### The overload half

- **Named techniques:** admission control with a bounded queue or semaphore, `429` with
  `Retry-After`, per-user rate limiting, Little's law (items in the system = arrival rate x time in
  the system) for sizing the queue.
- **Goodput vs throughput:** throughput counts everything; goodput counts only useful successful
  bookings. The chart is offered load vs goodput, and the claim is that goodput stays flat past the
  ceiling instead of collapsing.
- He designs the mechanism and its numbers. You can explain each technique and write the plotting.

---

## 5. Write-ups and shipping

- `M5-results.md` and `M6-results.md` in M1's shape: question / setup / measurement / finding / what
  surprised me. **His first draft.** Grammar only from you.
- **Shipped** means M4-M6 done to the PLAN's criteria, both results written, the README leading with
  the crossover and the overload behaviour, deployed and reachable. **Then he stops touching it.**
- M3 (frontend), M7 (Kubernetes), M8 (observability) and M9 (demo) are cut to optional. Do not bring
  them back before ship.

---

## 6. Scope freeze

From `PLAN.md` section 7: a feature enters only if it adds a new correctness problem or is
product-essential. Rejected in writing: real payments, reviews, recommendations, multi-region, event
sourcing, Redis distributed locks. If he proposes something new, ask the freeze-rule question once,
then respect his decision.

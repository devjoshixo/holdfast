# Holdfast — Correct-Under-Contention Booking Platform

> Codename **Holdfast** — placeholder, rename when you `git init` the real repo.
> "Holdfast" = a reservation that holds firm even when thousands of people grab for it at once.

**One-liner:** A real booking/ticketing product (browse → see live availability → book → manage
bookings) where the core reservation logic is *provably* correct under extreme concurrent demand —
a "flash sale" scenario, zero overselling, zero incorrect rejections — deployed as a proper
multi-replica, health-checked, load-balanced, observable system on Kubernetes.

**Status:** Plan / not started. Time budget: open-ended, re-baselined on real progress — not
compressed to fit a fixed window. Learn each piece properly; do not brush past anything.

**Scope re-evaluation (2026-07-17), decided deliberately per tracker guardrails:** evaluated
switching to a more ambitious project (3-agent analysis: app-ideas repo survey, depth scoring,
devil's-advocate alternatives — Raft-productionized, mini-Temporal orchestrator, event-sourced
ledger). Verdict: stay, but raise the ceiling. Upgrades folded in below: (1) the M0 mechanism
choice becomes a measured 3-way benchmark, (2) new retry-safety milestone (exactly-once
client-visible booking — closes the "commit succeeded, response lost, client retries" gap),
(3) expiring holds as a two-phase booking state machine, (4) cancellations + waitlist
auto-promotion, (5) designed overload behavior + rate limiting in the load-test milestone.
**Scope is FROZEN here.** Any future "add X" must pass the rule: does it add a NEW correctness
problem or is it product-essential? Otherwise it gets parked. The genuine 9/10-complexity project
(productionized Raft) stays sequenced AFTER Holdfast, per the pivot plan — not inflated into it.

**Method note:** Same discipline as everything else in this project — design each milestone in
plain English BEFORE touching code. Quiz/checkpoint after each learning topic. This is a spec, not
a tutorial: the design decisions below are questions to answer yourself, not answers to copy.

---

## 1. Why this project, and why this shape

You had a real interview where a technical story (an N+1 query fix) got fair, useful pushback:
"I understand, but that's the obvious approach." That's the actual gap this project targets — not
"do you know full-stack," you clearly do (VoIP at 1M+ calls/day, 4 CRM integrations, live Shopify
apps). The gap is: can you point to a problem where there was no known pattern to reach for, and
you had to reason out the correct answer yourself, under real constraints.

This project was chosen over three alternatives (a durable job-orchestration engine, a distributed
rate limiter, a from-scratch collaborative editor) specifically because it's the one where the hard
distributed-systems-style problem is embedded IN a real, end-user-facing product, not wrapped around
an infrastructure tool. Recruiters and interviewers immediately understand "a booking platform that
doesn't oversell tickets during a flash sale" — they don't need a primer on what a workflow engine is
first.

**The authenticity bonus:** the core hard problem here — preventing double-booking of scarce slots
under concurrent requests — is the EXACT bug found live, in your real job's production code, during
the conversation that led to this project. The naive "check availability, then book" pattern in that
codebase has no unique constraint and no transaction/lock around the insert — a genuine, still-live
race condition. This project is you building the version that's actually correct, with a real
concurrent test proving it, not just claiming it.

---

## 2. The genuinely hard problem(s) — plural, on purpose

This project is deliberately scoped so multiple DISTINCT hard problems show up at different layers,
not one problem repeated:

1. **Single-instance correctness under concurrency.** Two requests for the same slot at nearly the
   same instant — what actually prevents both from succeeding? (Not "add a check" — the check alone
   doesn't close the race. See M0/M1.)
2. **Multi-instance correctness.** Does your mechanism still hold when there are SEVERAL replicas of
   your booking service running behind a load balancer, all racing for the same shrinking inventory?
   Single-instance correctness does not automatically imply this — it must be reasoned through and
   tested separately (M3).
3. **Correctness under real load, not a unit test.** Does the guarantee survive an actual flash-sale
   burst — thousands of concurrent HTTP requests, real network/latency conditions — not just N
   goroutines in a clean test (M6).
4. **Correctness under failure.** Does a pod dying mid-request lose, duplicate, or correctly handle
   an in-flight booking attempt? (M2, M7.)
5. **Correctness under retries and time (added 2026-07-17).** A lost response + client retry must
   yield exactly one booking (M2); a hold expiring at the same instant as its confirmation must
   resolve one way, never both (M4).

Each of these is a DIFFERENT reason the system could be wrong, even if the earlier ones are solid.
That's the actual shape of "genuinely hard" here — not one clever trick, but layered correctness
under increasingly realistic conditions.

---

## 3. Build vs use

**BUILD (your core):** the reservation/inventory engine and its concurrency-safety mechanism, the
booking API, the correctness test harness (concurrent + load-test), the frontend booking flow.

**USE (configure, don't reinvent):** Postgres for durable storage, a load-testing tool (k6 or
similar) rather than hand-rolling one unless you want the extra depth, Prometheus + Grafana for
metrics (standard, don't rebuild), Kubernetes itself (you're learning to use it well, not
reimplementing an orchestrator).

---

## 4. Milestone roadmap

Each milestone: Goal / Hard problem / What to learn (properly, not brushed off) / Design decisions
(open questions, answer in writing) / Definition of done / Watch-it-run.

### M0 — Pin the correctness contract (plain English, before any code)

- **Goal:** Decide, in writing, EXACTLY what "no overselling, no incorrect rejection" means for
  this system, and which concurrency-safety mechanism you'll use and why.
- **Hard problem:** there are multiple valid mechanisms (DB unique constraint + catch conflict,
  transaction + `SELECT ... FOR UPDATE` row locking, optimistic compare-and-swap like Lab 2's
  versioned Put) with real tradeoffs in throughput, simplicity, and how failure surfaces to the
  user. Picking one without understanding the tradeoffs is exactly the trap that caused the real bug
  you found.
- **What to learn:** database transaction isolation levels (READ COMMITTED vs REPEATABLE READ vs
  SERIALIZABLE — what each actually promises), what a unique constraint guarantees at the storage
  engine level, optimistic vs pessimistic concurrency control as a general concept.
- **Design decisions:** which mechanism, and why over the alternatives? What does the user see when
  they lose a race (immediate rejection vs a brief "confirming..." state)? Is retry automatic or
  surfaced to the user?
- **Done:** a written contract + your reasoning for the choice, defensible out loud. (2026-07-17
  addition: the contract must also cover retry semantics — what "exactly one booking" means when a
  response is lost and the client retries. See M2.)

### M1 — Core reservation engine (Go), single instance, PROVEN correct + mechanism shootout

- **Goal:** implement the reservation logic for one instance, with a concurrent test that PROVES
  correctness — not "it seems fine," an actual assertion. **(2026-07-17 upgrade):** implement ALL
  THREE M0 candidate mechanisms (unique constraint + conflict-catch, `SELECT FOR UPDATE`,
  optimistic CAS) behind one interface, and benchmark them under hot-row contention — publish
  throughput / latency / retry-rate comparisons. The M0 choice gets confirmed or overturned by
  YOUR OWN numbers, not a blog post.
- **Hard problem:** proving correctness requires real concurrent-test methodology, not eyeballing;
  benchmarking under contention requires a harness that doesn't lie (see Risk #3).
- **What to learn:** Go's `-race` detector (you've used this in labs — apply it here), writing a
  test that spawns many goroutines racing for a small pool of slots and asserts exactly the right
  number succeed; basic benchmark methodology (warm-up, variance, what to hold constant).
- **Design decisions:** how many concurrent goroutines / how small a pool makes the race actually
  likely to manifest in a test (too generous a pool and the race never triggers, giving false
  confidence)?
- **Done:** `-race`-clean, a test that reliably proves "exactly N slots booked, never N+1" across
  many runs — for all three mechanisms — plus a short published comparison (numbers + which one
  wins for this workload and why).

### M2 — Retry safety: exactly-once client-visible booking (added 2026-07-17)

- **Goal:** the guarantee survives ambiguous outcomes. A booking commits in the DB, the pod dies
  before the response leaves, the client retries — the user must end up with exactly ONE booking
  (and one charge, when the fake-payment step exists), never two, never zero-but-charged.
- **Hard problem:** this is Lab 2's ErrMaybe wearing production clothes — you cannot know whether a
  lost-reply attempt succeeded, so the system must be designed so retries are SAFE rather than
  guessed-about. Idempotency keys, request deduplication, and what happens when the original and
  the retry are in flight simultaneously.
- **What to learn:** idempotency-key patterns (key storage, memoized responses, in-flight duplicate
  handling), at-least-once vs at-most-once vs exactly-once terminology precisely.
- **Design decisions:** who generates the key (client vs server)? How long do keys live? What does
  the retry get back — the memoized original response, or a fresh computation?
- **Done:** the M1 harness extended to inject lost responses + client retries, proving exactly-once
  client-visible outcomes under concurrent retry storms.

### M3 — Full-stack product wrapper

- **Goal:** a real frontend — browse/search, live availability, a checkout flow, a "my bookings"
  view — talking to the engine through a real API. **(2026-07-17 additions):** cancellations +
  waitlist with auto-promotion (a freed seat must go to exactly one waitlisted person — the core
  race in a new costume), and a deliberately LEAN admin side (create event, set capacity,
  open/close sales — 2-3 sessions max, it exists so the demo feels like a product).
- **Hard problem:** less DS-hard, more product-hard — what does the UX do gracefully when someone
  loses a race for a slot? How does availability update live (WebSocket vs polling) without
  hammering the backend? Plus: waitlist promotion is itself a contended write — prove it with the
  same discipline.
- **What to learn:** WebSocket basics for live updates, enough frontend state management to handle
  "the thing I'm looking at just changed under me."
- **Done:** a real, click-through, demoable product — not a Postman collection. Keep it lean:
  frontend skill is already proven elsewhere; this milestone is the wrapper, not the point.

### M4 — Expiring holds: the two-phase booking state machine (added 2026-07-17)

- **Goal:** real ticketing UX — a seat is HELD for ~10 minutes while the buyer checks out, then
  auto-releases if not confirmed. Adds TIME as a failure dimension.
- **Hard problem:** a whole new family of races: hold expires at the exact moment the user clicks
  confirm; crash between hold and confirm; the background expiry reaper racing a confirmation;
  held-but-never-confirmed seats leaking inventory.
- **What to learn:** state-machine modeling (available → held → booked, with expiry transitions),
  background-job patterns and their races, where the expiry decision authoritatively lives (DB
  timestamp vs application timer — only one of these survives a pod death).
- **Design decisions:** exact states + legal transitions, written BEFORE code; what the user sees
  when their hold expires mid-checkout.
- **Done:** concurrent tests covering the expiry-vs-confirm race and crash-between-hold-and-confirm,
  green under `-race`, invariant intact (a held seat is never double-sold, an expired hold always
  returns to inventory).

### M5 — Multi-instance correctness (the deeper, separate hard problem)

- **Goal:** run several instances of the reservation service behind a load balancer, and EXPLICITLY
  re-test — do not assume — that the correctness guarantee still holds.
- **Hard problem:** if your M0/M1 mechanism relies purely on the database as shared source of truth,
  it likely extends correctly — but this must be TESTED, not assumed, especially if you added any
  in-memory optimization along the way (that's exactly where multi-instance bugs hide).
- **What to learn:** how a load balancer actually distributes requests, why single-instance-tested
  correctness doesn't automatically transfer to N instances.
- **Done:** the same concurrent-correctness proof from M1, re-run against multiple live instances
  through a real load balancer, same guarantee holds.

### M6 — Load testing: the flash-sale simulation + designed overload behavior

- **Goal:** simulate a real flash-sale burst — thousands of concurrent buyers, a small inventory
  pool — against the deployed system, and confirm zero overselling under REALISTIC load, not a
  unit test. **(2026-07-17 upgrade):** don't just DISCOVER the capacity ceiling — DESIGN what
  happens past it: admission control (bounded queue/semaphore), `429 + Retry-After`, per-user rate
  limiting (a flash sale is organic DDoS — abuse protection is thematic here, not polish), and
  measure goodput under overload: graceful degradation, not collapse.
- **Hard problem:** realistic load-generation methodology; distinguishing "correctly rejected
  because actually sold out" from "incorrectly failed"; understanding what metrics actually matter;
  backpressure design under contention.
- **What to learn:** a load-testing tool (k6 recommended — scriptable, realistic), how to read
  latency percentiles (p50/p95/p99), basic capacity-planning intuition (where does throughput
  actually cap out, and why), backpressure/admission-control patterns.
- **Done:** a load-test report — latency behavior, zero overselling, documented capacity ceiling,
  plus a measured overload run showing designed degradation (goodput held, clients told to back
  off) instead of collapse.

### M7 — Kubernetes deployment: health checks + load balancing for real

- **Goal:** containerize everything, deploy multiple replicas per service, real liveness/readiness
  probes, a Service/Ingress actually load-balancing across replicas.
- **Hard problem:** a GOOD health check verifies "can this pod actually serve a request right now"
  (e.g. DB connection healthy), not just "is the process alive." What happens to an in-flight request
  during a rolling deploy or a pod crash — lost, gracefully retried, or silently dropped?
- **What to learn:** Kubernetes fundamentals from scratch — pods, deployments, services, ingress,
  liveness vs readiness probes, rolling updates, basic resource requests/limits. This is a real new
  skill area for you (resume shows Docker/Nginx/DO, not K8s yet) — budget real learning time, don't
  rush it.
- **Done:** `kubectl delete pod` on a booking-service replica MID-load-test — system keeps serving
  correctly, health checks detect and route around the dead pod within a defined window.

### M8 — Observability

- **Goal:** Prometheus metrics (request rate, booking-conflict rate, latency histograms) + Grafana
  dashboards, so correctness and behavior under load are VISIBLE, not just inferred from logs.
- **Hard problem:** choosing metrics that tell a real story, not vanity numbers; instrumenting the Go
  service properly without adding overhead that skews the very latency you're measuring.
- **What to learn:** Prometheus client libraries for Go, basic PromQL, building a Grafana dashboard
  that a stranger could read in 10 seconds.
- **Done:** a live dashboard, watchable during the M6 load test, showing request rate/latency/
  conflict-rate in real time.

### M9 — The proof demo (the centerpiece)

- **Goal:** the flash-sale load test, running against the full K8s-deployed, multi-replica,
  health-checked, observable system — dashboard visible, zero overselling proven on screen, and
  (bonus) a deliberate `kubectl delete pod` mid-demo showing the system survive it live.
- **Done:** this is what you show in an interview, and every piece of it is something you can defend
  under follow-up questions — because you built and understood it, not copied it.

---

## 5. Tech stack

- **Frontend:** React (or Next.js, matches your existing strength).
- **API/backend glue:** your call — Node.js (matches existing depth) or push further into Go.
- **Core reservation engine:** Go (the microservice this whole project is really about).
- **Database:** PostgreSQL (transactions, constraints, row locking — all real here).
- **Load testing:** k6.
- **Deployment:** Kubernetes (learn it properly here — new skill, budget real time).
- **Observability:** Prometheus + Grafana.

---

## 6. Timeline — adaptive, not compressed

No fixed deadline. Baseline estimate for a genuinely thorough pass, learning each new topic
properly rather than skimming (revised 2026-07-17 for the upgraded scope): **M0-M1 (contract +
engine + mechanism shootout) ~3-5 weeks, M2 (retry safety) ~1.5-2 weeks, M3 (product wrapper +
waitlist/cancellations) ~2-3 weeks, M4 (expiring holds) ~2 weeks, M5-M6 (multi-instance + load
testing + overload design) ~3-4 weeks, M7-M9 (K8s + observability + demo) ~3-5 weeks** — roughly
5-6.5 months total at the current ~6-7 focused hours/week. Re-baseline on real velocity per
milestone, same discipline as the Raft plan — do not force it to fit a number. Familiar ground
(M3) will run faster than estimated; K8s (M7) will run slower — expected, budgeted.

---

## 7. Risks

1. **Kubernetes is a real new skill — don't let it become "copy YAML from a tutorial."** Budget
   actual learning time for what each resource (Deployment, Service, Ingress, probes) is FOR, not
   just how to write it.
2. **Multi-instance correctness (M5) is easy to skip as "probably fine."** Explicitly test it. This
   is where the second layer of real depth lives.
3. **Load testing metrics can be gamed by accident** — e.g. testing against an inventory pool so
   large the race never manifests. Design the test to actually stress the contended path.
4. **Scope creep toward "add more product features."** The value is in the correctness story and
   the production-shape deployment — resist expanding into unrelated features. **Scope frozen
   2026-07-17. The freeze rule:** a new feature enters only if it adds a NEW correctness problem or
   is product-essential; otherwise it's parked. Explicitly REJECTED in writing (and rejecting them
   is itself part of the story): real payment processing (the fake charge's IDEMPOTENCY is the
   interesting part, not Stripe), reviews/ratings/recommendations, multi-region, event sourcing,
   Redis-based distributed locks. The 80/20 rule governs effort WITHIN scope too: the proof
   harnesses, benchmarks, retry safety, and state machine get the depth (that's the 20% that
   carries the signal); admin UI and frontend polish stay deliberately lean.

---

## 8. Progress & velocity log

Add a dated line per milestone: design pinned (plain English) → built → proven correct.

| Date | Milestone | Event | Note |
|---|---|---|---|
| 2026-07-17 | scope | Re-evaluated + FROZE final scope (deliberate, logged) | 3-agent analysis vs alternatives; verdict: stay + raise ceiling. Added: mechanism shootout (M1), retry safety (M2), holds (M4), waitlist/cancellations (M3), overload design (M6). Renumbered to M0-M9. |
| 2026-07-17 | M0 | Started — contract discussion + isolation levels unit | Two-sided contract articulated (no oversell + no undersell + truthful answers); race located in check-then-act gap; learned: atomicity ≠ isolation, READ COMMITTED lets the race through, quiz pending. |

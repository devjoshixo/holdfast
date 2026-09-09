# M2 — Retry safety: exactly-once client-visible booking

## The problem

Alice books seat 14A. The database commits the row. The pod dies before the response reaches her.
Her browser shows a spinner, then an error.

She clicks book again.

Today the second request runs `BookSeatCAS` from the top. It finds no matching `request_id`, reads
`seats.status`, sees `booked`, and returns `SeatTaken`. Alice is told the seat is gone. She actually
owns it.

That is the mild version. Once M6 adds payment, the same gap means a double charge, or a charge with
no booking.

M1 proved a guarantee about **different people** — thirty thousand requests, one seat, exactly one
winner. M2 is a guarantee about **one person seen twice**. The same intent arrives more than once
because the network lost the answer, and nothing in the seat row records who asked or how many times.

The client cannot know whether the lost attempt succeeded. Neither can the server after it restarts.
You do not fix that by asking harder. You fix it by making a retry safe to repeat, so that whether or
not the first attempt landed, running it again produces the same single outcome.

## Decision 1 — the client generates the idempotency key

The client mints the key when the user clicks Book, and sends it with the request.

The server cannot generate it. If the server invents the key and the response is lost, the client
never receives it. On the retry the client would have nothing to send, and the server would have no
way to recognise the second request as the same intent. A key is only useful if it survives on the
side that does the retrying.

**One `request_id` means one user intent, not one HTTP call.** An automatic retry of the same click
reuses the key — it is asking "what was my answer?", not "book me a seat now". If Alice clicks Book
a second time herself, that is a new intent, so the client mints a new key and gets a fresh answer
against the current state of the world.

That distinction is what makes a stored "no" defensible instead of a lie. The server is never
claiming the seat is still taken. It is repeating the answer to a question that was already asked and
already decided. Alice is never trapped, because asking again is always available to her.

## Decision 2 — keys live for a fixed window of hours

The rule: **the key must outlive the longest delay at which a retry can still arrive.**

Why the window matters, and it is not obvious. Suppose Alice loses the race and is told `SeatTaken`.
Four minutes later Bob's hold expires and seat 14A returns to `free`. Something retries her original
request — a proxy, a mobile app resuming from a background queue.

- If the key row still exists, the server replies `SeatTaken` again. Correct: same question, same
  answer.
- If the key row has been deleted, the server has no memory of Alice. It runs the booking logic from
  scratch, finds the seat free, and books it. She wakes up owning a seat she never asked for, and
  after M6, paying for it.

So the lifetime is bounded below by how late a retry can arrive. In practice that is unbounded,
because the client is outside our control — a phone in a tunnel can resend hours later. There is no
number that is provably safe. The honest answer is to pick a generous fixed window and accept the
small residual risk. Stripe keeps idempotency keys for 24 hours for exactly this reason.

**Do not set the key lifetime equal to the hold timeout.** With a 5 minute hold, seats start
returning to `free` at the 5 minute mark. If the key also dies at 5 minutes, the server loses its
memory at the exact instant the world starts changing. Both failure conditions line up by design
rather than by bad luck. Sit well above the hold timeout, not on it.

Cleanup is a periodic background delete:

```sql
DELETE FROM idempotency_keys
WHERE created_at < now() - interval '24 hours';
```

It never runs on the booking path. A user waiting on a seat must not pay for housekeeping.

Time-partitioning the table and dropping whole partitions is faster at scale, because dropping a
partition is instant while a large `DELETE` is not. At roughly 30,000 rows per event that is solving
a problem we do not have. Noted, not built.

## Decision 3 — a retry gets the stored original answer

When a request arrives carrying a key we have already decided, we do not run the booking logic again.
We read the stored outcome and return it.

Decision 2 contains the proof. A fresh computation can produce a different answer than the first one,
because the seat may have changed state in between. Only the stored answer is stable.

**Which outcomes get stored:**

| Outcome | Stored | Why |
| --- | --- | --- |
| `booked` | yes | a decision was made |
| `seat_taken` | yes | a decision was made, and it can go stale, which is exactly why it must be pinned |
| `sold_out` | yes | same shape as `seat_taken` — a held seat freeing up could flip it |
| `already_mine` | no | it is produced *by* a lookup hit, so it is never an original decision |
| `system_error` | no | not an answer at all |

The rule underneath the table: **memoize decisions, never failures.**

A system error is not a result, it is the absence of one. Nothing was decided, so there is nothing to
repeat. Storing it would freeze a temporary failure into a permanent one, when a retry is precisely
the correct response to a transient failure.

`already_mine` is a different case. It is the shape of the response to a hit on the key table, not a
value any first attempt can produce. A row is never written with that outcome.

## Decision 4 — claim the key first, then do the work

This is the hard one, and it is the case a naive idempotency-key design gets wrong.

### The failure

Alice's request gets stuck behind the connection pool and takes 3 seconds. Her client gives up at 2
seconds and retries with the same key. Two requests, A and B, are now inside the database at the same
moment, both carrying `abc`. Neither has finished, so neither has written anything.

If the key check is a `SELECT`, both requests run it and both find nothing. Reads cannot see
uncommitted rows, so B has no way to detect that A exists. Both proceed as first attempts.

A wins the CAS `UPDATE` on `seats` and commits. B's `UPDATE` matches nothing, so B loops, re-reads
the status, sees `booked`, and returns `SeatTaken`.

One key, two responses racing back to one customer. She sees whichever arrives first. If `SeatTaken`
lands first, she believes she has no seat while owning one — a direct violation of Guarantee 4.

### Why a lookup cannot fix it

No ordering of `SELECT` and work helps here, because the `SELECT` is blind to uncommitted siblings by
definition. The check has to be something that *collides* with A rather than something that reads
around it.

### The design

Put a `UNIQUE` constraint on `request_id` in the key table, and make the **first** thing every
request does an `INSERT` of its own key row — before any seat logic runs.

```
1. BEGIN
2. INSERT the key row (request_id UNIQUE)
     -> 23505: read the stored outcome and return it
3. run the CAS loop
4. UPDATE the key row with the final outcome, INSERT the booking row
5. COMMIT
```

When B tries to insert `abc` while A's uncommitted row is sitting there, Postgres cannot decide
whether the constraint is violated. The answer depends on something that has not happened yet: if A
commits, `abc` exists and B is a duplicate; if A rolls back, `abc` never existed and B is fine.

So Postgres does the only honest thing available. **It makes B wait.** B's `INSERT` holds until A's
transaction ends, one way or the other.

- **A committed.** B's insert fails with `23505`. B reads the key row — now visible — and returns the
  outcome A stored. B never touches the seat.
- **A rolled back.** The key `abc` does not exist. B's insert succeeds and B goes on to do the real
  work. Alice gets one proper attempt instead of zero.

Both branches are correct, and no coordination code was written. The uniqueness check does the
waiting.

### Why the order is the mechanism

If the claim is placed at the end, next to the booking insert, it never runs for the request that
needs it. B loses the CAS, returns `SeatTaken` at the status check, and exits long before reaching any
insert. The constraint is present in the code and unreachable in practice.

This is the same trap that killed the idea of folding the `request_id` check into the booking `INSERT`
itself. Stated generally, and worth remembering:

> **In CAS, any protection placed after the seat status read is dead code for losers.**

B has to be stopped before it can form an opinion. Once B has read the status it already holds an
answer, and it will return that answer. So the key insert is not a record of what happened. It is a
claim on the right to decide, taken before any deciding starts.

### Why both writes go in one transaction

The key row and the booking row are written inside the same transaction, so they land together or not
at all. If the booking rolls back and the key row survived, the key would claim a booking that does
not exist, and every future retry would be handed the outcome of an attempt that never completed.

### What this costs

Only requests carrying a `request_id` that another live request is already using ever wait. Two
different customers fighting over seat 14A carry different keys, never collide in the key table, and
meet only at the CAS `UPDATE` as before. CAS's property of never queueing on contention survives for
real traffic.

The claim-insert does not reduce the number of database round trips. It fires on every request, and a
write costs more than an indexed read, so per request it is slightly more expensive than the
pre-lookup `SELECT` it replaces. The saving is only against the naive key table, which needs a
`SELECT` and then an `INSERT` — two trips instead of one.

Cost is not the reason to do this. The `SELECT` design is **wrong**, not slow, and there was never a
cheaper correct option to trade against. That is the difference from M1: there, three mechanisms were
all correct and the numbers picked the winner. Here only one candidate is correct, so the numbers
decide nothing.

## Schema

```sql
CREATE TABLE idempotency_keys (
    request_id TEXT PRIMARY KEY,
    event_id   BIGINT NOT NULL REFERENCES events (id),
    seat_id    BIGINT NOT NULL REFERENCES seats (id),
    outcome    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`outcome` is nullable on purpose. Between step 2 and step 4 the row exists but no decision has been
made yet, so there is nothing honest to put in it. A null outcome means "claimed, still running".

`request_id` as the primary key gives the uniqueness and the index in one declaration.

## Definition of done

The M1 harness extended to:

1. drop a share of responses before the client sees them,
2. have those clients retry with the **same** `request_id`,
3. assert that every customer ends with exactly one booking, and that the answer they finally see
   matches what the database holds.

As in M1, the harness is the real work. The mechanism is short; proving it is not.

## What this does not cover

- **Payment.** M2 guarantees one booking per intent. One charge per intent is M6, and reuses the same
  key.
- **Cross-seat retries.** A retry carrying the same key but a different `seat_id` is a client bug. The
  key row records the seat it was claimed for, so this can be detected, but the behaviour is not
  specified here.
- **Key window tuning.** The window is picked, not derived. There is no proof that any number is
  large enough, only an argument that larger is safer.

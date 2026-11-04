# M2-results.md — the reader's questions

Step 1 of the artifact mechanic. **Questions only. No answers here.**

The questions reflect the critical reader's scrutiny. **Ordering is mine from
M4 onward** — the sequence of questions is the document's structure, and that is the part I was
missing when structuring the initial analysis.

---

## 1. Why this milestone exists

1. **What does M2 prove that M1 did not?**

*Why first: a reader arriving from `M1-results.md` needs the gap named before anything else. If they
cannot see what was still broken after M1, nothing below has a reason to exist.*

---

## 2. Scope — why one mechanism and not three

2. **Why prove retry safety on one mechanism instead of all three, the way M1 did?**
3. **Why CAS specifically?**

*Why second, and why these two are separate: M1 was a three-way shootout, so "one mechanism now" is
the reader's first objection. Answer it before they start doubting the whole milestone. And the two
questions have different answers — one is about what the milestone is for, the other is about my own
M1 data. Careful with #3: the M1 headline was "no single winner", so the honest claim is
regime-specific (the flash-sale premise lives at the concentrated end, which is where CAS won), not
a flat "CAS is fastest".*

---

## 3. The mechanism

4. **Who generates the idempotency key — client or server — and why?**
5. **Where is the key and its outcome stored?**
6. **Why is the key claimed FIRST? Why doesn't a pre-lookup work?**

*Why this order: identity (who mints it) -> location (where it lives) -> the ordering constraint that
makes it correct. #6 is the heart of the milestone and the only one of the three a reader could not
guess.*

---

## 4. What a client actually sees

7. **What does a retry get back?**
8. **Which outcomes are stored, and why not all of them?**

*Why here: the mechanism is established, so now describe the behaviour it produces. These were one
question in the first draft — split, because "what comes back" and "what is storable" have different
answers, and #8 is where the sharpest rule in the milestone lives.*

---

## 5. The hard race

9. **What happens when a retry arrives concurrently with — or before — the original request?**

*Why it gets its own section, and why it is NOT buried last: this is the case the 1000-pair test
exists to prove, and it is the hardest thing in M2. In the first draft it was question four of four
and read like an afterthought. It is a headline.*

---

## 6. Lifetime and cleanup

10. **How long do the keys live, and how was that duration chosen?**
11. **What removes them, and why does cleanup matter?**

*Why after the races: these are operational details. A reader will not care how long a key lives
until they believe the mechanism works. First draft had #10 sitting BEFORE the race question — a
detail outranking a fundamental. That inversion is the most common ordering mistake.*

---

## 7. The evidence

12. **How do I know it is exactly-once? What did the harness actually do, and what did it show?**
13. **Why does CAS retry LESS as contention increases?**

*Why near the end: claims first, proof second — the reader needs to know what is being proven before
numbers mean anything. #12 was missing entirely from the first draft, which would have left the
document as assertions. #13 is the counterintuitive finding in the data (42 -> 50 -> 15 -> 3 retries
as seats go 10000 -> 10) and it is what makes an artifact worth reading rather than merely correct.*

---

## 8. The ceiling

14. **What does M2 NOT protect against?**

*Why last: every decision entry and every `M1-results` section has one, and leaving it out makes the
document weaker rather than stronger. Knowing the limits of your own guarantee is the senior move,
and it is the natural handoff into M4.*

---

## Numbers available for section 7 (measured 2026-08-22)

**`TestBookSeatCAS_ConcurrentDuplicates`** — 1000 pairs sharing a `request_id`, 100 seats:

| | |
|---|---|
| Pairs that genuinely OVERLAPPED in time | **1000 / 1000** |
| `(Booked, AlreadyMine)` | **100** — exactly the seat count |
| `(SeatTaken, SeatTaken)` | **900** |
| Illegal outcome pairs | **0** |
| DB rows vs clients told booked | match exactly at **100** |

The 1000/1000 overlap is what makes this evidence rather than luck — the test proves it actually
raced, so it cannot have passed by accidental serialization.

**`TestBookSeatCAS_NoDoubleBooking`** — 30,000 attempts per seat level, every Booked/SeatTaken result
then retried with the same `request_id` (~120,000 retries total, all correct), 0 system errors:

| Seats | Booked | SeatTaken | Retries |
|---|---|---|---|
| 10,000 | 6,267 | 23,733 | 42 |
| 1,000 | 999 | 29,001 | 50 |
| 100 | 100 | 29,900 | 15 |
| 10 | 10 | 29,990 | 3 |

---

## Next session

Answer the 14 questions **alone**. A review pass only — no concept explanations, no
supplied structure. That precedent explicitly carries to the results artifact.

# M0 — The Correctness Contract

Guarantee 1. No matter how many requests come in — be it 5,000 — only 100 tickets are
booked. Nothing is undersold, nothing is oversold.

Guarantee 2. The system will not give a "no tickets" type error back while tickets are
there to sell. If the ticket is available at the time of the write, the booking will be done.

Guarantee 3. At no point will a read be an imaginary number — always a snapshot of the
real tickets sold and left at that given time.

Guarantee 4. If a booking is committed but the response is lost before the client sees it,
and the client sends the same request a second time, the system treats it as a retry and
not a new request: no second booking is created, and the retry receives the outcome of the
original attempt (whatever that outcome was). The system sees retries as retries and new
requests as new.

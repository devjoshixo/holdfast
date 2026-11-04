import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';

export const successfulHolds = new Counter('holds_successful_201');
export const conflictHolds   = new Counter('holds_conflict_409');
export const rateLimited     = new Counter('holds_ratelimited_429');
export const errorCount      = new Counter('holds_error_5xx');
export const holdLatency     = new Trend('hold_duration_ms', true);

export const options = {
  scenarios: {
    sustained_flash_sale: {
      executor: 'constant-vus',
      vus: 50,              // 50 concurrent buyers hammering constantly
      duration: '5s',       // for 5 seconds
    },
  },
  thresholds: {
    'holds_error_5xx': ['count==0'],
    'http_req_duration': ['p(95)<300'],
  },
};

const BASE_URL = __ENV.TARGET_URL || 'http://localhost:8080';

export default function () {
  const vuId = __VU;
  const iter = __ITER;
  const buyerId = `buyer-${vuId}-${iter}`;
  const eventId = 1;
  // Buyers randomly pick from the 40 seats (id 1 to 40)
  const seatId = Math.floor(Math.random() * 40) + 1;

  const payload = JSON.stringify({
    customer: buyerId,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': `k6-${vuId}-${iter}-${Date.now()}`,
    },
    tags: { name: 'SustainedFlashSale' },
  };

  const res = http.post(`${BASE_URL}/api/v1/events/${eventId}/seats/${seatId}/hold`, payload, params);

  holdLatency.add(res.timings.duration);

  if (res.status === 201) {
    successfulHolds.add(1);
  } else if (res.status === 409) {
    conflictHolds.add(1);
  } else if (res.status === 429) {
    rateLimited.add(1);
  } else if (res.status >= 500) {
    errorCount.add(1);
  }

  check(res, {
    'valid status': (r) => [201, 409, 429].includes(r.status),
  });
}

import http from 'k6/http';
import { check, sleep } from 'k6';

export let options = {
  stages: [
    { duration: '30s', target: 10 },
    { duration: '1m',  target: 20 },
    { duration: '30s', target: 0  },
  ],
  thresholds: {
    http_req_duration: ['p(95)<3000'],   // batch takes longer due to model sim
    http_req_failed:   ['rate<0.05'],
  },
};

const pageSizes = [20, 50, 100];

export default function () {
  const page  = Math.floor(Math.random() * 3) + 1;
  const limit = pageSizes[Math.floor(Math.random() * pageSizes.length)];

  const res = http.get(
    `http://localhost:8080/recommendations/batch?page=${page}&limit=${limit}`,
    { timeout: '30s' }
  );
  check(res, {
    'status is 200':       (r) => r.status === 200,
    'has results':         (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.results && body.results.length > 0;
      } catch {
        return false;
      }
    },
    'has summary':         (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.summary !== undefined;
      } catch {
        return false;
      }
    },
    'has success_count':   (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.summary && body.summary.success_count >= 0;
      } catch {
        return false;
      }
    },
    'has processing_time': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.summary && body.summary.processing_time_ms > 0;
      } catch {
        return false;
      }
    },
  });
  sleep(1);
}

import http from 'k6/http';
import { check, sleep } from 'k6';

export let options = {
  stages: [
    { duration: '30s', target: 50  },   // ramp up to 50 VUs
    { duration: '1m',  target: 100 },   // hold at 100 VUs (~100 RPS with sleep 0.1)
    { duration: '30s', target: 0   },   // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
    http_req_failed:   ['rate<0.03'],
  },
};

export default function () {
  const userId = Math.floor(Math.random() * 20) + 1;
  const res = http.get(
    `http://localhost:8080/users/${userId}/recommendations?limit=10`,
    { timeout: '5s' }
  );
  check(res, {
    'status is 200':       (r) => r.status === 200,
    'has recommendations': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.recommendations && body.recommendations.length > 0;
      } catch {
        return false;
      }
    },
    'has metadata':        (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.metadata !== undefined;
      } catch {
        return false;
      }
    },
    'cache_hit field set': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.metadata && body.metadata.cache_hit !== undefined;
      } catch {
        return false;
      }
    },
  });
  sleep(0.1);
}

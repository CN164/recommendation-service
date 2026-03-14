import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const cacheHitRate   = new Rate('cache_hit_rate');
const cacheLatency   = new Trend('cache_hit_latency_ms');
const noCacheLatency = new Trend('cache_miss_latency_ms');

export let options = {
  vus: 10,
  duration: '2m',   // 2 min: first pass warms cache, second pass measures hits
  thresholds: {
    cache_hit_rate: ['rate>0.70'],   // expect >70% hits after warm-up
  },
};

export default function () {
  // Small fixed user set → high cache hit probability after warm-up
  const userId = Math.floor(Math.random() * 5) + 1;
  const res = http.get(
    `http://localhost:8080/users/${userId}/recommendations?limit=10`
  );

  if (res.status === 200) {
    try {
      const body  = JSON.parse(res.body);
      const isHit = body.metadata && body.metadata.cache_hit === true;
      cacheHitRate.add(isHit);
      if (isHit) {
        cacheLatency.add(res.timings.duration);
      } else {
        noCacheLatency.add(res.timings.duration);
      }
    } catch (e) {
      // JSON parse error
    }
  }
  check(res, { 'status is 200': (r) => r.status === 200 });
}

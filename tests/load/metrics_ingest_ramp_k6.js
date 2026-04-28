import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const MACHINE_PREFIX = __ENV.MACHINE_PREFIX || 'k6-ramp-node';
const PRE_ALLOCATED_VUS = Number(__ENV.PRE_ALLOCATED_VUS || 200);
const MAX_VUS = Number(__ENV.MAX_VUS || 1500);

export const options = {
  scenarios: {
    ingest: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      preAllocatedVUs: PRE_ALLOCATED_VUS,
      maxVUs: MAX_VUS,
      stages: [
        { target: 100, duration: '1m' },
        { target: 300, duration: '1m' },
        { target: 500, duration: '1m' },
        { target: 800, duration: '1m' },
        { target: 1000, duration: '1m' },
        { target: 0, duration: '30s' },
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.02'],
    http_req_duration: ['p(95)<500'],
    checks: ['rate>0.98'],
  },
};

export default function () {
  const payload = JSON.stringify({
    machine_id: `${MACHINE_PREFIX}-${__VU % 200}`,
    ip: `10.1.${__VU % 255}.${__ITER % 255}`,
    cpu_usage: Math.random() * 100,
    mem_usage: 50 + Math.random() * 20,
    disk_usage: 40 + Math.random() * 30,
    timestamp: Math.floor(Date.now() / 1000),
  });

  const res = http.post(`${BASE_URL}/api/v1/metrics`, payload, {
    headers: { 'Content-Type': 'application/json' },
    timeout: '5s',
  });

  check(res, {
    'status is 200 or 202': (r) => r.status === 200 || r.status === 202,
  });
}

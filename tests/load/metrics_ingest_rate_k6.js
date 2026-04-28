import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || '60s';
const PRE_ALLOCATED_VUS = Number(__ENV.PRE_ALLOCATED_VUS || 100);
const MAX_VUS = Number(__ENV.MAX_VUS || 1000);
const MACHINE_PREFIX = __ENV.MACHINE_PREFIX || 'k6-rate-node';

export const options = {
  scenarios: {
    ingest: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PRE_ALLOCATED_VUS,
      maxVUs: MAX_VUS,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<300'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const payload = JSON.stringify({
    machine_id: `${MACHINE_PREFIX}-${__VU % 100}`,
    ip: `10.0.${__VU % 255}.${__ITER % 255}`,
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

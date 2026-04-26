import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export const options = {
  vus: 20,
  duration: '30s',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const payload = JSON.stringify({
    machine_id: 'k6-load-node',
    ip: '10.0.0.1',
    cpu_usage: Math.random() * 100,
    mem_usage: 50 + Math.random() * 20,
    disk_usage: 40 + Math.random() * 30,
    timestamp: Math.floor(Date.now() / 1000),
  });

  const res = http.post(`${BASE_URL}/api/v1/metrics`, payload, {
    headers: { 'Content-Type': 'application/json' },
  });

  check(res, {
    'status is 200 or 202': (r) => r.status === 200 || r.status === 202,
  });

  sleep(0.2);
}

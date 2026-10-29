import http from 'k6/http';
import {check, sleep} from 'k6';

const SKUS = ['WIDGET', 'GADGET', 'GIZMO'];

export const options = {
  stages: [
    {duration: '10s', target: 50},
    {duration: '20s', target: 400},
    {duration: '15s', target: 800},
    {duration: '15s', target: 800},
    {duration: '5s', target: 0},
  ],
  thresholds: {
    http_req_duration: ['p(95)<50'],
    http_req_failed: ['rate<0.005'],
    checks: ['rate>0.995'],
  },
};

export default function () {
  const list = http.get(`${__ENV.BASE_URL}/orders`);
  check(list, {'list orders -> 200': (r) => r.status === 200});

  const sku = SKUS[Math.floor(Math.random() * SKUS.length)];
  const created = http.post(
    `${__ENV.BASE_URL}/orders`,
    JSON.stringify({sku, qty: 1}),
    {headers: {'Content-Type': 'application/json'}},
  );
  check(created, {'create order -> 201': (r) => r.status === 201});

  sleep(0.5);
}

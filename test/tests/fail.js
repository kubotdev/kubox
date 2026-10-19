import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  stages: [
    { duration: '5s', target: 2 },
    { duration: '3s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<0.000001'],
  },
};

export default function () {
  http.get(`${__ENV.BASE_URL}/`);
  sleep(0.2);
}

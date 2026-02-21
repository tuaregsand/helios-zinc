import http from 'k6/http';
import { check, sleep } from 'k6';

const baseUrl = __ENV.BASE_URL || 'http://localhost:8080';
const apiKey = __ENV.API_KEY || '';
const openapiToken = __ENV.OPENAPI_TOKEN || '';
const duration = __ENV.DURATION || '2m';
const rate = Number(__ENV.RATE || 200);
const preAllocatedVUs = Number(__ENV.PRE_ALLOCATED_VUS || 50);
const maxVUs = Number(__ENV.MAX_VUS || 400);
const thinkTimeMs = Number(__ENV.THINK_TIME_MS || 50);
const summaryPath = __ENV.SUMMARY_PATH || 'load/k6/results/latest-summary.json';

export const options = {
  scenarios: {
    steady_reads: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs,
      maxVUs,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    'http_req_duration{endpoint:watermarks}': ['p(95)<750'],
    'http_req_duration{endpoint:replay_jobs}': ['p(95)<750'],
    'http_req_duration{endpoint:health}': ['p(95)<200'],
  },
};

function authHeaders(includeOpenAPIToken) {
  const headers = {
    'Content-Type': 'application/json',
  };
  if (apiKey !== '') {
    headers['X-API-Key'] = apiKey;
  }
  if (includeOpenAPIToken && openapiToken !== '') {
    headers['X-OpenAPI-Token'] = openapiToken;
  }
  return headers;
}

function weightedEndpoint() {
  const roll = Math.random();
  if (roll < 0.7) {
    return {
      name: 'watermarks',
      path: '/v1/watermarks?limit=250',
      expected: 200,
      headers: authHeaders(false),
    };
  }
  if (roll < 0.95) {
    return {
      name: 'replay_jobs',
      path: '/v1/replay/jobs?limit=50',
      expected: 200,
      headers: authHeaders(false),
    };
  }
  return {
    name: 'health',
    path: '/healthz',
    expected: 200,
    headers: authHeaders(false),
  };
}

export default function () {
  const endpoint = weightedEndpoint();
  const res = http.get(`${baseUrl}${endpoint.path}`, {
    headers: endpoint.headers,
    tags: {
      endpoint: endpoint.name,
    },
  });

  check(res, {
    'status is expected': (r) => r.status === endpoint.expected,
    'response is not empty': (r) => r.body && r.body.length > 0,
  });

  if (Math.random() < 0.05) {
    const openapi = http.get(`${baseUrl}/openapi.yaml`, {
      headers: authHeaders(true),
      tags: {
        endpoint: 'openapi',
      },
    });
    check(openapi, {
      'openapi status': (r) => r.status === 200 || r.status === 401,
    });
  }

  sleep(thinkTimeMs / 1000);
}

export function handleSummary(data) {
  const failedRate = data.metrics.http_req_failed?.values?.rate ?? 0;
  const p95 = data.metrics.http_req_duration?.values?.['p(95)'] ?? 0;
  const rps = data.metrics.http_reqs?.values?.rate ?? 0;

  const short = {
    baseUrl,
    duration,
    rate,
    preAllocatedVUs,
    maxVUs,
    requestsPerSecond: Number(rps.toFixed(2)),
    failedRate: Number(failedRate.toFixed(6)),
    p95Ms: Number(p95.toFixed(2)),
    timestamp: new Date().toISOString(),
  };

  return {
    [summaryPath]: JSON.stringify(data, null, 2),
    [`${summaryPath}.brief.json`]: JSON.stringify(short, null, 2),
    stdout: JSON.stringify(short, null, 2),
  };
}

import { Controller, Get, Header, Inject, Injectable } from '@nestjs/common';

export const METRICS = Symbol('METRICS');
const durationBuckets = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5];

interface RequestMetric {
  method: string;
  route: string;
  status: number;
  count: number;
  sum: number;
  buckets: number[];
}

@Injectable()
export class Metrics {
  private readonly requests = new Map<string, RequestMetric>();

  observe(method: string, route: string, status: number, seconds: number) {
    const safeMethod = /^[A-Z]{1,12}$/.test(method) ? method : 'OTHER';
    const safeRoute = route.startsWith('/') && route.length <= 256 ? route : 'unmatched';
    const safeStatus = Number.isInteger(status) && status >= 100 && status <= 599 ? status : 0;
    const key = `${safeMethod}\u0000${safeRoute}\u0000${safeStatus}`;
    let metric = this.requests.get(key);
    if (!metric) {
      metric = { method: safeMethod, route: safeRoute, status: safeStatus, count: 0, sum: 0,
        buckets: durationBuckets.map(() => 0) };
      this.requests.set(key, metric);
    }
    metric.count++;
    metric.sum += seconds;
    for (let index = 0; index < durationBuckets.length; index++) {
      if (seconds <= durationBuckets[index]!) metric.buckets[index] = metric.buckets[index]! + 1;
    }
  }

  render(): string {
    const lines = [
      '# HELP mesh_control_http_requests_total Completed controller HTTP requests.',
      '# TYPE mesh_control_http_requests_total counter',
    ];
    for (const metric of this.requests.values()) {
      const labels = `method="${escapeLabel(metric.method)}",route="${escapeLabel(metric.route)}",status="${metric.status}"`;
      lines.push(`mesh_control_http_requests_total{${labels}} ${metric.count}`);
    }
    lines.push('# HELP mesh_control_http_request_duration_seconds Controller HTTP request duration.',
      '# TYPE mesh_control_http_request_duration_seconds histogram');
    for (const metric of this.requests.values()) {
      const labels = `method="${escapeLabel(metric.method)}",route="${escapeLabel(metric.route)}",status="${metric.status}"`;
      for (let index = 0; index < durationBuckets.length; index++) {
        lines.push(`mesh_control_http_request_duration_seconds_bucket{${labels},le="${durationBuckets[index]}"} ${metric.buckets[index]}`);
      }
      lines.push(`mesh_control_http_request_duration_seconds_bucket{${labels},le="+Inf"} ${metric.count}`,
        `mesh_control_http_request_duration_seconds_sum{${labels}} ${metric.sum}`,
        `mesh_control_http_request_duration_seconds_count{${labels}} ${metric.count}`);
    }
    const memory = process.memoryUsage();
    lines.push('# HELP mesh_control_process_resident_memory_bytes Resident memory used by the controller.',
      '# TYPE mesh_control_process_resident_memory_bytes gauge',
      `mesh_control_process_resident_memory_bytes ${memory.rss}`,
      '# HELP mesh_control_process_uptime_seconds Controller process uptime.',
      '# TYPE mesh_control_process_uptime_seconds gauge',
      `mesh_control_process_uptime_seconds ${process.uptime()}`);
    return `${lines.join('\n')}\n`;
  }
}

function escapeLabel(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('\n', '\\n');
}

@Controller()
export class MetricsController {
  constructor(@Inject(Metrics) private readonly metrics: Metrics) {}

  @Get('metrics')
  @Header('Content-Type', 'text/plain; version=0.0.4; charset=utf-8')
  show() { return this.metrics.render(); }
}

export interface RateLimitDecision {
  allowed: boolean;
  retryAfterSeconds: number;
}

interface Bucket {
  tokens: number;
  updatedAt: number;
}

// A bounded token bucket protects a single controller process. The production
// edge applies a second independent limit before requests reach this process.
export class RateLimiter {
  private readonly buckets = new Map<string, Bucket>();
  constructor(private readonly perMinute: number, private readonly burst = perMinute, private readonly maxKeys = 20_000) {}

  check(key: string, now = Date.now()): RateLimitDecision {
    let bucket = this.buckets.get(key);
    if (!bucket) {
      if (this.buckets.size >= this.maxKeys) this.prune(now);
      if (this.buckets.size >= this.maxKeys) return { allowed: false, retryAfterSeconds: 60 };
      bucket = { tokens: this.burst, updatedAt: now };
      this.buckets.set(key, bucket);
    }
    const elapsed = Math.max(0, now - bucket.updatedAt);
    bucket.tokens = Math.min(this.burst, bucket.tokens + elapsed * this.perMinute / 60_000);
    bucket.updatedAt = now;
    if (bucket.tokens >= 1) {
      bucket.tokens--;
      return { allowed: true, retryAfterSeconds: 0 };
    }
    return { allowed: false, retryAfterSeconds: Math.max(1, Math.ceil((1 - bucket.tokens) * 60 / this.perMinute)) };
  }

  private prune(now: number) {
    const idle = 2 * 60_000;
    for (const [key, bucket] of this.buckets) {
      if (now - bucket.updatedAt >= idle) this.buckets.delete(key);
    }
  }
}

export function deliverySafetyMarginMs(leaseMs: number): number {
  return Math.min(1_000, Math.max(50, Math.floor(leaseMs / 10)))
}

import type { ProviderWorkReservation } from './types'

export class GatewayAtCapacityError extends Error {}

export class WorkReservationScope {
  private closed = false
  private readonly reservations = new Set<ScopedWorkReservation>()

  constructor(private readonly reserveUnderlying: (bytes: number) => ProviderWorkReservation) {}

  reserve = (bytes: number): ProviderWorkReservation => {
    if (this.closed) throw new Error('provider work reservation scope is closed')
    const underlying = this.reserveUnderlying(bytes)
    const reservation = new ScopedWorkReservation(underlying, () => {
      this.reservations.delete(reservation)
    })
    this.reservations.add(reservation)
    return reservation
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    for (const reservation of [...this.reservations]) reservation.release()
  }
}

// WorkByteBudget is a process-local admission boundary for retained webhook
// bodies, downloaded media, and serialization headroom. Its reserve method is
// an arrow function so providers can safely pass it across async scopes.
export class WorkByteBudget {
  private used = 0

  constructor(readonly limitBytes: number) {
    validateBytes(limitBytes)
    if (limitBytes === 0) throw new Error('work byte budget must be positive')
  }

  get usedBytes(): number {
    return this.used
  }

  reserve = (bytes: number): ProviderWorkReservation => {
    validateBytes(bytes)
    if (!this.tryAdjust(bytes)) throw new GatewayAtCapacityError()
    return new BudgetReservation(bytes, (delta) => this.tryAdjust(delta))
  }

  tryAdjust = (bytes: number): boolean => {
    if (!Number.isSafeInteger(bytes)) throw new Error('work byte adjustment must be a safe integer')
    const next = this.used + bytes
    if (next < 0) throw new Error('work byte budget underflow')
    if (bytes > 0 && next > this.limitBytes) return false
    this.used = next
    return true
  }
}

class BudgetReservation implements ProviderWorkReservation {
  private released = false

  constructor(
    private bytes: number,
    private readonly adjust: (bytes: number) => boolean,
  ) {}

  resize = (bytes: number): void => {
    validateBytes(bytes)
    if (this.released) throw new Error('provider work reservation is released')
    const delta = bytes - this.bytes
    if (delta > 0 && !this.adjust(delta)) throw new GatewayAtCapacityError()
    if (delta < 0) this.adjust(delta)
    this.bytes = bytes
  }

  release = (): void => {
    if (this.released) return
    this.released = true
    if (this.bytes > 0) this.adjust(-this.bytes)
    this.bytes = 0
  }
}

class ScopedWorkReservation implements ProviderWorkReservation {
  private released = false

  constructor(
    private readonly underlying: ProviderWorkReservation,
    private readonly onRelease: () => void,
  ) {}

  resize = (bytes: number): void => {
    if (this.released) throw new Error('provider work reservation is released')
    this.underlying.resize(bytes)
  }

  release = (): void => {
    if (this.released) return
    this.released = true
    this.underlying.release()
    this.onRelease()
  }
}

function validateBytes(bytes: number): void {
  if (!Number.isSafeInteger(bytes) || bytes < 0) {
    throw new Error('work byte count must be a non-negative safe integer')
  }
}

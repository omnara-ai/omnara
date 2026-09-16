import { describe, expect, it } from 'vitest'

import { GatewayAtCapacityError, WorkByteBudget, WorkReservationScope } from './work-budget'

describe('child work byte budgets', () => {
  it('charges actual child reservations to the parent alongside independent work', () => {
    const parent = new WorkByteBudget(100)
    const ingress = parent.reserve(20)
    const child = new WorkByteBudget(50, parent)
    const work = child.reserve(30)
    expect([child.usedBytes, parent.usedBytes]).toEqual([30, 50])
    work.resize(45)
    expect([child.usedBytes, parent.usedBytes]).toEqual([45, 65])
    work.resize(10)
    expect([child.usedBytes, parent.usedBytes]).toEqual([10, 30])
    work.release()
    work.release()
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 20])
    ingress.release()
    expect(parent.usedBytes).toBe(0)
  })

  it('rejects a child limit without charging the parent or changing retained work', () => {
    const parent = new WorkByteBudget(100)
    const child = new WorkByteBudget(50, parent)
    const work = child.reserve(40)
    expect(() => child.reserve(11)).toThrow(GatewayAtCapacityError)
    expect(() => {
      work.resize(51)
    }).toThrow(GatewayAtCapacityError)
    expect([child.usedBytes, parent.usedBytes]).toEqual([40, 40])
    work.resize(50)
    work.release()
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
  })

  it('rejects parent pressure atomically and admits the same resize after pressure clears', () => {
    const parent = new WorkByteBudget(100)
    const child = new WorkByteBudget(80, parent)
    const work = child.reserve(20)
    const ingress = parent.reserve(70)
    expect(() => child.reserve(11)).toThrow(GatewayAtCapacityError)
    expect(() => {
      work.resize(31)
    }).toThrow(GatewayAtCapacityError)
    expect([child.usedBytes, parent.usedBytes]).toEqual([20, 90])
    ingress.release()
    work.resize(31)
    expect([child.usedBytes, parent.usedBytes]).toEqual([31, 31])
    work.release()
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
  })

  it('accounts for sibling consumers and propagates failure through a parent chain', () => {
    const global = new WorkByteBudget(100)
    const provider = new WorkByteBudget(80, global)
    const first = new WorkByteBudget(60, provider)
    const second = new WorkByteBudget(60, provider)
    const retained = first.reserve(50)
    const other = second.reserve(20)
    expect(() => {
      other.resize(40)
    }).toThrow(GatewayAtCapacityError)
    expect([first.usedBytes, second.usedBytes, provider.usedBytes, global.usedBytes]).toEqual([
      50, 20, 70, 70,
    ])
    retained.release()
    other.resize(60)
    other.release()
    expect([provider.usedBytes, global.usedBytes]).toEqual([0, 0])
  })

  it('rejects invalid adjustments and released resizes without mutating either budget', () => {
    const parent = new WorkByteBudget(100)
    const child = new WorkByteBudget(50, parent)
    const work = child.reserve(10)
    for (const invalid of [-1, 0.5, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
      expect(() => child.reserve(invalid)).toThrow()
      expect(() => {
        work.resize(invalid)
      }).toThrow()
      expect([child.usedBytes, parent.usedBytes]).toEqual([10, 10])
    }
    expect(() => child.tryAdjust(-11)).toThrow('underflow')
    expect([child.usedBytes, parent.usedBytes]).toEqual([10, 10])
    work.release()
    expect(() => {
      work.resize(1)
    }).toThrow('released')
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
  })

  it('returns abandoned child reservations once when their work scope closes', () => {
    const parent = new WorkByteBudget(100)
    const child = new WorkByteBudget(50, parent)
    const scope = new WorkReservationScope(child.reserve)
    scope.reserve(0).resize(15)
    const released = scope.reserve(20)
    released.release()
    scope.reserve(10)
    expect([child.usedBytes, parent.usedBytes]).toEqual([25, 25])
    scope.close()
    scope.close()
    released.release()
    expect([child.usedBytes, parent.usedBytes]).toEqual([0, 0])
    expect(() => scope.reserve(1)).toThrow('closed')
  })
})

import { vi } from 'vitest'

export function trackAbortListeners(signal: AbortSignal) {
  const listeners = new Set<EventListenerOrEventListenerObject>()
  const add = signal.addEventListener.bind(signal)
  const remove = signal.removeEventListener.bind(signal)
  vi.spyOn(signal, 'addEventListener').mockImplementation((type, listener, options) => {
    if (type === 'abort') listeners.add(listener)
    add(type, listener, options)
  })
  vi.spyOn(signal, 'removeEventListener').mockImplementation((type, listener, options) => {
    if (type === 'abort') listeners.delete(listener)
    remove(type, listener, options)
  })
  return listeners
}

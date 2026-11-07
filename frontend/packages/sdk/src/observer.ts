/** Local diagnostics must never change the operation they describe. */
export function notifyObserver<T>(
  observer: ((observation: T) => void | Promise<void>) | undefined,
  observation: T,
): void {
  if (observer === undefined) return
  try {
    void Promise.resolve(observer(observation)).catch(() => undefined)
  } catch {
    // Observers are best effort, including synchronous callback failures.
  }
}

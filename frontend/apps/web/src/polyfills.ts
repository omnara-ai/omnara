// AbortSignal.any arrived in Safari 17.4, Chrome 116, and Firefox 124; older engines
// get the same behavior from this. Delete once nothing below that floor matters.
const signalStatics: { any?: (signals: AbortSignal[]) => AbortSignal } = AbortSignal

signalStatics.any ??= (signals) => {
  const controller = new AbortController()
  for (const signal of signals) {
    if (signal.aborted) {
      controller.abort(signal.reason)
      break
    }
    signal.addEventListener(
      'abort',
      () => {
        controller.abort(signal.reason)
      },
      { once: true },
    )
  }
  return controller.signal
}

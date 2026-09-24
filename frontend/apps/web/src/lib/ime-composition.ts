// Safari fires compositionend BEFORE the keydown of the Enter that confirmed an
// IME candidate, so that keydown arrives with isComposing already false and the
// half-composed line is sent. Chrome and Firefox fire it after, which is why
// `event.isComposing` alone looks correct everywhere except Safari and WKWebView.
export const SAFARI_IME_RACE_WINDOW_MS = 30

let composing = false
let lastCompositionEndAt = Number.NEGATIVE_INFINITY

export function installImeCompositionTracker(target: Window): () => void {
  function onCompositionStart() {
    composing = true
  }

  function onCompositionEnd() {
    composing = false
    lastCompositionEndAt = performance.now()
  }

  function onBlur() {
    composing = false
  }

  // Capture phase: composition events from a portalled editor still pass the
  // window on the way down even where a handler stops propagation, and blur
  // does not bubble at all.
  target.addEventListener('compositionstart', onCompositionStart, true)
  target.addEventListener('compositionend', onCompositionEnd, true)
  target.addEventListener('blur', onBlur, true)
  return () => {
    target.removeEventListener('compositionstart', onCompositionStart, true)
    target.removeEventListener('compositionend', onCompositionEnd, true)
    target.removeEventListener('blur', onBlur, true)
    composing = false
    lastCompositionEndAt = Number.NEGATIVE_INFINITY
  }
}

export function isImeComposing(event: KeyboardEvent, now = performance.now()): boolean {
  if (event.isComposing || composing) return true
  // The confirming keystroke follows its own compositionend synchronously. A
  // second, deliberate Enter takes a person far longer than this window.
  return now - lastCompositionEndAt < SAFARI_IME_RACE_WINDOW_MS
}

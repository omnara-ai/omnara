import { useSyncExternalStore } from 'react'

const MOBILE_BREAKPOINT = 768

export function useIsMobile() {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)
}

const mediaQuery = `(max-width: ${MOBILE_BREAKPOINT - 1}px)`

function subscribe(onStoreChange: () => void) {
  const query = window.matchMedia(mediaQuery)
  query.addEventListener('change', onStoreChange)
  return () => {
    query.removeEventListener('change', onStoreChange)
  }
}

function getSnapshot() {
  return window.matchMedia(mediaQuery).matches
}

function getServerSnapshot() {
  return false
}

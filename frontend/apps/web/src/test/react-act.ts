declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

export function enableReactActEnvironment(): () => void {
  const previous = globalThis.IS_REACT_ACT_ENVIRONMENT
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  return () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = previous
  }
}

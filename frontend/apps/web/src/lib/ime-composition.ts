export function isImeComposing(event: KeyboardEvent): boolean {
  // eslint-disable-next-line @typescript-eslint/no-deprecated -- Safari flags the Enter that confirms an IME candidate only with keyCode 229
  return event.isComposing || event.keyCode === 229
}

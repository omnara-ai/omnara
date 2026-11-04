export type ConfigSourceFormat = 'yaml' | 'json'

export function isJsonSource(source: string) {
  try {
    JSON.parse(source)
    return true
  } catch {
    return false
  }
}

export function configSourceFormat(source: string, storedFormat: ConfigSourceFormat = 'yaml') {
  return isJsonSource(source) ? 'json' : storedFormat
}

import type { ProviderFactory } from './types'

// Provider packages register here in integration-specific PRs. Keeping this empty makes
// the foundation executable without silently shipping a partially configured provider.
export function builtInProviderFactories(): ProviderFactory[] {
  return []
}

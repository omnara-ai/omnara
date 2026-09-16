import { writeSync } from 'node:fs'

/** The provider already exhausted its stop deadline. A live SDK worker can keep
 * Node running indefinitely, so exitCode or another cleanup wait is insufficient.
 * Emit no provider-supplied diagnostics or credentials on this last-resort path.
 */
export function terminateGatewayRuntime(_error: Error): never {
  try {
    writeSync(2, 'channel gateway: provider runtime could not stop; exiting for replacement\n')
  } finally {
    process.exit(1)
  }
}

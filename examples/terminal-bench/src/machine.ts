import { sdk } from '@omnara/sdk'

import type { Omnara } from './omnara.ts'

export interface ExecResult {
  exit_code: number | null
  output: string
  output_truncated: boolean
  timed_out: boolean
  duration_ms: number
  /** Why the command didn't run, e.g. a missing working directory. */
  error?: string
}

export async function exec(
  omnara: Omnara,
  machineID: string,
  command: string,
  options: { cwd?: string; env?: Record<string, string>; timeoutSeconds?: number } = {},
): Promise<ExecResult> {
  const { data } = await sdk.execMachineCommand({
    client: omnara.client,
    path: { orgID: omnara.orgID, machineID },
    body: {
      command,
      shell: 'bash',
      cwd: options.cwd,
      env: options.env,
      timeout_seconds: Math.min(Math.max(Math.ceil(options.timeoutSeconds ?? 300), 1), 3600),
    },
  })
  return data
}

/** Runs a command and throws with its output when it exits non-zero. */
export async function execOrThrow(
  omnara: Omnara,
  machineID: string,
  command: string,
  options: Parameters<typeof exec>[3] = {},
): Promise<ExecResult> {
  const result = await exec(omnara, machineID, command, options)
  if (result.exit_code !== 0) {
    throw new Error(
      `command failed (exit ${result.exit_code}${result.timed_out ? ', timed out' : ''}${result.error ? `, ${result.error}` : ''}): ${command}\n${result.output.slice(-4000)}`,
    )
  }
  return result
}

export async function upload(omnara: Omnara, machineID: string, path: string, bytes: Uint8Array): Promise<void> {
  await sdk.uploadMachineFile({
    client: omnara.client,
    path: { orgID: omnara.orgID, machineID },
    query: { path },
    body: new Blob([new Uint8Array(bytes)]),
    headers: { 'Content-Type': 'application/octet-stream' },
  })
}

export async function download(omnara: Omnara, machineID: string, path: string): Promise<Uint8Array> {
  const { data } = await sdk.downloadMachineFile({
    client: omnara.client,
    path: { orgID: omnara.orgID, machineID },
    query: { path },
    parseAs: 'arrayBuffer',
  })
  return new Uint8Array(data as unknown as ArrayBuffer)
}

import { spawn, type ChildProcess } from 'node:child_process'
import { chmodSync, copyFileSync, mkdirSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import type { CliEnv } from './env.js'

export interface DaemonState {
  apiUrl: string
  orgId: string
  machineId: string
  daemonToken: string
}

function omnaradHome(env: CliEnv): string {
  return path.join(env.daemonHome, 'omnarad')
}

function stateFile(env: CliEnv): string {
  return path.join(env.daemonHome, 'state.json')
}

function logFile(env: CliEnv): string {
  return path.join(env.daemonHome, 'omnarad.log')
}

export function loadDaemonState(env: CliEnv): DaemonState | undefined {
  try {
    const state = JSON.parse(readFileSync(stateFile(env), 'utf8')) as DaemonState
    if (state.apiUrl !== env.apiUrl) return undefined
    return state
  } catch {
    return undefined
  }
}

export function saveDaemonState(env: CliEnv, state: DaemonState): void {
  mkdirSync(env.daemonHome, { recursive: true })
  writeFileSync(stateFile(env), `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 })
}

export interface DaemonIdentity {
  installationId: string
  machineId: string
}

export function writeDaemonConfig(env: CliEnv, daemonToken: string, identity: DaemonIdentity): void {
  const home = omnaradHome(env)
  mkdirSync(home, { recursive: true })
  const config = {
    schema_version: 1,
    api_url: env.apiUrl,
    installation_id: identity.installationId,
    machine_id: identity.machineId,
    machine_token: daemonToken,
    no_update: true,
    runner_path: process.env.PATH ?? '',
  }
  writeFileSync(path.join(home, 'daemon.json'), `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 })
}

function ensureDaemonBinary(env: CliEnv): string {
  if (env.omnaradBinary == null) {
    throw new Error('OMNARAD_BINARY is required to run the local machine daemon; point it at a prebuilt omnarad')
  }
  const binDir = path.join(omnaradHome(env), 'bin')
  mkdirSync(binDir, { recursive: true })
  const binary = path.join(binDir, 'omnarad')
  copyFileSync(env.omnaradBinary, binary)
  chmodSync(binary, 0o755)
  return binary
}

export function startDaemon(env: CliEnv, daemonToken: string): ChildProcess {
  const binary = ensureDaemonBinary(env)
  const output = openSync(logFile(env), 'a')
  const child = spawn(binary, ['start', '--no-service'], {
    env: {
      ...process.env,
      OMNARA_API_URL: env.apiUrl,
      OMNARA_MACHINE_TOKEN: daemonToken,
      OMNARA_HOME: omnaradHome(env),
      OMNARA_NO_UPDATE: '1',
    },
    stdio: ['ignore', output, output],
  })
  child.on('exit', (code) => {
    if (code == null || code === 0) return
    console.error(`omnarad exited with code ${code}; last output from ${logFile(env)}:`)
    try {
      const lines = readFileSync(logFile(env), 'utf8').trimEnd().split('\n')
      for (const line of lines.slice(-5)) console.error(`  ${line}`)
    } catch {}
  })
  return child
}

export function stopDaemon(child: ChildProcess): void {
  if (child.exitCode == null && child.signalCode == null) {
    child.kill('SIGTERM')
  }
}

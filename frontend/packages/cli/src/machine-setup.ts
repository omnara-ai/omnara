import { spawn, spawnSync } from 'node:child_process'
import { accessSync, constants, existsSync, readdirSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, dirname, isAbsolute, join, resolve } from 'node:path'

import { type ConnectByoMachineResponse, sdk } from '@omnara/sdk'
import { zConnectByoMachineRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'
import type { OutputFormat } from './format.ts'
import { CliInputError } from './output.ts'
import type { FlowReporter } from './reporter.ts'

export const zMachineSetupBody = zConnectByoMachineRequest

type MachineSetupBody = z.output<typeof zMachineSetupBody>
type MachineSetupContext = FlowContext<{ orgID: string }, MachineSetupBody>

async function connectMachine(context: MachineSetupContext): Promise<ConnectByoMachineResponse> {
  const { client, path, body, report } = context
  report.start('Creating machine with a daemon token')
  try {
    const { data } = await sdk.connectByoMachine({ client, path, body })
    report.stop(`Machine created: ${data.machine.id}`)
    if (data.project_grants.length > 0) {
      report.info(`Granted to ${data.project_grants.length} project(s)`)
    }
    return data
  } catch (error) {
    report.fail('Machine could not be created')
    throw error
  }
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`
}

function installScriptUrl(apiUrl: string): string {
  return new URL('/install/omnarad.sh', apiUrl).toString()
}

export function installCommand(apiUrl: string): string {
  return `curl -fsSL ${shellQuote(installScriptUrl(apiUrl))} | sh`
}

export const formatMachineSetup: OutputFormat<ConnectByoMachineResponse> = (
  response,
  { apiUrl },
) => ({
  value: {
    ...response,
    install_command: installCommand(apiUrl),
    install_note: 'Run install_command on the target machine and paste the token when prompted.',
  },
})

const daemonConfigFileName = 'daemon.json'
const installLockFileName = 'install.lock'
const daemonLockFileName = 'daemon.lock'
const daemonLockPollMs = 250

const zDaemonConfigFile = z.looseObject({
  machine_id: z.string().optional(),
  api_url: z.string().optional(),
})

function daemonHome(): string {
  const override = process.env.OMNARA_HOME
  if (override !== undefined && override !== '') {
    if (!isAbsolute(override) || resolve(override) !== override) {
      throw new CliInputError('OMNARA_HOME must be an absolute clean path')
    }
    if (dirname(override) === override) {
      throw new CliInputError('OMNARA_HOME cannot be a filesystem root')
    }
    return override
  }
  const userHome = homedir()
  if (userHome === '' || !isAbsolute(userHome) || userHome.trim() !== userHome) {
    throw new CliInputError('HOME must be an absolute path so omnarad can install under ~/.omnarad')
  }
  return join(userHome, '.omnarad')
}

const supportedPlatforms = new Set<NodeJS.Platform>(['darwin', 'linux'])
const supportedArchitectures = new Set<NodeJS.Architecture>(['x64', 'arm64'])

export function ensureSupportedPlatform(
  platform: NodeJS.Platform,
  architecture: NodeJS.Architecture,
): void {
  if (!supportedPlatforms.has(platform)) {
    throw new CliInputError(`omnarad supports macOS and Linux; this machine reports ${platform}`)
  }
  if (!supportedArchitectures.has(architecture)) {
    throw new CliInputError(
      `omnarad supports amd64 and arm64; this machine reports ${architecture}`,
    )
  }
}

function isLoopbackHost(hostname: string): boolean {
  if (hostname === 'localhost' || hostname === '[::1]' || hostname === '::1') return true
  const octets = hostname.split('.')
  return (
    octets.length === 4 && octets[0] === '127' && octets.every((octet) => /^\d{1,3}$/.test(octet))
  )
}

export function ensureDaemonApiUrl(raw: string): void {
  const trimmed = raw.trim()
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    throw new CliInputError(`omnarad rejects the API URL ${raw}: it must be an absolute URL`)
  }
  const reject = (reason: string): never => {
    throw new CliInputError(`omnarad rejects the API URL ${raw}: ${reason}`)
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    reject('the scheme must be https or loopback http')
  }
  if (parsed.hostname === '') reject('a host is required')
  if (parsed.username !== '' || parsed.password !== '') reject('it must not contain user info')
  if (trimmed.includes('?') || trimmed.includes('#')) {
    reject('it must not contain a query or fragment')
  }
  const hostname = parsed.hostname.toLowerCase()
  if (
    parsed.protocol === 'http:' &&
    hostname !== 'host.docker.internal' &&
    !isLoopbackHost(hostname)
  ) {
    reject('it must use https unless the host is local')
  }
}

function describeDaemonConfig(configPath: string): string {
  try {
    const config = zDaemonConfigFile.parse(JSON.parse(readFileSync(configPath, 'utf8')))
    const details = [
      ...(config.machine_id === undefined ? [] : [`machine ${config.machine_id}`]),
      ...(config.api_url === undefined ? [] : [`API ${config.api_url}`]),
    ]
    return details.length === 0 ? configPath : `${configPath} (${details.join(', ')})`
  } catch {
    return configPath
  }
}

function describeExistingInstallation(home: string): string | undefined {
  const configPath = join(home, daemonConfigFileName)
  if (existsSync(configPath)) return describeDaemonConfig(configPath)
  if (!existsSync(home)) return undefined
  const leftovers = readdirSync(home).filter((name) => name !== installLockFileName)
  return leftovers.length === 0 ? undefined : `${home} already contains ${leftovers.join(', ')}`
}

function nearestExistingAncestor(path: string): string {
  const parent = dirname(path)
  return existsSync(path) || parent === path ? path : nearestExistingAncestor(parent)
}

function ensureDaemonHomeWritable(home: string): void {
  const target = nearestExistingAncestor(home)
  try {
    accessSync(target, constants.W_OK)
  } catch {
    throw new CliInputError(`omnarad cannot be installed because ${target} is not writable`)
  }
}

class DaemonExitError extends Error {}

export async function runMachineCreateLocal(context: MachineSetupContext): Promise<void> {
  const { report, apiUrl } = context
  ensureSupportedPlatform(process.platform, process.arch)
  ensureDaemonApiUrl(apiUrl)
  const home = daemonHome()
  const existing = describeExistingInstallation(home)
  if (existing !== undefined) {
    report.warn(`omnarad is already installed on this computer: ${existing}`)
    report.info(
      [
        'Nothing was created. Check the daemon with: omnarad status, or start it with: omnarad restart',
        'To connect this computer as a new machine, run: omnarad uninstall, then re-run this command',
      ].join('\n'),
    )
    throw new CliInputError('omnarad is already installed on this machine')
  }
  ensureDaemonHomeWritable(home)
  const { machine, token } = await connectMachine(context)
  report.info(`Machine ID: ${machine.id}`)
  report.start('Installing omnarad on this machine')
  try {
    await runInstaller(report, apiUrl, token, machine.id, home)
  } catch (error) {
    if (error instanceof DaemonExitError) {
      report.fail('omnarad exited after it was installed')
      report.info(
        [
          `omnarad is installed and registered as machine ${machine.id}; nothing needs to be reinstalled.`,
          'Check the daemon with: omnarad status, or launch it again with: omnarad start',
        ].join('\n'),
      )
      throw error
    }
    report.fail('omnarad installation failed')
    report.info(`Machine daemon token (shown only once):\n${token}`)
    report.url(
      'Finish setup by running this, then paste the machine token when prompted',
      installCommand(apiUrl),
    )
    throw error
  }
  report.done()
}

function daemonGuidance(machineId: string): string {
  return [
    'omnarad is connected to Omnara as this machine.',
    'Manage the daemon with: omnarad status | stop | restart | uninstall',
    `Let agents in a project use this machine with: omnara grant machines add --machine-id ${machineId}`,
    'Learn more: https://docs.omnara.com/machines/connect',
  ].join('\n')
}

const foregroundGuidance = [
  'No launchd/systemd user service manager is available, so omnarad is running in the foreground.',
  'Its logs stream below; press Ctrl-C to stop the daemon, and run omnarad start to launch it again.',
].join('\n')

const serviceManagerCommands = new Set(['launchd', 'systemd'])

interface ParentProcess {
  pid: number
  command: string
}

function parentProcess(pid: number): ParentProcess | undefined {
  if (process.platform === 'linux') {
    try {
      const stat = readFileSync(`/proc/${pid}/stat`, 'utf8')
      const fields = stat.slice(stat.lastIndexOf(')') + 2).split(' ')
      const ppid = Number(fields[1])
      const parentStat = readFileSync(`/proc/${ppid}/stat`, 'utf8')
      const command = parentStat.slice(parentStat.indexOf('(') + 1, parentStat.lastIndexOf(')'))
      return Number.isInteger(ppid) && ppid > 0 ? { pid: ppid, command } : undefined
    } catch {
      return undefined
    }
  }
  const result = spawnSync('ps', ['-o', 'ppid=', '-p', String(pid)], { encoding: 'utf8' })
  const ppid = Number(result.stdout.trim())
  if (result.status !== 0 || !Number.isInteger(ppid) || ppid <= 0) return undefined
  const parent = spawnSync('ps', ['-o', 'comm=', '-p', String(ppid)], { encoding: 'utf8' })
  return { pid: ppid, command: basename(parent.stdout.trim()) }
}

type DaemonSupervision = 'foreground' | 'service'

export function classifyDaemonSupervision(
  daemonPid: number,
  installerPid: number,
  lookupParent: (pid: number) => ParentProcess | undefined,
): DaemonSupervision | undefined {
  let current = daemonPid
  for (let depth = 0; depth < 16; depth += 1) {
    const parent = lookupParent(current)
    if (parent === undefined) return undefined
    if (parent.pid === installerPid) return 'foreground'
    if (parent.pid === 1 || serviceManagerCommands.has(parent.command)) return 'service'
    current = parent.pid
  }
  return undefined
}

function readDaemonLockPid(home: string): number | undefined {
  try {
    const pid = Number(readFileSync(join(home, daemonLockFileName), 'utf8').trim())
    return Number.isInteger(pid) && pid > 0 ? pid : undefined
  } catch {
    return undefined
  }
}

function installerScript(apiUrl: string): string {
  return [
    'installer=$(mktemp)',
    'trap \'rm -f "$installer"\' EXIT',
    'curl -qfsSL --connect-timeout 10 -m 60 --max-redirs 5 --max-filesize 1048576' +
      " --proto '=http,https' --proto-redir '=https'" +
      ` -o "$installer" ${shellQuote(installScriptUrl(apiUrl))}`,
    'sh "$installer"',
  ].join(' && ')
}

function installerFailure(code: number | null, signal: string | null, log: string): Error {
  const reason = signal === null ? `exit code ${code}` : `signal ${signal}`
  return new Error(`omnarad installer failed (${reason})${log === '' ? '' : `\n${log}`}`)
}

function runInstaller(
  report: FlowReporter,
  apiUrl: string,
  token: string,
  machineId: string,
  home: string,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn('/bin/sh', ['-c', installerScript(apiUrl)], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, OMNARA_API_URL: apiUrl, OMNARA_MACHINE_TOKEN: token },
    })
    const relay = (signal: NodeJS.Signals) => {
      child.kill(signal)
    }
    process.on('SIGINT', relay)
    process.on('SIGTERM', relay)
    const buffered: (readonly [NodeJS.WriteStream, Buffer])[] = []
    let transcript = ''
    let supervision: DaemonSupervision | undefined
    const enterForeground = () => {
      supervision = 'foreground'
      report.stop('omnarad installed')
      report.info(daemonGuidance(machineId))
      report.info(foregroundGuidance)
      for (const [target, data] of buffered) target.write(data)
    }
    const lockWatch = setInterval(() => {
      if (supervision !== undefined || child.pid === undefined) return
      const daemonPid = readDaemonLockPid(home)
      if (daemonPid === undefined) return
      const detected = classifyDaemonSupervision(daemonPid, child.pid, parentProcess)
      if (detected === 'foreground') enterForeground()
      else if (detected === 'service') supervision = 'service'
    }, daemonLockPollMs)
    const settle = (outcome: () => void) => {
      clearInterval(lockWatch)
      process.off('SIGINT', relay)
      process.off('SIGTERM', relay)
      outcome()
    }

    const consume = (stream: NodeJS.WriteStream) => (chunk: Buffer) => {
      if (supervision === 'foreground') {
        stream.write(chunk)
        return
      }
      buffered.push([stream, chunk])
      transcript += chunk.toString()
    }
    child.stdout.on('data', consume(process.stdout))
    child.stderr.on('data', consume(process.stderr))

    child.on('error', (error) => {
      settle(() => {
        reject(new Error(`could not run the omnarad installer: ${error.message}`))
      })
    })
    child.on('close', (code, signal) => {
      settle(() => {
        const foreground = supervision === 'foreground'
        if (foreground && (code === 0 || signal === 'SIGINT')) {
          report.info('omnarad stopped')
          resolve()
        } else if (foreground) {
          const reason = signal === null ? `exit code ${code}` : `signal ${signal}`
          reject(new DaemonExitError(`omnarad exited while running in the foreground (${reason})`))
        } else if (code === 0) {
          report.stop('omnarad installed and started as a background service')
          for (const [target, data] of buffered) target.write(data)
          report.info(daemonGuidance(machineId))
          resolve()
        } else {
          reject(installerFailure(code, signal, transcript.trim()))
        }
      })
    })
  })
}

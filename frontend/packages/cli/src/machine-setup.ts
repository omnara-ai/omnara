import { spawn } from 'node:child_process'
import { accessSync, constants, existsSync, readdirSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'

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

function installCommand(baseUrl: string): string {
  return `curl -fsSL ${shellQuote(`${baseUrl}/install/omnarad.sh`)} | sh`
}

export const formatMachineSetup: OutputFormat<ConnectByoMachineResponse> = (
  response,
  { baseUrl },
) => ({
  value: {
    ...response,
    install_command: installCommand(baseUrl),
    install_note: 'Run install_command on the target machine and paste the token when prompted.',
  },
})

const daemonConfigFileName = 'daemon.json'
const installLockFileName = 'install.lock'

const zDaemonConfigFile = z.looseObject({
  machine_id: z.string().optional(),
  api_url: z.string().optional(),
})

function daemonHome(): string {
  return process.env.OMNARA_HOME ?? join(homedir(), '.omnarad')
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

export async function runMachineCreateLocal(context: MachineSetupContext): Promise<void> {
  const { report, baseUrl } = context
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
    await runInstaller(report, baseUrl, token, machine.id)
  } catch (error) {
    report.fail('omnarad installation failed')
    report.info(`Machine daemon token (shown only once):\n${token}`)
    report.url(
      'Finish setup by running this, then paste the machine token when prompted',
      installCommand(baseUrl),
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

const noServiceMarker = 'no launchd/systemd user service manager is available'

function installerScript(baseUrl: string): string {
  return [
    'installer=$(mktemp)',
    'trap \'rm -f "$installer"\' EXIT',
    'curl -qfsSL --connect-timeout 10 -m 60 --max-redirs 5 --max-filesize 1048576' +
      " --proto '=http,https' --proto-redir '=https'" +
      ` -o "$installer" ${shellQuote(`${baseUrl}/install/omnarad.sh`)}`,
    'sh "$installer"',
  ].join(' && ')
}

function installerFailure(code: number | null, signal: string | null, log: string): Error {
  const reason = signal === null ? `exit code ${code}` : `signal ${signal}`
  return new Error(`omnarad installer failed (${reason})${log === '' ? '' : `\n${log}`}`)
}

function runInstaller(
  report: FlowReporter,
  baseUrl: string,
  token: string,
  machineId: string,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn('/bin/sh', ['-c', installerScript(baseUrl)], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, OMNARA_API_URL: baseUrl, OMNARA_MACHINE_TOKEN: token },
    })
    const relay = (signal: NodeJS.Signals) => {
      child.kill(signal)
    }
    process.on('SIGINT', relay)
    process.on('SIGTERM', relay)
    const settle = (outcome: () => void) => {
      process.off('SIGINT', relay)
      process.off('SIGTERM', relay)
      outcome()
    }

    const buffered: (readonly [NodeJS.WriteStream, Buffer])[] = []
    let transcript = ''
    let foreground = false
    const consume = (stream: NodeJS.WriteStream) => (chunk: Buffer) => {
      if (foreground) {
        stream.write(chunk)
        return
      }
      buffered.push([stream, chunk])
      transcript += chunk.toString()
      if (!transcript.includes(noServiceMarker)) return
      foreground = true
      report.stop('omnarad installed')
      report.info(daemonGuidance(machineId))
      report.info(foregroundGuidance)
      for (const [target, data] of buffered) target.write(data)
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
        if (foreground && (code === 0 || signal === 'SIGINT')) {
          report.info('omnarad stopped')
          resolve()
        } else if (code === 0) {
          report.stop('omnarad installed and started as a background service')
          report.info(daemonGuidance(machineId))
          resolve()
        } else {
          reject(installerFailure(code, signal, foreground ? '' : transcript.trim()))
        }
      })
    })
  })
}

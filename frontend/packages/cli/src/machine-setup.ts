import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

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
  let data: ConnectByoMachineResponse
  try {
    ;({ data } = await sdk.connectByoMachine({ client, path, body }))
  } catch (error) {
    report.fail('could not create machine')
    throw error
  }
  report.stop(`Machine created: ${data.machine.id}`)
  if (data.project_grants.length > 0) {
    report.info(`Granted to ${data.project_grants.length} project(s)`)
  }
  return data
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
    install_command: `${installCommand(baseUrl)}  (paste the token when prompted)`,
  },
})

const daemonConfigFileName = 'daemon.json'
const daemonWritabilityProbeFileName = '.omnara-write-check'

const zDaemonConfigFile = z.looseObject({
  machine_id: z.string().optional(),
  api_url: z.string().optional(),
})

function resolveDaemonHome(): string {
  return process.env.OMNARA_HOME ?? join(homedir(), '.omnarad')
}

function describeExistingDaemon(home: string): string | undefined {
  const configPath = join(home, daemonConfigFileName)
  if (!existsSync(configPath)) return undefined
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

function ensureDaemonHomeWritable(home: string): void {
  const probePath = join(home, daemonWritabilityProbeFileName)
  try {
    mkdirSync(home, { recursive: true })
    writeFileSync(probePath, '')
    rmSync(probePath, { force: true })
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    throw new CliInputError(`cannot write to ${home}, so omnarad cannot be installed: ${reason}`)
  }
}

export async function runMachineCreateLocal(context: MachineSetupContext): Promise<void> {
  const { report, baseUrl } = context
  const home = resolveDaemonHome()
  const existing = describeExistingDaemon(home)
  if (existing !== undefined) {
    report.warn(`omnarad is already configured on this computer: ${existing}`)
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
  ].join('\n')
}

const NO_SERVICE_MARKER = 'no launchd/systemd user service manager is available'

function runInstaller(
  report: FlowReporter,
  baseUrl: string,
  token: string,
  machineId: string,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const script = [
      'installer=$(mktemp)',
      'trap \'rm -f "$installer"\' EXIT',
      'curl -qfsSL --connect-timeout 10 -m 60 --max-redirs 5 --max-filesize 1048576' +
        " --proto '=http,https' --proto-redir '=https'" +
        ` -o "$installer" ${shellQuote(`${baseUrl}/install/omnarad.sh`)}`,
      'sh "$installer"',
    ].join(' && ')
    const child = spawn('/bin/sh', ['-c', script], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, OMNARA_API_URL: baseUrl, OMNARA_MACHINE_TOKEN: token },
    })
    const captured: Buffer[] = []
    let foreground = false
    const enterForeground = () => {
      foreground = true
      report.stop('omnarad installed')
      report.info(daemonGuidance(machineId))
      report.info(
        'No launchd/systemd user service manager is available, so omnarad was started in the foreground.\n' +
          'Its logs stream below; press Ctrl-C to stop the daemon, and run omnarad start to launch it again.',
      )
      process.stdout.write(Buffer.concat(captured))
      captured.length = 0
    }
    const consume = (stream: NodeJS.WriteStream) => (chunk: Buffer) => {
      if (foreground) {
        stream.write(chunk)
        return
      }
      captured.push(chunk)
      if (Buffer.concat(captured).toString().includes(NO_SERVICE_MARKER)) enterForeground()
    }
    child.stdout.on('data', consume(process.stdout))
    child.stderr.on('data', consume(process.stderr))
    child.on('error', (error) => {
      reject(new Error(`could not run the omnarad installer: ${error.message}`))
    })
    child.on('exit', (code, signal) => {
      const cleanStop =
        code === 0 || (code === null && (signal === 'SIGINT' || signal === 'SIGTERM'))
      if (foreground && cleanStop) {
        report.info('omnarad stopped')
        resolve()
      } else if (!foreground && code === 0) {
        report.stop('omnarad installed and started as a background service')
        report.info(daemonGuidance(machineId))
        resolve()
      } else {
        const reason = signal === null ? `exit code ${code}` : `signal ${signal}`
        const log = Buffer.concat(captured).toString().trim()
        reject(new Error(`omnarad installer failed (${reason})${log === '' ? '' : `\n${log}`}`))
      }
    })
  })
}

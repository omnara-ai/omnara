import { spawn } from 'node:child_process'

import { type Machine, sdk } from '@omnara/sdk'
import { zCreateMachineRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'

export const zMachineSetupBody = zCreateMachineRequest.extend({
  token_name: z.string().optional().describe('name for the machine daemon token'),
})

type MachineSetupBody = z.output<typeof zMachineSetupBody>
type MachineSetupContext = FlowContext<{ orgID: string }, MachineSetupBody>

interface MachineWithToken {
  machine: Machine
  token: string
}

async function createMachineWithDaemonToken(
  context: MachineSetupContext,
): Promise<MachineWithToken> {
  const { client, path, body, report } = context
  const { token_name, ...machineBody } = body
  report.start('Creating machine')
  const { data: machine } = await sdk.createMachine({ client, path, body: machineBody })
  report.stop(`Machine created: ${machine.id}`)
  report.start('Creating machine daemon token')
  let created
  try {
    ;({ data: created } = await sdk.createByoMachineDaemonToken({
      client,
      path: { orgID: path.orgID, machineID: machine.id },
      body: { name: token_name },
    }))
  } catch (error) {
    report.fail(`could not create a daemon token for machine ${machine.id}`)
    throw error
  }
  report.stop('Machine daemon token created')
  return { machine, token: created.token }
}

function installCommand(baseUrl: string): string {
  return `curl -fsSL ${baseUrl}/install/omnarad.sh | sh`
}

export async function runMachineCreate(context: MachineSetupContext): Promise<void> {
  const { report, baseUrl } = context
  const { machine, token } = await createMachineWithDaemonToken(context)
  report.info(`Machine ID: ${machine.id}`)
  report.info(`Machine daemon token (shown only once):\n${token}`)
  report.url(
    'Run this on the machine, then paste the machine token when prompted',
    installCommand(baseUrl),
  )
  report.done()
}

export async function runMachineCreateLocal(context: MachineSetupContext): Promise<void> {
  const { report, baseUrl } = context
  const { machine, token } = await createMachineWithDaemonToken(context)
  report.info(`Machine ID: ${machine.id}`)
  report.start('Installing omnarad on this machine')
  try {
    await runInstaller(baseUrl, token)
  } catch (error) {
    report.fail('omnarad installation failed')
    report.info(`Machine daemon token (shown only once):\n${token}`)
    report.url(
      'Finish setup by running this, then paste the machine token when prompted',
      installCommand(baseUrl),
    )
    throw error
  }
  report.stop('omnarad installed and running')
  report.info(
    [
      'omnarad is installed and connected to Omnara as this machine.',
      'Manage the daemon with: omnarad status | stop | restart | uninstall',
      `Let agents in a project use this machine with: omnara grant machines add --machine-id ${machine.id}`,
    ].join('\n'),
  )
  report.done()
}

function runInstaller(baseUrl: string, token: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const script = [
      'installer=$(mktemp)',
      'trap \'rm -f "$installer"\' EXIT',
      'curl -fsSL "$OMNARA_API_URL/install/omnarad.sh" -o "$installer"',
      'sh "$installer"',
    ].join(' && ')
    const child = spawn('/bin/sh', ['-c', script], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, OMNARA_API_URL: baseUrl, OMNARA_MACHINE_TOKEN: token },
    })
    const output: Buffer[] = []
    child.stdout.on('data', (chunk: Buffer) => output.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => output.push(chunk))
    child.on('error', (error) => {
      reject(new Error(`could not run the omnarad installer: ${error.message}`))
    })
    child.on('exit', (code, signal) => {
      if (code === 0) resolve()
      else {
        const reason = signal === null ? `exit code ${code}` : `signal ${signal}`
        const log = Buffer.concat(output).toString().trim()
        reject(new Error(`omnarad installer failed (${reason})${log === '' ? '' : `\n${log}`}`))
      }
    })
  })
}

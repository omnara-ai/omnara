import { spawn } from 'node:child_process'

function browserCommand(url: string): {
  command: string
  args: string[]
  windowsVerbatimArguments: boolean
} {
  if (process.platform === 'darwin') {
    return { command: 'open', args: [url], windowsVerbatimArguments: false }
  }
  if (process.platform === 'win32') {
    return {
      command: 'cmd',
      args: ['/c', 'start', '""', `"${url}"`],
      windowsVerbatimArguments: true,
    }
  }
  return { command: 'xdg-open', args: [url], windowsVerbatimArguments: false }
}

export function openInBrowser(url: string, onError: (message: string) => void): void {
  const { command, args, windowsVerbatimArguments } = browserCommand(url)
  const child = spawn(command, args, { stdio: 'ignore', detached: true, windowsVerbatimArguments })
  child.once('error', () => {
    onError('could not open a browser; use the URL above')
  })
  child.unref()
}

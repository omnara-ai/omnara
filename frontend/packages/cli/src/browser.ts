import { spawn } from 'node:child_process'

function browserCommand(url: string): { command: string; args: string[] } {
  if (process.platform === 'darwin') return { command: 'open', args: [url] }
  if (process.platform === 'win32') return { command: 'cmd', args: ['/c', 'start', '', url] }
  return { command: 'xdg-open', args: [url] }
}

export function openInBrowser(url: string, onError: (message: string) => void): void {
  const { command, args } = browserCommand(url)
  const child = spawn(command, args, { stdio: 'ignore', detached: true })
  child.once('error', () => {
    onError('could not open a browser; use the URL above')
  })
  child.unref()
}

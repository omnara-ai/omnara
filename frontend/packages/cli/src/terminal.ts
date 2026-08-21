import readlineCursor from 'node:readline'
import readline from 'node:readline/promises'

export const ansi = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  gray: '\x1b[90m',
  cyan: '\x1b[36m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  magenta: '\x1b[35m',
  red: '\x1b[31m',
}

export function label(name: string, color: string): string {
  return `${ansi.bold}${color}${name}${ansi.reset}`
}

export function terminalColumns(): number {
  const columns: number | undefined = process.stdout.columns
  return Number.isInteger(columns) && columns > 0 ? columns : 80
}

export class Terminal {
  private currentRl: readline.Interface | undefined
  private queued: string[] = []
  private locked = false
  private lastBlank = true
  private status: string | undefined
  private preview: string | undefined
  private renderedRows = 0

  private eraseRegion(): void {
    readlineCursor.cursorTo(process.stdout, 0)
    readlineCursor.clearLine(process.stdout, 0)
    for (let row = 0; row < this.renderedRows; row++) {
      readlineCursor.moveCursor(process.stdout, 0, -1)
      readlineCursor.clearLine(process.stdout, 0)
    }
    this.renderedRows = 0
  }

  private renderRegion(): void {
    if (this.preview != null) {
      console.log(this.preview)
      this.renderedRows += 1
    }
    if (this.status != null) {
      console.log(this.status)
      this.renderedRows += 1
    }
    console.log('')
    this.renderedRows += 1
    this.currentRl?.prompt(true)
  }

  print(text: string): void {
    if (this.locked) {
      this.queued.push(text)
      return
    }
    this.eraseRegion()
    console.log(text)
    this.lastBlank = text.trim() === ''
    this.renderRegion()
  }

  setStatus(text: string | undefined): void {
    if (this.locked) {
      this.status = text
      return
    }
    if (text === this.status) return
    this.eraseRegion()
    this.status = text
    this.renderRegion()
  }

  setPreview(text: string | undefined): void {
    if (this.locked) {
      this.preview = text
      return
    }
    if (text === this.preview) return
    this.eraseRegion()
    this.preview = text
    this.renderRegion()
  }

  repaint(): void {
    if (this.locked) {
      this.currentRl?.prompt(true)
      return
    }
    this.eraseRegion()
    this.renderRegion()
  }

  finishRead(): void {
    const staleRows = this.renderedRows
    this.renderedRows = 0
    if (staleRows > 0) {
      readlineCursor.cursorTo(process.stdout, 0)
      readlineCursor.moveCursor(process.stdout, 0, -(staleRows + 1))
      for (let row = 0; row < staleRows; row++) {
        readlineCursor.clearLine(process.stdout, 0)
        readlineCursor.moveCursor(process.stdout, 0, 1)
      }
      readlineCursor.moveCursor(process.stdout, 0, 1)
      readlineCursor.cursorTo(process.stdout, 0)
    }
    console.log('')
    this.lastBlank = true
  }

  blankLine(): void {
    if (this.locked) {
      const lastQueued = this.queued.at(-1)
      const queuedBlank = lastQueued === undefined ? this.lastBlank : lastQueued.trim() === ''
      if (!queuedBlank) this.queued.push('')
      return
    }
    if (!this.lastBlank) this.print('')
  }

  printBlock(text: string): void {
    this.blankLine()
    this.print(text)
  }

  printNow(text: string): void {
    if (!this.locked) {
      this.print(text)
      return
    }
    console.log(text)
    this.lastBlank = text.trim() === ''
  }

  blankLineNow(): void {
    if (!this.lastBlank) this.printNow('')
  }

  lock(): void {
    this.eraseRegion()
    this.locked = true
  }

  unlock(): void {
    this.locked = false
    const queued = this.queued
    this.queued = []
    for (const text of queued) this.print(text)
  }

  attach(rl: readline.Interface): void {
    this.currentRl = rl
  }

  detach(): void {
    this.currentRl = undefined
  }
}

export interface ReadResult {
  line?: string
  interruptedInput?: string
  closed?: boolean
}

export function readLineOnce(
  terminal: Terminal,
  prompt: string,
  initial: string,
  interrupt: AbortSignal,
): Promise<ReadResult> {
  return new Promise((resolve) => {
    const rl = readline.createInterface({ input: process.stdin, output: process.stdout, prompt })
    terminal.attach(rl)
    let settled = false
    const settle = (result: ReadResult) => {
      if (settled) return
      settled = true
      interrupt.removeEventListener('abort', onInterrupt)
      if (result.line != null) terminal.finishRead()
      terminal.detach()
      rl.close()
      resolve(result)
    }
    const onInterrupt = () => {
      settle({ interruptedInput: rl.line })
    }
    interrupt.addEventListener('abort', onInterrupt)
    rl.once('line', (line) => {
      settle({ line })
    })
    rl.once('close', () => {
      settle({ closed: true })
    })
    rl.once('SIGINT', () => {
      settle({ closed: true })
    })
    terminal.repaint()
    if (initial !== '') rl.write(initial)
    if (interrupt.aborted) settle({ interruptedInput: rl.line })
  })
}

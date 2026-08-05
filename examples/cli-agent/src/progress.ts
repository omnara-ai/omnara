const out = process.stderr
let activeName: string | undefined

export function activeStep(): string | undefined {
  return activeName
}

export function progress(name: string, status: string): void {
  activeName = name
  if (out.isTTY) {
    out.write(`\r\x1b[2K${name}: \x1b[2m${status}\x1b[0m`)
  } else {
    out.write(`${name}: ${status}\n`)
  }
}

export function complete(name: string, result: string): void {
  activeName = undefined
  if (out.isTTY) {
    out.write(`\r\x1b[2K${name}: ${result}\n`)
  } else {
    out.write(`${name}: ${result}\n`)
  }
}

export function interruptForError(): void {
  if (out.isTTY && activeName != null) out.write('\n')
}

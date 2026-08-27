import readline from 'node:readline'

import { ansi } from './terminal.ts'

export interface SelectItem {
  label: string
  hint?: string
}

interface PickOptions {
  multiple?: boolean
}

function renderItem(
  item: SelectItem,
  active: boolean,
  selected: boolean,
  multiple: boolean,
): string {
  const pointer = active ? `${ansi.cyan}❯${ansi.reset}` : ' '
  const marker = multiple ? (selected ? '[x] ' : '[ ] ') : ''
  const text = active ? `${ansi.cyan}${item.label}${ansi.reset}` : item.label
  const hint = item.hint != null ? ` ${ansi.dim}${item.hint}${ansi.reset}` : ''
  return `${pointer} ${marker}${text}${hint}`
}

export async function pick(
  title: string,
  items: SelectItem[],
  options: PickOptions = {},
): Promise<number[]> {
  if (items.length === 0) throw new Error('nothing to select from')
  const multiple = options.multiple === true
  const out = process.stderr
  const selected = new Set<number>()
  let active = 0

  const lineCount = items.length + 2
  const paint = (first: boolean) => {
    if (!first) out.write(`\x1b[${lineCount}A`)
    out.write(`\r\x1b[2K${title}\n`)
    items.forEach((item, index) => {
      out.write(`\r\x1b[2K${renderItem(item, index === active, selected.has(index), multiple)}\n`)
    })
    out.write(
      `\r\x1b[2K${ansi.dim}${multiple ? '↑/↓ move · space toggle · enter confirm' : '↑/↓ move · enter confirm'}${ansi.reset}\n`,
    )
  }

  readline.emitKeypressEvents(process.stdin)
  const wasRaw = process.stdin.isRaw
  process.stdin.setRawMode(true)
  process.stdin.resume()
  out.write(ansi.hideCursor)
  paint(true)

  try {
    return await new Promise<number[]>((resolve) => {
      const onKeypress = (
        _: string | undefined,
        key: { name?: string; ctrl?: boolean } | undefined,
      ) => {
        if (key == null) return
        if (key.ctrl === true && key.name === 'c') {
          out.write(ansi.showCursor)
          process.exit(130)
        }
        switch (key.name) {
          case 'up':
          case 'k':
            active = (active - 1 + items.length) % items.length
            paint(false)
            return
          case 'down':
          case 'j':
            active = (active + 1) % items.length
            paint(false)
            return
          case 'space':
            if (multiple) {
              if (selected.has(active)) selected.delete(active)
              else selected.add(active)
              paint(false)
            }
            return
          case 'return':
          case 'enter': {
            if (multiple && selected.size === 0) selected.add(active)
            process.stdin.off('keypress', onKeypress)
            resolve(multiple ? [...selected].sort((a, b) => a - b) : [active])
            return
          }
          default:
            return
        }
      }
      process.stdin.on('keypress', onKeypress)
    })
  } finally {
    out.write(`\x1b[${lineCount}A\x1b[J`)
    out.write(ansi.showCursor)
    process.stdin.setRawMode(wasRaw)
  }
}

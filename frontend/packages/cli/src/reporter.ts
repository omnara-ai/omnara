import { intro, log, note, outro, spinner } from '@clack/prompts'

import { canPromptInteractively } from './interactive.ts'

export interface FlowReporter {
  url(title: string, href: string): void
  start(message: string): void
  stop(message: string): void
  fail(message: string): void
  warn(message: string): void
  info(message: string): void
  done(): void
}

function interactiveReporter(title: string): FlowReporter {
  intro(title)
  const spin = spinner()
  let spinning = false
  return {
    url(label, href) {
      note(href, label)
    },
    start(message) {
      spin.start(message)
      spinning = true
    },
    stop(message) {
      if (!spinning) return
      spin.stop(message)
      spinning = false
    },
    fail(message) {
      if (!spinning) return
      spin.error(message)
      spinning = false
    },
    warn(message) {
      log.warn(message)
    },
    info(message) {
      log.info(message)
    },
    done() {
      outro('Done')
    },
  }
}

function plainReporter(): FlowReporter {
  return {
    url(label, href) {
      console.log(`${label}: ${href}`)
    },
    start(message) {
      console.log(`${message}...`)
    },
    stop(message) {
      console.log(`${message}.`)
    },
    fail(message) {
      console.error(message)
    },
    warn(message) {
      console.error(`warning: ${message}`)
    },
    info(message) {
      console.log(message)
    },
    done() {
      console.log('Done')
    },
  }
}

export function createFlowReporter(title: string): FlowReporter {
  return canPromptInteractively() ? interactiveReporter(title) : plainReporter()
}

#!/usr/bin/env node
// Writes the guide's agent config from the template (agent.yaml) and your settings
// (guide.config.json): the product name, the model, where the product knowledge comes
// from (see lib-knowledge.mjs), and whether "Do it for me" is allowed.
//
//   node scripts/render-agent.mjs [--config guide.config.json] [--out guide.agent.yaml]
//
// Paths inside the config file are relative to the config file.

import { readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { renderKnowledge } from './lib-knowledge.mjs'

const here = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
const arg = (name, fallback) => {
  const i = process.argv.indexOf(`--${name}`)
  return i > 0 ? process.argv[i + 1] : fallback
}
const configPath = path.resolve(arg('config', 'guide.config.json'))
const outPath = path.resolve(arg('out', 'guide.agent.yaml'))

const config = JSON.parse(await readFile(configPath, 'utf8'))
for (const key of ['product', 'model_provider', 'model_name']) {
  if (!config[key]) throw new Error(`${path.basename(configPath)} needs "${key}"`)
}
let template = await readFile(path.join(here, 'agent.yaml'), 'utf8')

const doMode = config.do_mode !== false
const modeRules = doMode
  ? [
      'Mode "do": use click and fill to move through the flow for the user. The final',
      'action that saves, sends, submits, approves, pays, signs or deletes must use',
      'submit, which asks the user to approve it first. Never use click for those.',
      'Buttons that only open a form or wizard (for example "New invoice" on a list',
      'page) are ordinary clicks. A checkbox that gives consent or authorization',
      '("I authorize…", "I agree…") is the user\'s to tick: point at it with show_step,',
      'even in "do" mode.',
    ].join('\n')
  : [
      'You only show; you never act on the page yourself. If the user asks you to do',
      'something for them, say that you can walk them through it, and do that.',
    ].join('\n')
// Show-only: the agent gets no tools for clicking, typing or submitting.
if (!doMode) template = template.replace(/^ *# do-mode:start[\s\S]*?# do-mode:end\n/m, '')

const values = { ...config, mode_rules: modeRules, ...(await renderKnowledge(config, path.dirname(configPath))) }
const out = template.replace(/\{\{(\w+)\}\}/g, (match, key, offset) => {
  // {{user}} stays: the server fills it in for each user when it makes their profile.
  if (key === 'user') return match
  if (!(key in values)) throw new Error(`the template needs "${key}", which ${path.basename(configPath)} doesn't set`)
  const value = String(values[key])
  if (!value.includes('\n')) return value
  // Continuation lines get the indent of the line the placeholder sits on. A numbered
  // item ("  4. {{mode_rules}}") continues under its text, not under the number.
  const lineStart = template.lastIndexOf('\n', offset) + 1
  const indent = ' '.repeat(offset - lineStart)
  const lead = template.slice(lineStart, offset)
  return value.split('\n').join(`\n${/^\s*$/.test(lead) ? lead : indent}`)
})

await writeFile(outPath, out)
console.log(`wrote ${path.relative(process.cwd(), outPath)} (${doMode ? 'show and do' : 'show only'}, knowledge: ${config.knowledge?.source ?? 'none'})`)

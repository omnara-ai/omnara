/** @vitest-environment happy-dom */

import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { findOnPage, pageSnapshot } from './page-snapshot'

// happy-dom has no layout: give every element a box and treat only [hidden] as not rendered.
beforeEach(() => {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 10, 10))
  Element.prototype.checkVisibility = function checkVisibility(this: Element) {
    return this.closest('[hidden]') == null && getComputedStyle(this).display !== 'contents'
  }
})
afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

function show(html: string) {
  document.body.innerHTML = html
  const fields = document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('[data-typed]')
  for (const field of fields) field.value = field.dataset.typed ?? ''
  return pageSnapshot()
}

it('lists what is on the page in reading order, without the assistant itself', () => {
  const snapshot = show(`
    <aside><a href="/agents" aria-current="page">Agents</a></aside>
    <main>
      <h1>Agents</h1>
      <p>Launch and manage agents.</p>
      <button class="bg-primary" disabled>New agent</button>
      <button class="bg-destructive">Delete</button>
      <div style="display: contents"><button role="tab" aria-selected="true">Active</button></div>
      <table>
        <thead><tr><th>Name</th><th>Status</th></tr></thead>
        <tbody><tr><td>Reviewer</td><td>running</td></tr></tbody>
      </table>
      <section hidden><h2>Collapsed</h2></section>
      <dl><dt>Tokens</dt><dd>29.3K</dd><dt>Model calls</dt><dd>7</dd></dl>
      <div role="slider" aria-label="Tokens per day by model" aria-valuetext="Nov 22: 29.3K">
        <div aria-hidden="true">bars</div>
      </div>
      <p role="status">Saved</p>
    </main>
    <div role="tooltip">New agent</div>
    <div data-assistant-surface>
      <dialog open aria-label="Assistant"><p>Earlier answer</p></dialog>
      <button>Assistant</button>
    </div>`)
  expect(snapshot.split('\n').slice(2)).toEqual([
    '-- sidebar',
    'link "Agents" current href=/agents',
    '-- end sidebar',
    '-- main',
    '# Agents',
    'text: Launch and manage agents.',
    'button "New agent" primary disabled',
    'button "Delete" destructive',
    'tab "Active" selected',
    'table: Name | Status (1 rows)',
    '  row: Reviewer | running',
    'details: Tokens: 29.3K; Model calls: 7',
    'slider "Tokens per day by model" value="Nov 22: 29.3K"',
    'notice: Saved',
    '-- end main',
  ])
})

it('names fields and editors but never sends what was typed into them', () => {
  const snapshot = show(`
    <main>
      <label for="name">Secret name</label>
      <input id="name" data-typed="prod-key" />
      <input id="token" aria-label="API token" readonly data-typed="omn_live_secret" />
      <p>Notes <textarea data-typed="typed notes">default notes</textarea></p>
      <div>Config <div contenteditable="true" aria-label="Description">typed description</div></div>
      <div class="monaco-editor" aria-label="Agent config"><div>model: typed-yaml</div></div>
      <label><input type="checkbox" checked /> Share with project</label>
    </main>`)
  expect(snapshot).toContain('textbox "Secret name"')
  expect(snapshot).toContain('textbox "API token"')
  expect(snapshot).toContain('textbox "Description"')
  expect(snapshot).toContain('textbox "Agent config"')
  expect(snapshot).toContain('checkbox "Share with project" checked')
  expect(snapshot).not.toMatch(/prod-key|omn_live_secret|typed|default notes/)
})

it('reads only the open modal dialog, since the page behind it is blocked', () => {
  const snapshot = show(`
    <main><h1>Members</h1><button>Invite</button></main>
    <div role="dialog" aria-modal="true" aria-label="Invite member">
      <h2>Invite member</h2>
      <button>Send invite</button>
    </div>`)
  expect(snapshot).toContain('Open dialog: "Invite member"')
  expect(snapshot).toContain('button "Send invite"')
  expect(snapshot).not.toContain('Members')
})

it('finds what a name from the outline refers to, preferring an exact control over a heading', () => {
  document.body.innerHTML = `
    <main>
      <h1 id="title">Models</h1>
      <a id="link" href="/models">Models</a>
      <button id="new">New model</button>
      <button id="provider">New model provider</button>
      <table><tbody><tr id="row"><td>gpt-6.1-terra</td><td>prod-openai</td></tr></tbody></table>
    </main>
    <div data-assistant-surface><button id="own">New model</button></div>`
  expect(findOnPage('New model')?.id).toBe('new')
  expect(findOnPage('button "New model" primary')?.id).toBe('new')
  expect(findOnPage('models')?.id).toBe('link')
  expect(findOnPage('heading "Models"')?.id).toBe('title')
  expect(findOnPage('new model prov')?.id).toBe('provider')
  expect(findOnPage('gpt-6.1-terra')?.id).toBe('row')
  expect(findOnPage('Delete everything')).toBeNull()
})

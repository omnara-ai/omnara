import { act } from 'react'

import { button } from '@/test/secret-editor'

async function settle(action: () => void) {
  await act(async () => {
    action()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

/** The options the open ProjectShareChips menu offers, by label. */
export function projectOptions() {
  return [...document.querySelectorAll('[role="option"]')].map((option) => option.textContent)
}

/** Opens ProjectShareChips' add menu, optionally typing into its search box. */
export async function openProjectMenu(search?: string) {
  if (!document.querySelector('input[aria-label="Search projects…"]')) {
    await settle(() => {
      button('Add project').click()
    })
  }
  if (search === undefined) return
  await settle(() => {
    const input = document.querySelector('input[aria-label="Search projects…"]')
    if (!(input instanceof HTMLInputElement)) throw new Error('Missing project search')
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, search)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/** Picks a project from ProjectShareChips' add menu, then closes the menu. */
export async function addProject(name: string) {
  await openProjectMenu()
  await settle(() => {
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
      (candidate) => candidate.textContent === name,
    )
    if (!option) throw new Error(`Missing project: ${name}`)
    option.click()
  })
  await closeProjectMenu()
}

/** Closes ProjectShareChips' add menu by toggling its trigger. */
export async function closeProjectMenu() {
  await settle(() => {
    button('Add project').click()
  })
  if (document.querySelector('input[aria-label="Search projects…"]')) {
    throw new Error('Project menu stayed open')
  }
}

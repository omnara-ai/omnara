import { act } from 'react'
import { expect, vi } from 'vitest'

export function field(labelText: string) {
  const label = [...document.querySelectorAll('label')].find(
    (item) => item.textContent === labelText,
  )
  const element = label
    ? document.getElementById(label.htmlFor)
    : [...document.querySelectorAll('input, textarea')].find(
        (item) => item.getAttribute('aria-label') === labelText,
      )
  if (!(element instanceof HTMLInputElement) && !(element instanceof HTMLTextAreaElement))
    throw new Error(`Missing field: ${labelText}`)
  return element
}

export function button(name: string) {
  const element = [...document.querySelectorAll('button')].find(
    (item) => (item.getAttribute('aria-label') ?? item.textContent.trim()) === name,
  )
  if (!element) throw new Error(`Missing button: ${name}`)
  return element
}

export async function enter(label: string, value: string) {
  await act(async () => {
    const element = field(label)
    const prototype =
      element instanceof HTMLTextAreaElement
        ? HTMLTextAreaElement.prototype
        : HTMLInputElement.prototype
    const descriptor = Object.getOwnPropertyDescriptor(prototype, 'value')
    if (!descriptor?.set) throw new Error('Missing native field setter')
    descriptor.set.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
    await Promise.resolve()
  })
}

export async function waitForUI(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => {
      await Promise.resolve()
    })
    assertion()
  })
}

export async function submit() {
  const form = document.querySelector('form')
  if (!form) throw new Error('Missing secret form')
  act(() => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    if (!form.isConnected) return
    expect(form.querySelector('fieldset')?.disabled).toBe(false)
  })
}

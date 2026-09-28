/** @vitest-environment happy-dom */
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { installTranslationGuard } from '@/lib/translation-guard'

function translatedParagraph() {
  const paragraph = document.createElement('p')
  const label = document.createElement('span')
  const text = document.createTextNode(' per 1M tokens')
  paragraph.append(label, text)
  const translated = document.createElement('span')
  translated.textContent = '每100万个代币'
  paragraph.replaceChild(translated, text)
  return { paragraph, label, text, translated }
}

describe('installTranslationGuard', () => {
  let uninstall: () => void

  beforeEach(() => {
    uninstall = installTranslationGuard(window)
  })

  afterEach(() => {
    uninstall()
  })

  it('skips removing a node a translator already replaced', () => {
    const { paragraph, label, text, translated } = translatedParagraph()

    expect(paragraph.removeChild(text)).toBe(text)
    expect([...paragraph.childNodes]).toEqual([label, translated])
  })

  it('appends when inserting before a node a translator already replaced', () => {
    const { paragraph, label, text, translated } = translatedParagraph()
    const icon = document.createElement('svg')

    expect(paragraph.insertBefore(icon, text)).toBe(icon)
    expect([...paragraph.childNodes]).toEqual([label, translated, icon])
  })

  it('keeps normal removals and insertions', () => {
    const { paragraph, label, translated } = translatedParagraph()
    const icon = document.createElement('svg')

    paragraph.insertBefore(icon, translated)
    paragraph.removeChild(label)

    expect([...paragraph.childNodes]).toEqual([icon, translated])
  })

  it('restores the native methods when uninstalled', () => {
    uninstall()
    const { paragraph, text } = translatedParagraph()

    expect(() => paragraph.removeChild(text)).toThrow()
    uninstall = installTranslationGuard(window)
  })
})

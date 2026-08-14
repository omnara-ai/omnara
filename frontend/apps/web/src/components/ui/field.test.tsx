import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { CheckboxField } from './field'

describe('CheckboxField', () => {
  it('exposes its label and description through separate accessibility relationships', () => {
    const markup = renderToStaticMarkup(
      <CheckboxField
        id="runtime-protection"
        label="Runtime protection"
        description="Delete abandoned sandboxes."
        aria-describedby="external-description"
      />,
    )

    const labelEnd = markup.indexOf('</label>')
    const descriptionStart = markup.indexOf('id="runtime-protection-description"')

    expect(labelEnd).toBeGreaterThan(-1)
    expect(descriptionStart).toBeGreaterThan(labelEnd)
    expect(markup).toContain('for="runtime-protection"')
    expect(markup).toContain(
      'aria-describedby="external-description runtime-protection-description"',
    )
  })
})

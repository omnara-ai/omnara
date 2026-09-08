import type * as Monaco from 'monaco-editor'

export function editorAppearance(monaco: typeof Monaco, element: HTMLElement) {
  const rootStyle = getComputedStyle(document.documentElement)
  const textStyle = getComputedStyle(element)
  const dark = document.documentElement.classList.contains('dark')
  const mode = dark ? 'dark' : 'light'
  const theme = `omnara-${mode}`
  // Monaco's theme API takes hex colors rather than CSS custom properties.
  const color = (token: string) =>
    `#${rootStyle
      .getPropertyValue(token)
      .trim()
      .split(/\s+/)
      .map((channel) => Number(channel).toString(16).padStart(2, '0'))
      .join('')}`
  const foreground = color(`--palette-${mode}-fg`)
  const primary = color('--palette-primary')

  monaco.editor.defineTheme(theme, {
    base: dark ? 'vs-dark' : 'vs',
    inherit: true,
    rules: [],
    colors: {
      'editor.background': color(`--palette-${mode}-surface`),
      'editor.foreground': foreground,
      'editorLineNumber.foreground': `${foreground}80`,
      'editorLineNumber.activeForeground': foreground,
      'editorCursor.foreground': color(`--palette-${mode}-accent`),
      'editor.selectionBackground': `${primary}40`,
      'editor.inactiveSelectionBackground': `${primary}20`,
      'editor.lineHighlightBackground': `${primary}10`,
      'editorWidget.background': color(`--palette-${mode}-surface-2`),
      'editorWidget.border': color(`--palette-${mode}-line`),
    },
  })

  return {
    theme,
    fontFamily: textStyle.fontFamily,
    fontSize: Number.parseFloat(textStyle.fontSize),
    lineHeight: Number.parseFloat(textStyle.lineHeight),
  }
}

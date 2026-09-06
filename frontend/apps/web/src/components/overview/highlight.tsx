import { use } from 'react'
import type { DecorationItem, HighlighterCore } from 'shiki/core'

export type CodeLanguage = 'shell' | 'typescript' | 'json' | 'text'

const shikiLanguage = {
  shell: 'bash',
  typescript: 'typescript',
  json: 'json',
} satisfies Record<Exclude<CodeLanguage, 'text'>, string>

let highlighterPromise: Promise<HighlighterCore> | undefined

function loadHighlighter() {
  highlighterPromise ??= Promise.all([
    import('shiki/core'),
    import('shiki/engine/javascript'),
    import('shiki/langs'),
    import('shiki/themes'),
  ]).then(([core, engine, langs, themes]) =>
    core.createHighlighterCore({
      themes: [themes.bundledThemes['github-light'], themes.bundledThemes['github-dark']],
      langs: [
        langs.bundledLanguages.bash,
        langs.bundledLanguages.typescript,
        langs.bundledLanguages.json,
      ],
      engine: engine.createJavaScriptRegexEngine(),
    }),
  )
  return highlighterPromise
}

const cliCommandPattern = /^npx omnara(?: [a-z-]+)*/

function cliDecorations(code: string, language: CodeLanguage): DecorationItem[] {
  if (language !== 'shell') return []
  const match = cliCommandPattern.exec(code)
  if (!match) return []
  return [
    {
      start: 0,
      end: match[0].length,
      properties: { class: 'text-foreground font-semibold' },
    },
  ]
}

export function Highlighted({ code, language }: { code: string; language: CodeLanguage }) {
  if (language === 'text') return code
  const highlighter = use(loadHighlighter())
  const html = highlighter.codeToHtml(code, {
    lang: shikiLanguage[language],
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
    structure: 'inline',
    decorations: cliDecorations(code, language),
  })
  return <span dangerouslySetInnerHTML={{ __html: html }} />
}

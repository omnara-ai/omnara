import type { ReactNode } from 'react'

export type CodeLanguage = 'shell' | 'typescript' | 'json' | 'text'

type TokenKind =
  | 'keyword'
  | 'command'
  | 'flag'
  | 'key'
  | 'string'
  | 'number'
  | 'literal'
  | 'variable'
  | 'function'
  | 'comment'
  | 'punctuation'

const tokenClasses: Record<TokenKind, string> = {
  keyword: 'text-violet-700 dark:text-violet-300',
  command: 'text-foreground font-semibold',
  flag: 'text-violet-700 dark:text-violet-300',
  key: 'text-sky-700 dark:text-sky-300',
  string: 'text-emerald-700 dark:text-emerald-300',
  number: 'text-amber-700 dark:text-amber-300',
  literal: 'text-amber-700 dark:text-amber-300',
  variable: 'text-amber-700 dark:text-amber-300',
  function: 'text-sky-700 dark:text-sky-300',
  comment: 'text-muted-foreground/70 italic',
  punctuation: 'text-muted-foreground',
}

interface Rule {
  pattern: RegExp
  kind: TokenKind | ((match: RegExpExecArray) => TokenKind)
  nested?: CodeLanguage
}

const jsonRules: Rule[] = [
  { pattern: /"(?:[^"\\]|\\.)*"(?=\s*:)/y, kind: 'key' },
  { pattern: /"(?:[^"\\]|\\.)*"/y, kind: 'string' },
  { pattern: /-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/y, kind: 'number' },
  { pattern: /\b(?:true|false|null)\b/y, kind: 'literal' },
  { pattern: /[{}[\]:,]/y, kind: 'punctuation' },
]

const shellRules: Rule[] = [
  { pattern: /#[^\n]*/y, kind: 'comment' },
  { pattern: /^(?:npx omnara(?: [a-z-]+)*|curl|export)\b/my, kind: 'command' },
  { pattern: /--?[\w-]+/y, kind: 'flag' },
  { pattern: /'\s*[{[][\s\S]*?[}\]]\s*'/y, kind: 'string', nested: 'json' },
  { pattern: /'(?:[^'\\]|\\.)*'/y, kind: 'string' },
  { pattern: /"(?:[^"\\]|\\.)*"/y, kind: 'string' },
  { pattern: /\$\{?\w+\}?/y, kind: 'variable' },
  { pattern: /\\\n/y, kind: 'punctuation' },
]

const typescriptKeywords =
  /\b(?:import|from|export|const|let|var|await|async|new|return|function|if|else|throw|type|interface|as)\b/y

const typescriptRules: Rule[] = [
  { pattern: /\/\/[^\n]*/y, kind: 'comment' },
  { pattern: typescriptKeywords, kind: 'keyword' },
  { pattern: /'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|`(?:[^`\\]|\\.)*`/y, kind: 'string' },
  { pattern: /\b(?:true|false|null|undefined)\b/y, kind: 'literal' },
  { pattern: /\b\d+(?:\.\d+)?\b/y, kind: 'number' },
  { pattern: /\b[A-Za-z_$][\w$]*(?=\s*:)/y, kind: 'key' },
  { pattern: /\b[A-Za-z_$][\w$]*(?=\()/y, kind: 'function' },
  { pattern: /[{}[\]();,.:=]/y, kind: 'punctuation' },
]

const rulesByLanguage: Record<CodeLanguage, Rule[]> = {
  shell: shellRules,
  typescript: typescriptRules,
  json: jsonRules,
  text: [],
}

function tokenNodes(text: string, language: CodeLanguage, keyPrefix: string): ReactNode[] {
  const rules = rulesByLanguage[language]
  const nodes: ReactNode[] = []
  let plainStart = 0
  let cursor = 0
  const flushPlain = (end: number) => {
    if (end > plainStart) nodes.push(text.slice(plainStart, end))
  }
  while (cursor < text.length) {
    let matched = false
    for (const rule of rules) {
      rule.pattern.lastIndex = cursor
      const match = rule.pattern.exec(text)
      if (match?.index !== cursor || match[0] === '') continue
      flushPlain(cursor)
      const kind = typeof rule.kind === 'function' ? rule.kind(match) : rule.kind
      const key = `${keyPrefix}${cursor}`
      nodes.push(
        rule.nested ? (
          <span key={key} data-token={kind}>
            {tokenNodes(match[0], rule.nested, `${key}-`)}
          </span>
        ) : (
          <span key={key} data-token={kind} className={tokenClasses[kind]}>
            {match[0]}
          </span>
        ),
      )
      cursor += match[0].length
      plainStart = cursor
      matched = true
      break
    }
    if (!matched) cursor += 1
  }
  flushPlain(text.length)
  return nodes
}

export function highlight(text: string, language: CodeLanguage): ReactNode[] {
  return tokenNodes(text, language, '')
}

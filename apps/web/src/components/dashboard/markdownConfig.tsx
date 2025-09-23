import { Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'

// Export remark plugins for better markdown parsing
export const remarkPlugins = [
  remarkGfm,      // GitHub Flavored Markdown (tables, strikethrough, etc.)
  remarkBreaks    // Preserve line breaks
]

// Preprocess markdown to convert Unicode bullets to proper markdown lists
export const preprocessMarkdown = (markdown: string): string => {
  // Convert Unicode bullet points to proper markdown lists
  // This handles cases where text uses • instead of - or *
  let result = markdown.replace(/^([•·▪▫‣⁃])\s+/gm, '- ')

  // Wrap apply_patch style patches in fenced code blocks for diff highlighting
  // Detect blocks between *** Begin Patch and *** End Patch
  result = result.replace(/(^|\n)(\*\*\* Begin Patch[\s\S]*?\*\*\* End Patch)(?=\n|$)/g, (_m, prefix, block) => {
    // If it's already fenced, leave it
    if (/```/.test(block)) return `${prefix}${block}`
    return `${prefix}\n\n\x60\x60\x60diff\n${block}\n\x60\x60\x60\n\n`
  })

  return result
}

export const markdownComponents: Components = {
  // Paragraphs - tighter spacing with preserved whitespace
  p: ({ children }) => <p className="mb-1 last:mb-0 leading-relaxed">{children}</p>,
  
  // Lists - compact spacing between items
  ul: ({ children }) => (
    <ul className="list-disc list-outside ml-6 mb-2 space-y-0.5" style={{ listStyleType: 'disc' }}>
      {children}
    </ul>
  ),
  ol: ({ children }) => (
    <ol className="list-decimal list-outside ml-6 mb-2 space-y-0.5" style={{ listStyleType: 'decimal' }}>
      {children}
    </ol>
  ),
  li: ({ children }) => (
    <li className="text-off-white leading-relaxed" style={{ display: 'list-item' }}>
      {children}
    </li>
  ),
  
  // Headers - improved spacing for better readability
  h1: ({ children }) => <h1 className="text-lg font-semibold mb-3 mt-4 first:mt-0 text-off-white">{children}</h1>,
  h2: ({ children }) => <h2 className="text-base font-semibold mb-2 mt-3 first:mt-0 text-off-white">{children}</h2>,
  h3: ({ children }) => <h3 className="text-base font-medium mb-2 mt-3 first:mt-0 text-off-white">{children}</h3>,
  
  // Code
  code: ({ inline, children, ...props }: any) => {
    // Normalize content to a string
    const raw = Array.isArray(children) ? children.join('') : String(children ?? '')
    const className: string = props?.className || ''

    // Heuristics to detect diff/patch content
    const isDiffLanguage = /\blanguage-(diff|patch)\b/.test(className)
    const looksLikeUnifiedDiff = /^(\+|\-|@@|diff --git|index |\*\*\* Begin Patch|\*\*\* Add File:|\*\*\* Update File:|\*\*\* Delete File:|--- |\+\+\+ )/m.test(raw)
    const isDiffBlock = !inline && (isDiffLanguage || looksLikeUnifiedDiff)

    if (!inline && isDiffBlock) {
      const lines = raw.replace(/\n$/,'').split('\n')

      const renderLine = (line: string, idx: number) => {
        const isHunk = line.startsWith('@@')
        const isAdd = line.startsWith('+') && !line.startsWith('+++')
        const isDel = line.startsWith('-') && !line.startsWith('---')
        const isHeader = (
          line.startsWith('diff --git') ||
          line.startsWith('index ') ||
          line.startsWith('---') ||
          line.startsWith('+++') ||
          line.startsWith('*** Begin Patch') ||
          line.startsWith('*** End Patch') ||
          line.startsWith('*** Add File:') ||
          line.startsWith('*** Update File:') ||
          line.startsWith('*** Delete File:') ||
          line.startsWith('*** Move to: ')
        )

        let cls = 'flex whitespace-pre'
        if (isHunk) {
          cls += ' text-cyan-400 bg-midnight-blue/50 px-2 py-1 my-1 rounded text-xs'
        } else if (isAdd) {
          // Softer additions with tokens
          cls += ' bg-diff-add-bg text-diff-add-text'
        } else if (isDel) {
          // Softer deletions with tokens
          cls += ' bg-diff-del-bg text-diff-del-text'
        } else if (isHeader) {
          cls += ' text-off-white/70'
        } else {
          cls += ' text-off-white/70'
        }

        return (
          <div key={idx} className={cls}>
            {line === '' ? '\u00A0' : line}
          </div>
        )
      }

      return (
        <div className="text-off-white font-mono overflow-x-auto scrollbar-thin scrollbar-transparent">
          <div className="min-w-full w-max">
            {lines.map(renderLine)}
          </div>
        </div>
      )
    }

    // Default rendering
    return inline ? (
      <code className="bg-black/30 text-off-white px-1 py-0.5 rounded text-sm font-mono border border-white/10">
        {children}
      </code>
    ) : (
      <code className="text-off-white">{children}</code>
    )
  },
  pre: ({ children }) => (
    <pre className="bg-black/30 rounded-lg p-3 text-sm text-off-white overflow-x-auto border border-white/10 my-2">
      {children}
    </pre>
  ),
  
  // Tables - enhanced rendering with better visibility
  table: ({ children }) => (
    <div className="overflow-x-auto my-4">
      <table className="min-w-full border-collapse border border-white/40 text-sm bg-black/30 rounded-lg shadow-lg">
        {children}
      </table>
    </div>
  ),
  thead: ({ children }) => (
    <thead className="bg-black/50 border-b-2 border-white/40">
      {children}
    </thead>
  ),
  tbody: ({ children }) => <tbody className="divide-y divide-white/20">{children}</tbody>,
  tr: ({ children }) => <tr className="hover:bg-white/10 transition-colors duration-200">{children}</tr>,
  th: ({ children }) => (
    <th className="border-r border-white/30 px-4 py-3 text-left font-semibold text-white last:border-r-0 bg-black/20">
      {children}
    </th>
  ),
  td: ({ children }) => (
    <td className="border-r border-white/20 px-4 py-3 text-off-white last:border-r-0 align-top">
      {children}
    </td>
  ),
  
  // Horizontal rules
  hr: () => <hr className="border-white/30 my-4" />,
  
  // Text formatting
  strong: ({ children }) => <strong className="font-semibold text-off-white">{children}</strong>,
  em: ({ children }) => <em className="italic text-off-white">{children}</em>,
  
  // Line breaks - remove extra spacing
  br: () => <br />
} 

import '@/components/agents/monacoEnvironment'

import type * as Monaco from 'monaco-editor'
import { use, useEffect, useEffectEvent, useRef } from 'react'

import type { SkillMdProblem } from '@/lib/skill-bundles'
import { cn } from '@/lib/utils'
import { editorAppearance } from '@/styles/editor'

const monacoPromise = Promise.all([
  import('monaco-editor'),
  import('monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution.js'),
]).then(([module]) => module)

export function SkillMdEditor({
  id,
  value,
  onChange,
  readOnly = false,
  problem,
  className,
}: {
  id: string
  value: string
  onChange: (value: string) => void
  readOnly?: boolean
  problem?: SkillMdProblem
  className?: string
}) {
  const monaco = use(monacoPromise)
  const editorElementRef = useRef<HTMLDivElement | null>(null)
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null)
  const modelRef = useRef<Monaco.editor.ITextModel | null>(null)
  const emitChange = useEffectEvent(onChange)
  const initialValueRef = useRef(value)
  const initialReadOnlyRef = useRef(readOnly)

  useEffect(() => {
    if (!editorElementRef.current) return

    const modelUri = monaco.Uri.parse(`file:///skill-md-${id}.md`)
    const model =
      monaco.editor.getModel(modelUri) ??
      monaco.editor.createModel(initialValueRef.current, 'markdown', modelUri)

    modelRef.current = model
    editorRef.current = monaco.editor.create(editorElementRef.current, {
      model,
      ariaLabel: 'SKILL.md',
      automaticLayout: true,
      ...editorAppearance(monaco, editorElementRef.current),
      minimap: { enabled: false },
      padding: { top: 12, bottom: 12 },
      readOnly: initialReadOnlyRef.current,
      scrollBeyondLastLine: false,
      stickyScroll: { enabled: false },
      wordWrap: 'on',
      wrappingIndent: 'same',
    })

    const themeObserver = new MutationObserver(() => {
      if (editorElementRef.current) {
        editorRef.current?.updateOptions(editorAppearance(monaco, editorElementRef.current))
      }
    })
    themeObserver.observe(document.documentElement, {
      attributeFilter: ['class'],
      attributes: true,
    })

    const subscription = model.onDidChangeContent(() => {
      emitChange(model.getValue())
    })

    return () => {
      themeObserver.disconnect()
      subscription.dispose()
      editorRef.current?.dispose()
      editorRef.current = null
      model.dispose()
      modelRef.current = null
    }
  }, [id, monaco])

  useEffect(() => {
    const model = modelRef.current
    if (model && value !== model.getValue()) {
      model.setValue(value)
    }
  }, [value])

  useEffect(() => {
    editorRef.current?.updateOptions({ readOnly })
  }, [readOnly])

  const problemMessage = problem?.message
  const problemStart = problem?.startLine
  const problemEnd = problem?.endLine
  useEffect(() => {
    const model = modelRef.current
    if (!model) return
    if (problemMessage === undefined || problemStart === undefined || problemEnd === undefined) {
      monaco.editor.setModelMarkers(model, 'skill-md', [])
      return
    }
    const endLineNumber = Math.min(problemEnd, model.getLineCount())
    monaco.editor.setModelMarkers(model, 'skill-md', [
      {
        severity: monaco.MarkerSeverity.Error,
        message: problemMessage,
        startLineNumber: Math.min(problemStart, endLineNumber),
        startColumn: 1,
        endLineNumber,
        endColumn: model.getLineMaxColumn(endLineNumber),
      },
    ])
  }, [monaco, problemMessage, problemStart, problemEnd, value])

  return (
    <div
      id={id}
      ref={editorElementRef}
      className={cn(
        'border-input bg-card type-code rounded-control h-80 overflow-hidden border',
        className,
      )}
    />
  )
}

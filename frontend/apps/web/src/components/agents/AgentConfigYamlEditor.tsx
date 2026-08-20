import './monacoEnvironment'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution.js'

import * as monaco from 'monaco-editor'
import { configureMonacoYaml, type MonacoYaml, type MonacoYamlOptions } from 'monaco-yaml'
import { useEffect, useRef } from 'react'

import { cn } from '@/lib/utils'

import agentConfigSchemaSource from '../../../../../../internal/agentconfig/generated/agent_config.schema.json?raw'

const agentConfigSchema = JSON.parse(agentConfigSchemaSource) as Record<string, unknown>
const agentConfigSchemaUri = 'https://omnara.local/schemas/agent-config.schema.json'
const agentConfigYamlFileMatches = ['*.yaml', '*.yml']
let yamlConfiguration: MonacoYaml | undefined

function monacoThemeFromDocument() {
  return document.documentElement.classList.contains('dark') ? 'vs-dark' : 'vs'
}

function yamlOptions(): MonacoYamlOptions {
  return {
    completion: true,
    enableSchemaRequest: false,
    hover: true,
    validate: true,
    format: { enable: true },
    schemas: [
      {
        uri: agentConfigSchemaUri,
        fileMatch: agentConfigYamlFileMatches,
        schema: {
          $id: agentConfigSchemaUri,
          title: 'Omnara Agent Config',
          ...agentConfigSchema,
        },
      },
    ],
  }
}

function configureYaml() {
  const options = yamlOptions()
  if (yamlConfiguration) {
    void yamlConfiguration.update(options)
    return
  }

  yamlConfiguration = configureMonacoYaml(monaco, options)
}

function shouldTriggerSuggestionsForChange(event: monaco.editor.IModelContentChangedEvent) {
  return !event.isFlush && event.changes.some((change) => change.text.includes('\n'))
}

const agentConfigYamlPlaceholder = `instruction: |
  You are a helpful assistant.
model:
  provider_config: <provider-config-name>
  name: <model-name>`

function agentConfigYamlAriaLabel(readOnly: boolean) {
  return readOnly ? 'Config preview (YAML)' : 'Config (YAML)'
}

export interface AgentConfigYamlEditorProps {
  id: string
  value: string
  onChange: (value: string) => void
  readOnly?: boolean
  className?: string
}

export function AgentConfigYamlEditor({
  id,
  value,
  onChange,
  readOnly = false,
  className,
}: AgentConfigYamlEditorProps) {
  const editorElementRef = useRef<HTMLDivElement | null>(null)
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)
  const modelRef = useRef<monaco.editor.ITextModel | null>(null)
  const onChangeRef = useRef(onChange)
  const initialValueRef = useRef(value)
  const initialReadOnlyRef = useRef(readOnly)

  useEffect(() => {
    onChangeRef.current = onChange
  }, [onChange])

  useEffect(() => {
    if (!editorElementRef.current) return

    configureYaml()

    const modelUri = monaco.Uri.parse(`file:///agent-config-${id}.yaml`)
    const model =
      monaco.editor.getModel(modelUri) ??
      monaco.editor.createModel(initialValueRef.current, 'yaml', modelUri)

    modelRef.current = model
    editorRef.current = monaco.editor.create(editorElementRef.current, {
      model,
      ariaLabel: agentConfigYamlAriaLabel(initialReadOnlyRef.current),
      ariaRequired: !initialReadOnlyRef.current,
      automaticLayout: true,
      fontFamily: 'var(--font-mono)',
      fontSize: 12,
      lineHeight: 18,
      minimap: { enabled: false },
      padding: { top: 12, bottom: 12 },
      placeholder: agentConfigYamlPlaceholder,
      quickSuggestions: { comments: false, other: true, strings: true },
      readOnly: initialReadOnlyRef.current,
      scrollBeyondLastLine: false,
      stickyScroll: { enabled: false },
      tabSize: 2,
      theme: monacoThemeFromDocument(),
      wordWrap: 'on',
      wrappingIndent: 'same',
    })

    const themeObserver = new MutationObserver(() => {
      editorRef.current?.updateOptions({ theme: monacoThemeFromDocument() })
    })
    themeObserver.observe(document.documentElement, {
      attributeFilter: ['class'],
      attributes: true,
    })

    const subscription = model.onDidChangeContent((event) => {
      onChangeRef.current(model.getValue())

      const editor = editorRef.current
      if (!editor?.hasTextFocus() || !shouldTriggerSuggestionsForChange(event)) return

      window.requestAnimationFrame(() => {
        if (!editor.hasTextFocus()) return

        editor.trigger('agent-config-yaml-newline', 'editor.action.triggerSuggest', {})
      })
    })

    return () => {
      themeObserver.disconnect()
      subscription.dispose()
      editorRef.current?.dispose()
      editorRef.current = null
      model.dispose()
      modelRef.current = null
    }
  }, [id])

  useEffect(() => {
    const model = modelRef.current
    if (model && value !== model.getValue()) {
      model.setValue(value)
    }
  }, [value])

  useEffect(() => {
    editorRef.current?.updateOptions({
      ariaLabel: agentConfigYamlAriaLabel(readOnly),
      ariaRequired: !readOnly,
      readOnly,
    })
  }, [readOnly])

  return (
    <div
      id={id}
      ref={editorElementRef}
      className={cn(
        'border-input bg-background h-[28rem] overflow-hidden rounded-md border text-xs',
        className,
      )}
    />
  )
}

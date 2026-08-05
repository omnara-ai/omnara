import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'

import YamlWorker from './yaml.worker.js?worker'

function createReadyYamlWorker() {
  const worker = new YamlWorker()

  return new Promise<Worker>((resolve, reject) => {
    const handleMessage = (event: MessageEvent) => {
      const data = event.data as unknown
      if (
        typeof data !== 'object' ||
        data === null ||
        !('type' in data) ||
        data.type !== 'omnara-yaml-worker-ready'
      ) {
        return
      }

      worker.removeEventListener('message', handleMessage)
      worker.removeEventListener('error', handleError)
      resolve(worker)
    }
    const handleError = (event: ErrorEvent) => {
      worker.removeEventListener('message', handleMessage)
      worker.removeEventListener('error', handleError)
      reject(event.error instanceof Error ? event.error : new Error(event.message))
    }

    worker.addEventListener('message', handleMessage)
    worker.addEventListener('error', handleError)
  })
}

const monacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    switch (label) {
      case 'yaml':
        return createReadyYamlWorker()
      case 'editorWorkerService':
      default:
        return new EditorWorker()
    }
  },
}

const monacoGlobal = globalThis as typeof globalThis & {
  MonacoEnvironment?: typeof monacoEnvironment
}

monacoGlobal.MonacoEnvironment = monacoEnvironment
self.MonacoEnvironment = monacoEnvironment

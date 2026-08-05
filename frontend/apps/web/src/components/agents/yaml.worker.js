/* global self */

import 'monaco-yaml/yaml.worker.js'

self.postMessage({ type: 'omnara-yaml-worker-ready' })

import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: '../../../api/openapi/openapi.yaml',
  output: {
    path: 'src/generated',
    postProcess: [],
  },
  plugins: [
    { name: '@hey-api/client-fetch', throwOnError: true },
    { name: '@hey-api/typescript' },
    {
      name: 'zod',
      dates: { offset: true },
      requests: false,
      responses: true,
      definitions: true,
    },
    {
      name: '@hey-api/sdk',
      // paramsStructure 'flat' would give sdk fns single-object params
      // (orgID at top level), but the @tanstack/react-query plugin does not
      // support it (its generated options mis-call the flat functions):
      // https://github.com/hey-api/hey-api/issues/3191. Stay grouped until
      // that is fixed upstream; @omnara/react flattens at the hook layer.
      validator: { response: true },
    },
    '@tanstack/react-query',
  ],
})

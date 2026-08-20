import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: '../../../api/openapi/openapi.yaml',
  output: {
    path: 'src/generated',
    postProcess: [
      {
        name: 'Unicode resource-name lengths',
        command: 'node',
        args: ['scripts/fix-resource-name-zod.mjs', '{{path}}'],
      },
    ],
  },
  plugins: [
    { name: '@hey-api/client-fetch', throwOnError: true },
    { name: '@hey-api/typescript' },
    {
      name: 'zod',
      dates: { offset: true },
      requests: true,
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

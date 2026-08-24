import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: '../../../api/openapi/openapi.yaml',
  output: 'src/generated',
  plugins: [
    { name: '@hey-api/client-fetch', throwOnError: true },
    { name: '@hey-api/typescript' },
    {
      name: 'zod',
      dates: { offset: true },
      requests: true,
      responses: true,
      definitions: true,
      $resolvers: {
        string: (ctx) => {
          if (ctx.schema['x-omnara-unicode-normalization'] !== 'NFC') return

          ctx.chain.current = ctx.nodes.base(ctx)
          const minLength = ctx.nodes.minLength(ctx)
          if (minLength) ctx.chain.current = minLength

          ctx.chain.current = ctx.chain.current.attr('transform').call(
            ctx.$.func()
              .param('value')
              .do(ctx.$.return(ctx.$('value').attr('normalize').call(ctx.$.literal('NFC')))),
          )

          type Expression = Parameters<typeof ctx.$.not>[0]
          const refine = (predicate: Expression, message: string) => {
            ctx.chain.current = ctx.chain.current
              .attr('refine')
              .call(
                ctx.$.func().param('value').do(ctx.$.return(predicate)),
                ctx.$.object().prop('message', ctx.$.literal(message)),
              )
          }
          const regexTest = (pattern: string, value: string) =>
            ctx.$(ctx.$.regexp(pattern, 'u')).attr('test').call(value)
          const not = (value: Expression) => ctx.$.not(value)

          if (ctx.schema.maxLength !== undefined) {
            refine(
              ctx.$.binary(
                ctx.$('Array').attr('from').call('value').attr('length'),
                '<=',
                ctx.$.literal(ctx.schema.maxLength),
              ),
              `Resource name cannot exceed ${ctx.schema.maxLength} Unicode characters`,
            )
          }
          refine(
            not(regexTest('^\\p{White_Space}|\\p{White_Space}$', 'value')),
            'Resource name must not start or end with whitespace',
          )
          refine(
            not(
              regexTest('[\\p{Cc}\\p{Cf}\\p{Cs}\\p{Default_Ignorable_Code_Point}\\u2800]', 'value'),
            ),
            'Resource name contains an unsupported invisible, control, or format character',
          )
          refine(
            not(ctx.$('value').attr('includes').call(ctx.$.literal('\ufffd'))),
            'Resource name contains the Unicode replacement character',
          )
          refine(
            not(
              ctx
                .$('Array')
                .attr('from')
                .call('value')
                .attr('some')
                .call(
                  ctx.$.func()
                    .param('character')
                    .do(
                      ctx.$.return(
                        ctx.$.binary(
                          ctx.$.binary('character', '!==', ctx.$.literal(' ')),
                          '&&',
                          regexTest('\\p{White_Space}', 'character'),
                        ),
                      ),
                    ),
                ),
            ),
            'Resource name may only use ordinary spaces',
          )

          return ctx.chain.current
        },
      },
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

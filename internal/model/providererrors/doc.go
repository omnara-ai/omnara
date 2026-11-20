// Package providererrors interprets native error fields, configured gateway
// extensions and relayed upstream evidence without making recovery decisions.
// Protocol adapters own envelope decoding; execution owns retries.
//
// Provider error contracts used by the adapters:
//
//   - OpenRouter cross-skin error_type fields:
//     https://openrouter.ai/docs/api_reference/errors-and-debugging#typed-error-codes
//   - OpenAI API errors:
//     https://developers.openai.com/api/docs/guides/error-codes#api-errors
//   - Anthropic API errors:
//     https://platform.claude.com/docs/en/api/errors
package providererrors

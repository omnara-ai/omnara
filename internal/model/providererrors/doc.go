// Package providererrors classifies evidence extracted by protocol adapters
// from provider-specific error envelopes.
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

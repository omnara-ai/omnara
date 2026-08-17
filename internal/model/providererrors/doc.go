// Package providererrors normalizes error evidence shared by model protocol
// adapters. Each adapter remains responsible for decoding its provider-specific
// HTTP and streaming envelopes, then passes the ordered structured codes,
// statuses, and message extracted from that wire format to this package.
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

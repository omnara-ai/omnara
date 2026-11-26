# Provider error boundaries

An API format describes the request, response, and stream grammar. It does not
identify the company serving the request or the company that trained the model.
OpenAI-compatible Chat Completions can carry a response from OpenAI, a gateway,
a cloud platform, or a local server.

Omnara keeps these configuration concepts separate:

| Concept | Owner | Meaning |
| --- | --- | --- |
| `api_format` | Protocol adapter | The wire grammar: Responses, Chat Completions, or Messages. |
| `api_variant` | Explicit provider configuration | Supported behavior that extends or changes that grammar. |
| Base URL and endpoint path | Routing | Where the request is sent. A proxy address does not select provider semantics. |
| Authentication | Transport | How the configured endpoint authenticates the request. |
| Provider model slug | Model configuration | The model or deployment requested through that endpoint. |

A new hosting company does not automatically need a new variant. Add a variant
when an integration needs a distinct behavior contract that the existing
format, endpoint, authentication, and model settings cannot express. Conversely,
changing the base URL does not make a native API wire-compatible.

## Error ownership

Protocol adapters decode HTTP bodies, in-band errors, and stream events. They
retain the distinction between an error code, an error type, and an extension
field. A numeric code inside a successful HTTP response can supply the failure
status; it must not replace an actual failing HTTP status.

This package interprets decoded evidence. An adapter constructs native evidence
with `OpenAI(code, type)` or `Anthropic(type, detailCode)`, then calls `Interpret`
with its configured variant. Extension fields remain raw JSON until that
variant is selected, so another provider's metadata cannot break native error
decoding. Malformed extensions are discarded without discarding native fields.
The constructors use their own native vocabularies;
there is no caller-ordered list of unrelated strings or additional provider
identity to keep synchronized.

Forwarded upstream evidence has a separate owner in `upstream.go`. For example,
an Anthropic spend-limit code in OpenRouter's `provider_code` retains its meaning
inside an OpenAI-shaped envelope. Wrapped `raw` details use the same native
vocabularies when the bounded refinement rules below allow them. Exact vendor message recognizers
also work across routes, including messages prefixed by a compatible gateway.
This is not permission to interpret arbitrary metadata as native error codes.

OpenRouter owns the meanings of its limit sources, canonical error types, and
wrapped upstream details. Those fields have different roles: a limit source
identifies a spending constraint, while a canonical error type identifies a
failure category. `openrouter.go` owns that namespace, the spending-limit prose,
and the affordable-token parser. `Interpret` consults it only for the configured
OpenRouter variant. Default and Bedrock configurations ignore those extensions.

Specific evidence wins over generic wrappers. Within OpenRouter, the order is a
known limit source, a detailed native reason, the canonical gateway type, an
upstream code, then native code/type. This preserves Anthropic's spending detail
over a coarse rate-limit category. Transport status and precise upstream prose
then refine generic classifications. See `classify_test.go` and
`evidence_test.go` for the conflicting-evidence cases.

An explicit upstream billing code also refines OpenRouter's broad
`rate_limit_exceeded` category: a spending cap can use HTTP 429 without being a
temporary rate limit. This exception does not replace other specific gateway
types, such as authentication failures.

Wrapped details may refine only a generic effective HTTP 400 error, within a
64 KiB bound. Known authentication/permission failures, unknown canonical types,
spending constraints, and masked server failures block refinement. An unknown
upstream code does not block the accompanying upstream payload. Scalar
diagnostics are allowlisted for retry evidence; raw bodies and flagged input
are excluded.

The result is a `model.ProviderError`. Recovery information, such as a provider's
retry timing or affordable output allowance, is data on that error. Execution
owns whether to retry, the minimum usable output allowance, and attempt limits.
An affordable-token amount extracted from an English sentence remains a
best-effort hint, not a guaranteed provider API field.

## Provider contracts consulted

These examples explain the boundaries, rather than promise support for every
API surface offered by each service. Configuration validation remains the
authority for supported format/variant combinations in Omnara. In particular,
the configured OpenRouter variant currently supports Chat Completions only.

| Service | Relevant distinction | Primary documentation |
| --- | --- | --- |
| OpenAI | Responses and Chat Completions are separate grammars. Structured codes distinguish billing failures from rate limits even when both use HTTP 429. | [Errors](https://developers.openai.com/api/docs/guides/error-codes), [Responses migration](https://developers.openai.com/api/docs/guides/migrate-to-responses) |
| Anthropic | Messages has its own envelope and stream events. `error.details.error_code` can distinguish a tier spending cap from an ordinary rate limit. | [Errors](https://platform.claude.com/docs/en/api/errors), [Spend limits](https://platform.claude.com/docs/en/api/rate-limits) |
| OpenRouter | A gateway exposes multiple API formats. Its canonical `error_type` appears in different locations in each format; `limit_source` is a separate gateway contract. | [Errors](https://openrouter.ai/docs/api_reference/errors-and-debugging), [Limits](https://openrouter.ai/docs/api_reference/limits) |
| Azure OpenAI | The v1 compatibility surface uses OpenAI clients with a resource-specific base URL and deployment name. Authentication and hosting are separate from the wire grammar. | [v1 API](https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle) |
| Google / Vertex | Gemini offers an OpenAI-compatible surface. Native Gemini and Vertex Claude `rawPredict` are different contracts; the latter also changes the path and request body. | [Gemini compatibility](https://ai.google.dev/gemini-api/docs/openai), [Vertex compatibility](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/migrate/openai/overview), [Vertex Claude](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/use-claude) |
| LiteLLM | OpenAI-shaped errors can carry numeric status strings, gateway-specific types, or upstream provider codes. Compatibility does not make LiteLLM metadata an OpenRouter contract. | [Gateway errors](https://docs.litellm.ai/docs/proxy/error_reference), [Exception mapping](https://docs.litellm.ai/docs/exception_mapping) |
| Ollama | Its OpenAI-compatible endpoints are distinct from its native error-string and NDJSON API. Local and cloud deployments also differ in authentication. | [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility), [Native errors](https://docs.ollama.com/api/errors) |
| Amazon Bedrock | OpenAI-compatible and Messages interfaces coexist with native Invoke/Converse APIs. Runtime and Mantle are endpoint surfaces; neither is a wire-format name. | [API compatibility](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html), [Endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html) |

Adding support for another service's native API, or enabling another
format/variant combination, requires checking request construction, streaming,
usage accounting, and configuration validation as well as errors. Error
normalization alone must not silently enable that broader integration.

Existing hostname checks in request builders (for example, which user/safety
identifier field to send) remain a separate request-configuration concern.
They are not used by this interpreter. Moving them requires auditing request
semantics and identifier disclosure, rather than inferring a new error identity
from the URL.

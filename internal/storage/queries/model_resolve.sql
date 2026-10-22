-- name: ResolveModelForUse :one
SELECT  revision.id AS revision_id,
 revision.org_id AS revision_org_id,
 revision.configured_model_id AS revision_configured_model_id,
 revision.model_provider_config_id AS revision_model_provider_config_id,
 revision.provider_model_slug AS revision_provider_model_slug,
 revision.context_window_tokens AS revision_context_window_tokens,
 revision.max_output_tokens AS revision_max_output_tokens,
 revision.default_max_output_tokens AS revision_default_max_output_tokens,
 revision.default_cache_retention AS revision_default_cache_retention,
 revision.supports_tools AS revision_supports_tools,
 revision.supports_reasoning AS revision_supports_reasoning,
 revision.default_reasoning_effort AS revision_default_reasoning_effort,
 revision.supported_reasoning_efforts AS revision_supported_reasoning_efforts,
 revision.input_modalities AS revision_input_modalities,
 revision.output_modalities AS revision_output_modalities,
 revision.api_variant_options AS revision_api_variant_options,
 revision.created_at AS revision_created_at,
 provider.id AS provider_id,
 provider.org_id AS provider_org_id,
 provider.management_kind AS provider_management_kind,
 provider.name AS provider_name,
 provider.api_format AS provider_api_format,
 provider.api_variant AS provider_api_variant,
 provider.base_url AS provider_base_url,
 provider.endpoint_path AS provider_endpoint_path,
 provider.request_timeout_ms AS provider_request_timeout_ms,
 provider.auth_kind AS provider_auth_kind,
 provider.auth_options AS provider_auth_options,
 provider.credential_secret_id AS provider_credential_secret_id,
 provider.deleted_at AS provider_deleted_at,
 provider.created_at AS provider_created_at,
 provider.updated_at AS provider_updated_at,
 provider.idle_timeout_ms AS provider_idle_timeout_ms,
 provider.headers AS provider_headers,
 provider.secret_headers AS provider_secret_headers,CASE WHEN grant_row.id IS NULL THEN 'null'::jsonb ELSE jsonb_build_object('id',grant_row.id,
'org_id',grant_row.org_id,
'project_id',grant_row.project_id,
'configured_model_id',grant_row.configured_model_id,
'context_window_tokens',grant_row.context_window_tokens,
'max_output_tokens',grant_row.max_output_tokens,
'default_max_output_tokens',grant_row.default_max_output_tokens,
'default_cache_retention',grant_row.default_cache_retention,
'supports_tools',grant_row.supports_tools,
'supports_reasoning',grant_row.supports_reasoning,
'default_reasoning_effort',grant_row.default_reasoning_effort,
'supported_reasoning_efforts',grant_row.supported_reasoning_efforts,
'input_modalities',grant_row.input_modalities,
'output_modalities',grant_row.output_modalities,
'created_at',grant_row.created_at,
'updated_at',grant_row.updated_at) END::jsonb AS grant_policy,
 coalesce((
 SELECT jsonb_object_agg(s.id::text,jsonb_build_object('version_id',s.current_version_id,'kind',s.kind))
 FROM secrets s
 WHERE s.org_id=provider.org_id AND s.deleted_at IS NULL AND s.owner_kind='org'
 AND s.management_kind=provider.management_kind
 AND s.id=ANY(ARRAY[provider.credential_secret_id] || ARRAY(
 SELECT secret_header.value::uuid FROM jsonb_each_text(provider.secret_headers) AS secret_header(name,value)))
 ),'{}'::jsonb)::jsonb AS secret_versions
FROM configured_model_revisions revision
JOIN configured_models configured_model ON configured_model.org_id=revision.org_id
 AND configured_model.id=revision.configured_model_id AND configured_model.deleted_at IS NULL
JOIN model_provider_configs provider ON provider.org_id=revision.org_id
 AND provider.id=revision.model_provider_config_id AND provider.deleted_at IS NULL
LEFT JOIN project_model_grants grant_row ON grant_row.org_id=revision.org_id
 AND grant_row.project_id=sqlc.arg(project_id) AND grant_row.configured_model_id=revision.configured_model_id
WHERE revision.org_id=sqlc.arg(org_id) AND revision.id=sqlc.arg(revision_id);

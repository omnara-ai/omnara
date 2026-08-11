-- +goose Up

UPDATE machines
SET metadata = CASE
      WHEN concat_ws(
        '/',
        NULLIF(metadata->'observed_platform'->>'os', ''),
        NULLIF(metadata->'observed_platform'->>'arch', '')
      ) = '' THEN metadata - 'observed_platform'
      ELSE jsonb_set(
        metadata,
        '{observed_platform}',
        to_jsonb(concat_ws(
          '/',
          NULLIF(metadata->'observed_platform'->>'os', ''),
          NULLIF(metadata->'observed_platform'->>'arch', '')
        ))
      )
    END
WHERE jsonb_typeof(metadata->'observed_platform') = 'object';

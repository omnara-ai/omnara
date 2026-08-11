# Approved OpenAPI compatibility exceptions

The bearer-token wire-format cutover is an explicitly approved pre-launch
break. Keep each machine-readable exception exact; do not add broad path,
operation, or response ignores.

POST /api/v1/personal-access-tokens the `token` response's property pattern was changed from `^omnara_pat_[^_]+_[^_]+$` to `^omnara_pat_v1_[0-9A-Za-z]{43}_[0-9A-Za-z]{6}$` for the status `201`
POST /api/v1/orgs/{orgID}/api-keys the `token` response's property pattern was changed from `^omnara_org_[^_]+_[^_]+$` to `^omnara_org_v1_[0-9A-Za-z]{43}_[0-9A-Za-z]{6}$` for the status `201`

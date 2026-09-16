import { reviewInstructions } from './prompt'

interface ReviewProfileInputs {
  publicUrl: string
  repo: string
  prNumber: number
  baseRef: string
  headRef: string
  githubTokenSecretId: string
  reviewTokenSecretId: string
}

// Edit the model, pool, and machine overrides here; prompts live in prompt.ts.
// The worker supplies PR-specific values and secret IDs for each launch.
export function createReviewProfile(input: ReviewProfileInputs) {
  return {
    version: 'v1',
    instruction: reviewInstructions,
    model: {
      provider_config: 'omnara-openrouter',
      name: 'anthropic/claude-fable-5',
    },
    tools: {
      run_command: {
        permission: {
          mode: 'always_allow',
        },
      },
      read_process: {},
      write_process: {
        permission: {
          mode: 'always_allow',
        },
      },
      stop_process: {},
      list_processes: {},
      list_machines: {},
      inspect_machine: {},
      web_fetch: {},
    },
    machine_sources: [
      {
        machine_pool_name: 'default-pool',
        cwd: '/workspace',
        max_machines: 1,
        machine_provider_options_overlay: {
          startup_script: sandboxStartupScript(),
        },
        env_overlay: {
          REVIEW_API_URL: input.publicUrl,
          GITHUB_REPOSITORY: input.repo,
          PR_NUMBER: String(input.prNumber),
          PR_BASE_REF: input.baseRef,
          PR_HEAD_REF: input.headRef,
        },
        secret_env_overlay: {
          GITHUB_TOKEN: input.githubTokenSecretId,
          REVIEW_TOKEN: input.reviewTokenSecretId,
        },
      },
    ],
  }
}

function sandboxStartupScript(): string {
  const commentScript = /* sh */ `#!/bin/sh
# Post review output to GitHub through the review bot worker.
# Requires REVIEW_API_URL and REVIEW_TOKEN in the environment (set on this machine by the bot).
#
#   comment.sh comment <body>                                  general PR comment
#   comment.sh line <path> <line> <body>                       inline comment on the new side of the diff
#   comment.sh review <APPROVE|REQUEST_CHANGES|COMMENT> <body> submit the review verdict
#   comment.sh issue <title> <body>                            open an issue in the repository
#
# Any <body> may be "-" to read it from stdin.
set -eu

: "\${REVIEW_API_URL:?REVIEW_API_URL is not set}"
: "\${REVIEW_TOKEN:?REVIEW_TOKEN is not set}"

usage() { sed -n '2,10p' "$0" | cut -c3-; }

read_body() {
  if [ "$1" = "-" ]; then cat; else printf '%s' "$1"; fi
}

post() {
  printf '%s' "$1" | curl -fsS -X POST "$REVIEW_API_URL/review_comment" \\
    -H "Authorization: Bearer $REVIEW_TOKEN" \\
    -H "Content-Type: application/json" \\
    --data-binary @-
  echo
}

command -v jq >/dev/null 2>&1 || { echo "comment.sh: jq is required" >&2; exit 2; }

cmd="\${1:-help}"
case "$cmd" in
  comment)
    [ $# -eq 2 ] || { usage; exit 1; }
    body=$(read_body "$2")
    post "$(jq -n --arg body "$body" '{kind:"comment", body:$body}')"
    ;;
  line)
    [ $# -eq 4 ] || { usage; exit 1; }
    body=$(read_body "$4")
    post "$(jq -n --arg path "$2" --argjson line "$3" --arg body "$body" '{kind:"line_comment", path:$path, line:$line, body:$body}')"
    ;;
  review)
    [ $# -eq 3 ] || { usage; exit 1; }
    body=$(read_body "$3")
    post "$(jq -n --arg event "$2" --arg body "$body" '{kind:"review", event:$event, body:$body}')"
    ;;
  issue)
    [ $# -eq 3 ] || { usage; exit 1; }
    body=$(read_body "$3")
    post "$(jq -n --arg title "$2" --arg body "$body" '{kind:"issue", title:$title, body:$body}')"
    ;;
  *)
    usage
    ;;
esac
`
  return /* sh */ `#!/bin/sh
set -eu
if ! command -v git >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq git curl jq
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache git curl jq
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q git curl jq
  else
    echo 'Install git, curl and jq in the pool image.' >&2
    exit 1
  fi
fi
mkdir -p /usr/local/bin
cat > /usr/local/bin/comment.sh <<'EOF_COMMENT_SH'
${commentScript.trimEnd()}
EOF_COMMENT_SH
chmod 755 /usr/local/bin/comment.sh
`
}

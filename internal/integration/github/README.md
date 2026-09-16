# GitHub connection setup

This package performs GitHub App setup only. OAuth code exchange is its only
native mutation. It does not mint installation tokens, fetch repository content,
perform channel operations, persist credentials, or create product records.

`NewClient(Config)` accepts resolved App ID, OAuth client ID/secret and RSA private
key. API, authorization, token URL and HTTP client overrides are trusted local test
configuration; never derive them from a request or provider response. Defaults
use GitHub's official endpoints and the repository's public-network HTTP client.

The methods return semantic facts for the HTTP/storage owner:

- `VerifyApp(ctx)` verifies the key and registered App/client identity using an
  RS256 JWT. It returns the actual slug, installation URL, owner and permissions.
- `AuthorizeURL(AuthorizeInput)` constructs user authorization with state and
  optional S256 PKCE; `ExchangeCode(ctx, ExchangeInput)` returns `UserOAuth` with
  only `AccessToken` and `ExpiresAt`. Expiring GitHub App user tokens are required.
- `ListInstallations(ctx, user, page)` and
  `ListRepositories(ctx, user, installationID, page)` expose 100-item pages and
  `NextPage` (zero at end). Empty installation lists are successful results.
- `VerifySelection(ctx, user, installationID, repositoryID, repositoryFullName)`
  performs three fresh reads: App identity, user-authorized repository metadata,
  and that repository's App installation. The full name is an untrusted, validated
  owner/name lookup hint; exact returned IDs and ownership are checked. A rename
  requires refreshing the picker; redirects are never followed. It requires
  the user's `permissions.admin`, installed Pull requests write, Issues read or
  write, and Metadata read. Additional installed permissions do not add behavior.
  The result includes only verified App/installation/repository facts. No
  Administration or Contents permission is required by these endpoints.

For fresh installation, authorize the user with PKCE first, then show the
installation/repository picker. Offer the verified App installation URL in a new
tab when installation is needed; return to the picker and refresh native lists
using the same unexpired temporary user token. Document authorization during
installation **disabled** for this flow. No installation callback or installation
state round trip is needed. An organization approval request may leave the list
empty until approved. The user-token list and final verification, never a browser
installation return, establish access.

If another caller deliberately uses GitHub's authorization-during-installation
flow, `ExchangeInput.CodeVerifier` can be absent when that correlated flow did
not send a PKCE challenge. The caller must pin this choice in its consumed OAuth
state; do not silently fall back after a PKCE exchange fails.

The caller owns single-use OAuth state, authenticated project/App selection,
encrypted temporary user-token storage, and the final local lifecycle/authority
recheck before committing. An OAuth user token is not a runtime bot credential.

Every public I/O operation has one 30-second context bound; responses are capped
at 2 MiB before strict JSON materialization. Selection verification uses a constant three requests, independent of the
number of accessible repositories. Option lists remain explicitly paginated. No retries or redirects occur.
Native error bodies, URLs and transport error text are not returned; `APIError`
retains HTTP status and context cancellation/deadline errors remain identifiable.

REST IDs are parsed as exact integral decimal text. App/install/repository IDs
above `9007199254740991` yield `ErrUnsupportedID` at the setup boundary, matching
the current Octokit runtime's numeric installation-token configuration. They are
never rounded. Repository owner/name/node-ID limits also match the gateway.

Primary references checked 2026-09-15:

- [GitHub App user tokens and PKCE](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
- [Accessible installations and repository permission semantics](https://docs.github.com/en/rest/apps/installations)
- [User-authorized repository metadata](https://docs.github.com/en/rest/repos/repos#get-a-repository)
- [JWT-authenticated App and installation endpoints](https://docs.github.com/en/rest/apps/apps)
- [JWT construction](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)
- [Authorization can precede installation](https://docs.github.com/en/apps/using-github-apps/authorizing-github-apps)
- [Install links and selected repositories](https://docs.github.com/en/apps/using-github-apps/installing-a-github-app-from-a-third-party)
- [Authorization during installation versus setup URL](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration)

Tests use local fake APIs and generated test RSA keys. They do not establish live
GitHub behavior beyond the documented contracts.

# Session Sharing & Teams Plan

## Goals
- Support collaborative agent sessions by letting a user share a session with individuals or teams.
- Track the true human sender of each user-authored message so collaborators can follow the conversation.
- Introduce team management with role-based permissions (owner, admin, member).
- Enforce read/write access levels when reading or posting session messages.

## Domain Overview
- **Session** refers to an `AgentInstance`. The creating user remains the owner.
- **Message** refers to `Message` rows produced by users or the agent runtime.
- Users may have access to sessions via:
  1. Direct ownership (existing behaviour).
  2. A direct share granting `read` or `write` access.
  3. Membership in a team that has been granted `read` or `write` access.
- Direct share permissions take precedence over team permissions when both exist; the most permissive access should be used for enforcement (`write` > `read`).

## Data Model Changes

### Messages
- Add nullable `sender_user_id` (FK to `users.id`) on `messages`.
  - Populate for all USER messages going forward.
  - Historical data: allow NULL and rely on migration to backfill to the session owner where appropriate.
- Update `Message` relationships to link `sender_user` (lazy relationship to `User`).
- Extend factories/utilities (`create_user_message`, streaming serializers, etc.) to include sender info.

### Instance Sharing Tables
- `agent_instance_access`
  - `id` UUID PK
  - `agent_instance_id` FK to `agent_instances`
  - `shared_email` stored exactly as provided (case-insensitive matches handled in code)
  - `user_id` FK to `users`, nullable so we can persist shares for accounts that do not exist yet
  - `access`: enum (`READ`, `WRITE`)
  - `granted_by_user_id` FK to `users`
  - `created_at`, `updated_at`
  - Unique constraint on (`agent_instance_id`, `shared_email`) to avoid duplicate invitations
  - Optional partial unique index on (`agent_instance_id`, `user_id`) for rows where `user_id` is present to simplify joins

### Teams
- `teams`
  - `id` UUID PK
  - `name`
  - `created_at`, `updated_at`
- `team_memberships`
  - `id` UUID PK
  - `team_id` FK to `teams`
  - `user_id` FK to `users`, nullable to allow pending invitations
  - `invited_email` stored as provided when `user_id` is NULL so we can keep a placeholder for users who haven't signed up yet
  - `role`: enum (`OWNER`, `ADMIN`, `MEMBER`)
  - `created_at`, `updated_at`
  - Unique constraint on (`team_id`, `user_id`) for resolved members and (`team_id`, `invited_email`) for email-only placeholders
  - Ensure exactly one owner per team via membership rows (no separate owner column).
- `team_invitations` (optional for v1?) — prefer to skip for now because user will share only with existing accounts.

### Team Instance Access
- `team_instance_access`
  - `id` UUID PK
  - `team_id` FK
  - `agent_instance_id` FK
  - `access`: enum (`READ`, `WRITE`)
  - `granted_by_user_id` FK to `users`
  - `created_at`, `updated_at`
  - Unique constraint on (`team_id`, `agent_instance_id`).

### Enums / Constants
- Add SQLAlchemy/pydantic enums for `InstanceAccessLevel` (`READ`, `WRITE`) and `TeamRole` (`OWNER`, `ADMIN`, `MEMBER`).
- Update application-layer constants and serializers to expose these values.

## Access Control Rules
- **Ownership:** existing owner retains implicit `WRITE` and cannot be removed; only owner can delete the session.
- **Direct Shares:**
  - Only session owner or users with `WRITE` via ownership/team-admin? -> propose: owner & team admins with write access can share; detail below.
  - Access escalation allowed (read→write) but not demote owner.
- **Teams:**
  - Team owner can promote/demote members; admins can add/remove members except the owner; members cannot manage others.
  - Session access for teams can be created/updated by session owner or team admins/owner with sufficient rights.
- **Effective permissions:**
  - Evaluate union of ownership, direct share, and team-based share.
  - Determine `can_read` if any grant >= read.
  - Determine `can_write` if any grant = write.
- **Message Creation:**
  - `create_user_message` must require `WRITE` access and set `sender_user_id`.
- **Message Fetching:**
  - All read endpoints must require `READ` access.
- **Mutation endpoints** (rename session, delete session, etc.) should validate appropriate ownership or `WRITE` rights per feature (rename requires write, delete owner only).

## API Surface Changes

### Session Sharing Endpoints (FastAPI backend)
- `GET /agent-instances/{id}/access` – returns direct shares and team grants with access level + metadata.
- `POST /agent-instances/{id}/share`
  - Payload: `{ "emails": [..]?, "user_ids": [..]?, "access": "read"|"write" }`
  - Only owner (and maybe team admins?) can call.
  - Look up existing users by email when possible, but keep the share even if no account matches yet.
  - When a user record is later created (e.g., first login), a reconciliation helper will match pending shares by email and populate the `user_id` field.
- `PATCH /agent-instances/{id}/share/{share_id}` – change access level.
- `DELETE /agent-instances/{id}/share/{share_id}`.
- `POST /agent-instances/{id}/team-access`
  - Body: `{ "team_id": ..., "access": "read"|"write" }`
- `PATCH/DELETE` for team access similar to direct share.
- Update `/agent-instances` list & detail queries to include sessions shared with the current user.

### Team Management Endpoints
- `GET /teams` – list teams for user (owned or joined).
- `POST /teams` – create team (creates membership record as OWNER).
- `PATCH /teams/{id}` – rename (owner/admin).
- `DELETE /teams/{id}` – owner only.
- `GET /teams/{id}/members` – list members.
- `POST /teams/{id}/members` – add member by email (owner/admin). Store email-only membership placeholders if the user does not exist yet and link automatically when they join later.
  - Reconciliation on user creation/login fills `user_id` for any email-only placeholders matching that address, enforcing uniqueness from then on.
- `PATCH /teams/{id}/members/{membership_id}` – change role (owner/admin but cannot demote owner).
- `DELETE /teams/{id}/members/{membership_id}` – remove member (owner/admin, cannot remove owner; owner leaving deletes team or transfer?).

### Responses / Serializers
- Extend `MessageResponse` to include `sender_user_id` and basic profile (email/display name) for UI to show who wrote what.
- Provide data models for new sharing/team endpoints.

## Service Layer Updates
- Augment query helpers (existing modules in `shared/` and FastAPI backend) to:
  - Resolve accessible sessions for a user via new joins.
  - Filter message fetch/create by effective permissions.
  - Include sender metadata when serializing messages.
- Introduce dedicated permission helper module (e.g., `shared/services/session_access.py`) to centralize permission checks and share creation updates. The FastAPI backend will consume these helpers; the `servers/` package remains untouched for this phase.

## Frontend / Client Touchpoints (follow-up work)
- Update web/mobile/CLI clients to surface teams & sharing UI, show sender info, gate message composer based on `can_write`.
- Expose effective permission status in API responses so clients can disable compose field for read-only viewers.

## Implementation Phases
1. **Schema Preparation** – add SQLAlchemy models/enums and relationships (no migrations yet).
2. **Permission Utilities** – build helpers to compute effective access and keep them in `shared/` so backend entrypoints can rely on them without modifying the `servers/` package.
3. **Message Sender Tracking** – update message creation/query paths, Pydantic models, and SSE stream payloads to include `sender_user_id`.
4. **Session Share Endpoints** – implement CRUD for direct shares and integrate into existing list/detail queries.
5. **Team Management** – add models, endpoints, and rules for managing teams and linking them to sessions.
6. **Access Enforcement Pass** – audit existing mutations (rename, delete, heartbeat actions) to ensure they respect new permissions.
7. **Tests** – extend database + API tests to cover permission matrix, share/team flows, and message sender visibility.

**TODO (next steps):**
- Expand agent instance listing/polling endpoints to include shared/team visibility.
- Implement direct instance sharing endpoints and reconciliation utilities.
- Backfill historical messages with `sender_user_id` once migrations are ready.

## Open Questions / Assumptions
- Sharing by email does not perform validation; we persist the share regardless of whether a matching user exists yet and reconcile later when the user account appears.
- Session owner can manage all shares and team grants; team admins can manage membership but cannot share sessions they do not own.
- Backfilling historical messages will be handled in a dedicated migration script (user to run separately).
- Last-read tracking stays session-wide for now; per-user read receipts could be a later enhancement.

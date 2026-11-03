---
name: ai-relay-development
description: Develop the CX official AI conversation relay end to end across its Go service, cx_home member authentication, cx_member_new_admin management UI, and CxApp EC client. Use for relay, billing, model/provider/API-key management, JWT, schema, integration, or deployment work.
---

# CX AI Relay Development

## Cross-project architecture

This feature spans four separate codebases with different frameworks and sources of truth. Read [references/repository-map.md](references/repository-map.md) for the architecture, boundaries, and exact pre-work reading checklist. Do not treat the four repositories as one application or place business code in the wrong repository.

### Required before every implementation pass

1. Identify every repository the change will touch. Before editing each one, read its applicable `SKILL.md` and `AGENTS.md` (if present), then its documentation entrypoint and the numbered docs for the affected code. The exact minimum files are listed in `references/repository-map.md`.
2. Inspect current source and `git status` in that repository. Treat all pre-existing edits as user work; preserve them and do not overwrite unrelated changes.
3. Establish the real route, auth state, database, schema, caller, and response contract from source. Do not guess from folder names or this skill when source has changed.
4. If a repository's required skill or documentation cannot be read, stop edits in that repository and report the missing prerequisite. Continue only independent work elsewhere.

The user explicitly requires this read-first workflow for every project. Do not skip it for small changes, SQL work, or follow-up turns.

### Repository architecture summary

- **AI relay (`E:\dev\dotnet\Project\AI中转服务`)**: Go `net/http` service in `cmd/relay` and `internal/relay`; MySQL schema migrations in `migrations`; API, architecture and run docs in `docs`. It connects only to `cx_ec`, owns provider routing/credentials, model catalog and policy, TPM/RPM, wallet/usage/ledger, and OpenAI-compatible chat endpoints. Provider credentials are AES-GCM encrypted.
- **Member PHP (`E:\dev\WWW\cx_home`)**: custom PHP framework with product-specific controllers/models/views in numeric product directories and shared code in `main/`. Product 17 is EC. Existing cookie/session remains authoritative. `17/controller/ai/token.php` issues a short-lived relay JWT from the authenticated session and registers its JTI directly in shared `cx_ec` in an atomic account/wallet/token transaction; it must never take a UID/password as proof of identity.
- **Admin (`E:\dev\WWW\cx_member_new_admin`)**: custom PHP backend for product 12 plus a Vue 2 SPA at `12/view/template/new/new_admin`. The active menu is statically registered in `src/config/router.config.js`; UI visibility is not authorization. AI management is a product-12 authenticated PHP controller; after the admin session check it reuses the framework's existing MySQL connection and switches the schema to `cx_ec`. The Vue app never receives provider credentials except one-time form submission and never stores database/admin secrets.
- **EC client (`E:\dev\dotnet\CxApp`)**: .NET Framework 4.8 WPF/MVVM app. `CxEc` owns AI settings and the conversation UI/client; `CxClient.Member` owns member authentication/session and service protocol. The official relay must fit the existing OpenAI-compatible streaming client while obtaining/refreshing JWT through the authenticated member session. Preserve user-configured custom AI providers.

## Security and billing invariants

- The member PHP service is the sole issuer of user JWTs. It must require the existing EC login session; never accept a UID, cookie, machine code, or account credentials as a substitute for that session.
- JWTs use HS256, a configured shared secret, fixed issuer and audience, short expiry, and a unique token id. Do not log JWTs, cookies, provider keys, or prompts containing credentials.
- Go validates signature, issuer, audience, expiry, and required subject claims before any user operation. Provider credentials stay server-side.
- Billing is based on authoritative upstream usage, model pricing multiplier, and a server-side integer unit scale. Store ledger entries and wallet changes atomically; use an idempotency key per upstream request. Reject requests when the wallet cannot cover the configured reservation. Refund reservations on upstream failure.
- TPM and RPM limits are server-enforced as shared per-model limits across users and relay instances. When a minute window is full, the relay queues the HTTP request until capacity resets or the client cancels; it clamps output reservation to fit TPM and rejects a prompt estimate that cannot fit in any window. Model capability flags (`thinking`, tools) are enforced by the relay even if a client sends unsupported fields.
- Keep OpenAI-compatible request and streaming response behavior compatible with EC's existing `chat/completions` client.
- Database schema target is the existing MySQL/MariaDB database `cx_ec`. Keep the migration in source control; before applying it, confirm the active connection's server/database and inspect collisions. Never print or commit connection credentials. If remote authentication/permissions fail, do not retry with guessed accounts or alternate hosts; leave the SQL ready and report the exact non-secret error.

## Workflow

1. Follow the read-first checklist above for each repository involved.
2. Implement in dependency order: schema → PHP session JWT → Go JWT verification/catalog/chat/billing → secured admin APIs/UI → EC token refresh and relay selection.
3. Keep `docs/API.md`, `docs/architecture.md`, `docs/development.md`, implementation status, and each affected repository's own documentation synchronized with code.
4. Add admin operations only with backend authorization, secret masking, and audit logging; frontend menu visibility is not authorization.
5. Preserve streaming cancellation, upstream timeouts, bounded request/response sizes, and provider error handling.
6. Use the target repository's documented lint/build procedures. Do not deploy, publish, commit, or change production credentials unless the user explicitly requests that action.

## Cross-project publish targets

- `cx_home` member service: the user-designated connection entrypoint is `E:/dev/WWW/cx_home/.vscode/sftp.json`. The authoritative production connection/password-reuse instructions are recorded in `E:/dev/WWW/cx_member_new_admin/.codex/skills/cx-admin-dev/SKILL.md`, section "官网 cx_home 的生产连接（用户指定）"; read that section before publishing and do not ask the user to restate its configuration. Keep passwords in memory, never in skills, source, or logs.
- `cx_member_new_admin`: FTP settings live in its ignored `.env.deploy.local`. It has only the production target (`adminn-open.cxsup.com`, site `cxadminn.cxsup.com`); when the user asks to publish the admin project, publish to production directly. Do not add a sandbox target.
- Never read credentials into terminal output, logs, or tracked files. The project-specific SKILL documents the local publish command and environment field names.

## Local service

The Go service uses the standard `net/http` router and MySQL. Runtime configuration is provided through environment variables; never commit secrets. Read `docs/implementation-status.md` before enabling endpoints or applying migrations. See `docs/development.md` for required variables, schema migration, and local run instructions.

## Shared configuration

- Go defaults to `HTTP_ADDR=:8088`; local client-facing base URL is `http://localhost:8088/v1`.
- Go uses `DATABASE_DSN` to access only `cx_ec`; the admin AI controller reuses its product-12 framework connection and switches schema to `cx_ec`, so it needs no separate `CX_EC_DB_*` settings.
- PHP reads its signing key from the existing `ENCRYPTION_KEY` constant in `config/config.php`; Go `JWT_HS256_SECRET` must contain the same raw string. Optional PHP issuer/audience constants `CX_AI_JWT_ISSUER` and `CX_AI_JWT_AUDIENCE` match Go settings (defaults `cx-member` / `cx-ai-relay`). PHP uses its authenticated framework database connection to register the JTI in `cx_ec`; no remote callback to local Go or internal registration bearer is required. Responses contain only the temporary token, token type, and lifetime.
- PHP JWT lifetime is the configurable constant `CX_AI_JWT_TTL_SECONDS`, default `900` (15 minutes). The accepted range is 60 to 1800 seconds; invalid configuration fails issuance. Go rejects expired tokens and any lifetime above 30 minutes. EC holds only temporary JWTs in memory, checks every 30 seconds while an Eder page is loaded, and renews 60 seconds before expiry; logout/account changes clear cached and in-flight credentials.
- Provider credential encryption must use the same `ENCRYPTION_KEY` bytes in both services: the Go relay reads its runtime setting, while `cx_member_new_admin` reads the PHP constant defined in its local `config/config.php`. EC reads its relay base from `server_info.app_setting` key `AI_SERVICE_BASEURL`.
- Default provider seed is `DeepSeek官方` at `https://api.deepseek.com` (`migrations/002_seed_deepseek_provider.sql`). Model discovery runs server-side against the selected provider's authenticated `/models` endpoint; imported models start disabled with zero prices, and existing model policy must be preserved. Never place a provider API key in source or frontend assets; create its encrypted record through the authenticated admin credential flow.
- Before closing a cross-project feature, verify PHP lint, admin frontend lint/build, EC build, and Go build when their toolchains are available. Report unavailable toolchains and database access failures; never claim an unavailable verification passed.

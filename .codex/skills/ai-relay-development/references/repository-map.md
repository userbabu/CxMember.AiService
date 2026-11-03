# Repository map and read-first checklist

This is a source-guided map for the AI relay integration. Re-check each named file and the current source before edits; the map does not override a repository's own skill or documentation.

## 1. Go relay — `E:\dev\dotnet\Project\AI中转服务`

### Architecture

- `cmd/relay/main.go`: process entry and HTTP server startup.
- `internal/relay/config.go`: environment-based runtime configuration.
- `internal/relay/server.go`: HTTP routes, member/admin middleware, model listing, and chat handler.
- `internal/relay/jwt.go`: HS256 JWT validation and required claims.
- `internal/relay/store.go`: MySQL connection.
- `migrations/001_initial.sql`: initial `ai_*` schema in the existing `cx_ec` database.
- `docs/API.md`, `docs/architecture.md`, `docs/development.md`, `docs/implementation-status.md`: service contract, boundaries, setup and known incomplete work.

### Must read before Go changes

Read this skill, `docs/implementation-status.md`, `docs/architecture.md`, `docs/API.md`, and `docs/development.md`. Inspect the current Go source and migration before changing behavior or schema. Confirm auth, idempotent reservation/settlement, capability enforcement, and the database target before enabling or changing request paths.

## 2. Member PHP — `E:\dev\WWW\cx_home`

### Architecture

This is a custom PHP application, not Laravel or a conventional auto-routed MVC framework. `index.php` and `app_load.php` select product context; `system/` implements routing, controller/model loading, sessions, database and output. Shared controllers/models live in `main/`; product-specific business code belongs in `<puctype>/controller`, `<puctype>/model`, and `<puctype>/view`. Route paths resolve to controller files/methods. `17/` is the EC product area. Session auth uses existing cookies/session IDs and login state; product authorization and device binding are distinct checks that must be verified in the current login flow.

### Must read before member PHP changes

1. `.github/skills/project-architecture/SKILL.md`.
2. `readme.txt`, `index.php`, `app_load.php`, and relevant route/session/auth implementations in `system/`.
3. Existing product 17 controller/model for the target route and caller.
4. `docs/ai-relay.md` for the JWT issuance and shared-database JTI registration contract.
5. For token issuance, inspect `main/controller/account/login.php`, `main/controller/account/account.php` methods that establish/check member session and binds, plus current `17/controller/ai/token.php`.

Keep product 17 logic under `17/`; use `main/` only when code is genuinely shared across products. Do not read out or copy secrets from protected configuration files. PHP reads the existing signing constant `ENCRYPTION_KEY` from protected `config/config.php`; EC reads the public relay URL from `AI_SERVICE_BASEURL`. JTI registration uses the existing authenticated database connection to `cx_ec`.

## 3. Admin PHP + Vue — `E:\dev\WWW\cx_member_new_admin`

### Architecture

The PHP layer is a custom product-12 app. Controllers live in `12/controller`, models in `12/model`; shared routes use `main/`. The backend response contract is `{error_code, code, val}`. Admin session and permission checks are enforced by backend routes. The Vue 2 admin SPA is `12/view/template/new/new_admin`; API wrappers live in `src/api`, pages in `src/views`, and static navigation is defined in `src/config/router.config.js`. Only direct children of `/` are sidebar roots. Frontend permission metadata only filters visible routes and is not an authorization boundary. Product database and member database connections are distinct; do not write AI relay tables through the wrong connection.

### Must read before admin changes

1. `.codex/skills/cx-admin-dev/SKILL.md`.
2. `.codex/skills/cx-admin-dev/references/backend.md` for route, auth and JSON contracts.
3. `references/frontend.md` for Vue API, route/menu, build and component rules.
4. `references/database.md` before schema or database access.
5. Inspect `12/controller/startup/system.php::check_login`, a nearby manager controller, `src/config/router.config.js`, `src/utils/request.js`, and neighboring API/page components.

AI management uses an authenticated product-12 PHP controller. After its normal admin session check, it reuses the framework MySQL connection and selects the `cx_ec` schema. Never ship database credentials, JWT signing key, or provider secret retrieval to the SPA. Provider secrets are write-only; mask them in reads and audit mutations.

## 4. EC client — `E:\dev\dotnet\CxApp`

### Architecture

The solution targets .NET Framework 4.8. `CxEc/CxEc.Main` owns the WPF/MVVM application, settings UI, and agent conversation UI/client. `CxEc/CxEc.Engine` owns collection engine behavior and shared engine settings. `CxClient.Member` owns account session, authorization, and the server protocol used by the EC app. Other `CxClient.*` and `CxLib.*` projects provide UI, browser, localization, proxy and HTTP support. UI strings follow the project localization resources. AI custom-provider settings and the existing streaming/tool-call client are already present in the current tree; inspect current uncommitted changes before editing them.

### Must read before CxApp changes

1. `AGENTS.md`.
2. `.codex/skills/cxec-development/SKILL.md`, `.codex/skills/cxec-general-development/SKILL.md`, and `.codex/skills/cxec-documentation/SKILL.md` for code, generic, and docs changes respectively.
3. `.documents/开始文档.md`, `CxEc/.documents/cxec.main/开始文档.md`, `CxEc/.documents/cxec.main/15.采集助手与工具调用.md`, and `CxEc/.documents/cxec.main/16.Eder AI中转服务.md` for agent/client integration.
4. `.documents/cxclient.member/开始文档.md`, `3.cah 账户身份与授权.md`, `9.Protocol 服务端协议.md`, and `12.账户授权与协议数据模型.md` for auth and token request flow.
5. Inspect actual `SettingAi`, current AI agent client, and CxClient.Member protocol/cookie implementation before selecting how to request or refresh the JWT. Do not assume `HttpClient` automatically shares the member session.

Keep feature logic in the repository/project that owns it. The desktop client calls the PHP token endpoint with the existing authenticated session, then calls Go with the returned short-lived bearer JWT; provider keys never enter the client.

## Shared integration sequence

```text
existing EC login session
  → PHP product-17 JWT endpoint
  → Go validates issuer/audience/signature/expiry/product
  → user-scoped model catalog and OpenAI-compatible chat
  → wallet reservation, upstream usage settlement, idempotent ledger
  → authenticated PHP admin controller directly manages cx_ec and Vue pages
```

Keep the API contract documented in the Go repo and link/update the relevant docs in each touched repository. Build/lint each changed project using its own instructions. Never deploy or execute production schema mutations implicitly. When the user explicitly asks to apply SQL, verify target database `cx_ec`, inspect existing tables, run the versioned migration once, and verify created table names. If database access is denied, preserve the SQL artifact, report the non-secret error, and do not try guessed credentials or alternate hosts.

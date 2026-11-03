# API contract

Base URL is configured by the EC client. All user APIs require `Authorization: Bearer <short-lived member JWT>`.

The local relay route is `http://127.0.0.1:8088/v1` (development may use `http://localhost:8088/v1`). The deployed container mapping remains loopback-only at `127.0.0.1:8088`; this is not a verified public client endpoint. EC must receive the official externally reachable HTTPS base through `AI_SERVICE_BASEURL`. The public reverse proxy and authoritative HTTPS base are not present in this repository, and external/client reachability remains unresolved.

## JSON response envelope

All relay JSON endpoints use the CxWebServer `ResponseJsonFormat` fields: `code`, `msg`, `data`, and `errors`. Success uses `code: 0`, `msg: "操作成功"`, and the endpoint result in `data`; `errors` is `null` when there are no field errors. Failures keep the corresponding non-2xx HTTP status, put that status in `code`, preserve the server's user-facing text verbatim in `msg`, set `data` and `errors` to `null`, and include `request_id`. Unknown routes and method mismatches use the same error shape. The Go client reads `msg` before any protocol-specific error field.

Suspended accounts receive HTTP 403 with `error.type="account_disabled"`. Both `msg` and `error.message` contain `您的账户异常：` followed by the administrator's disable explanation verbatim; an empty explanation uses `该账户已被禁用，暂时无法继续对话。`. The account remains suspended, and this rejection occurs before upstream access or billing.

`POST /v1/chat/completions` is the OpenAI Chat Completions protocol endpoint: successful non-stream responses and SSE chunks retain the OpenAI wire format so existing SDKs and streaming clients continue to work. Its HTTP errors and in-stream error events also include the common `code/msg/data/errors/request_id` fields alongside the OpenAI `error` object; both message fields contain the same server text. CORS preflight and file/stream transport responses are not JSON envelopes.

## OpenAI-compatible endpoints

Backend model administration remains product-12 session authenticated PHP, not a member JWT API. `ai/manage/get_models` accepts `provider_id` (0 for all); `list_provider_models` reads an enabled provider/credential's upstream catalog without inserting models; `sync_models` imports only a nonempty unique string `model_ids` selection (maximum 500), validates it against the current upstream catalog and leaves existing global model IDs unchanged. New models remain disabled with zero customer prices. `delete_models` takes the same string-ID list, verifies and locks the full selection, and atomically deletes model/configuration rows with audit; any missing model fails the entire batch. Historical usage, locked pricing evidence and wallet records are retained. Previews expose existing model/provider names, never credentials.

`save_model` accepts `peak_interval` such as `9:00-12:00` and exposes its normalized form in model lists; cross-midnight intervals are supported. Existing standard prices are the low-period prices. Low, peak and limited periods independently configure cache-hit/non-cache-hit input and output costs and customer prices; period-cost migration 013 is required before the new PHP version.

- `GET /v1/models`: `data` contains the OpenAI model-list object; its `data` array includes only models enabled for the authenticated user/product. Each model includes `model_type` (`text`, `image` or `video`), integer `max_context_tokens`, ascending integer `context_tiers`, and the effective `reasoning_efforts` string array. Its `pricing` object includes `input_credit_per_1k`, `cache_input_credit_per_1k`, and `output_credit_per_1k`. The same model objects appear in `/v1/account/quota` as `pricing_models`.
- `GET /v1/service/config`: read the Eder service setting on every request. Returns `enabled` and `unavailable_message`; this is public operational configuration only and requires the normal member bearer JWT. The server-only global prompt is never included. Missing configuration or database failure returns HTTP 503. The EC client calls it before each Eder conversation turn and stops before chat when disabled or unreadable.
- `POST /v1/client/initialize`: submit `eder_version`, `software_version`, `software_hash` (64-character SHA-256 hex) and `system` after the member authenticates. `data` contains `{available, message}` using the current service switch and exact-version blocklists. This endpoint is also called before each chat; `/v1/chat/completions` requires valid client metadata and independently rechecks service availability and blocklists.
- `GET /v1/account/quota`: `data` contains current account status and the sum of the member and eligible parent wallet balances. `balance_credit` is the total; `self_balance_credit` and `parent_balance_credit` expose the two components. Amounts are exact decimal strings in credit (up to six decimal places), with `unit: "credit"`. A parent without an active AI account/wallet contributes zero; a main account is counted once. `quota_baseline_credit` is the combined post-recharge balance baseline, and `remaining_percent` is the server-calculated remaining percentage or `null` when no baseline exists. `used_percent` remains a separate historical model-consumption/cumulative-recharge statistic.
- `GET /v1/account/usage`: `data` is the recent usage array, including `cached_prompt_tokens` when the upstream reports cache hits.
- `POST /v1/chat/completions`: accept OpenAI Chat Completions JSON. `stream=true` returns SSE frames and `[DONE]`; cancellation closes the upstream request. Usage is included in the final frame when available.

Streaming responses send `X-Accel-Buffering: no` and flush the response headers as soon as the successful upstream response is available. Every validated completion event is flushed individually. Upstream SSE comments produce a fixed `: keep-alive` comment; the original comment text is not exposed. Streaming upstream requests use `Accept-Encoding: identity` to avoid compression buffering. The first content event still depends on the upstream model's generation time, and response headers arriving earlier does not mean the model has generated content.

Chat accepts only models with `model_type="text"`. An available `image` or `video` model returns HTTP 400 before price resolution, wallet reservation, billing or upstream forwarding; an invalid stored type returns HTTP 503. The type is also returned in `/v1/models` and quota `pricing_models`, but does not introduce image/video generation routes. The updated Go startup requires the `ai_model.model_type` column defined by `migrations/008_model_type.sql`: apply 008 when the column is absent, or verify the exact ENUM/nullability/default when it already exists before starting the new service; do not blindly repeat ALTER. Production has verified the existing `ENUM('text','image','video') NOT NULL DEFAULT 'text'` column and two text models, so this release did not rerun the migration. Model-type code is deployed in `cx-ai-relay-local:model-type-20261027`; loopback deployment checks do not resolve the public HTTPS base described above.

Clients may send `Idempotency-Key` (up to 36 characters). The relay returns the selected request id in `X-Request-ID`; duplicate reservation attempts with the same id cannot charge twice. If a provider omits usage, the relay estimates it and applies the same model pricing policy.

The relay removes `ai_usage` records older than 30 days at startup and once every 24 hours. Their matching `ai_usage_pricing` snapshots are removed in the same batch transaction; wallet ledger records remain available as financial records. Apply `migrations/007_usage_retention.sql` before deploying this cleanup.

The relay enforces model capabilities, token bounds, TPM/RPM, wallet balance, provider selection, and billing. TPM/RPM are shared per model across users and relay instances using the `cx_ec` rate-window table. When a model's current one-minute bucket is full, chat requests wait for the next bucket instead of returning HTTP 429; callers may cancel while queued. If the estimated prompt itself exceeds the model TPM, the request is rejected as invalid because waiting cannot make it fit. The relay reduces `max_tokens` when needed so the estimated prompt plus reserved output fits the per-minute TPM. It does not accept a provider API key from the client.

The EC native host sends bearer-authenticated requests; the embedded conversation page uses origin `https://cxec-agent.invalid` for its host bridge and does not send API requests or bearer credentials. The relay permits that exact origin on `GET /v1/service/config`, `POST /v1/client/initialize`, `GET /v1/account/quota` and `POST /v1/chat/completions`, and answers matching CORS preflight with `Authorization` and `Content-Type`; it does not enable credentialed CORS or expose models, usage, or internal token-registration endpoints to the page. Other browser origins do not receive CORS permission.

The Eder service setting is stored in the singleton row of `ai_eder_service_config`, created by `migrations/010_eder_service_config.sql`; version policy adds `blocked_eder_versions` and `blocked_software_versions` in migration 011. Apply 011 before deploying this relay/admin version. The authenticated admin page controls the switch, unavailable message and exact-match version blocklists and writes an audit record. The client initialization sends Eder version, software file version, executable SHA-256 and OS/architecture. A disabled service or matching blocked version returns `available:false` and the configured message; a blank message uses the client's localized upgrade prompt. Chat rechecks the database policy for each request. Blocklist JSON must be an array of at most 200 unique version strings; invalid persisted configuration fails closed with HTTP 503. The relay reads the row directly and does not cache it.

## Used quota and currently available total

`used_credit` and `total_credit` are published in the Go service `cx-ai-relay-current` using `cx-ai-relay-local:quota-details`; the live binary SHA matches the build. Local verification returned health 200, unauthenticated quota 401 and embedded-origin CORS preflight 204. These checks validate the local route and response availability, not public HTTPS exposure; external EC access still depends on the unresolved official `AI_SERVICE_BASEURL` and reverse proxy. No schema migration was required, and `used_percent` remains available with its existing meaning.

`GET /v1/account/quota` also returns:

- `used_credit`: all completed quota deductions from the eligible wallet pool: settled model consumption (`settlement` / `settlement_parent`) plus administrative reductions/clearing (`admin_deduction`). It excludes temporary reservations and releases/refunds.
- `total_credit`: the quota currently available for subsequent requests, equal to `balance_credit`. It is the current member wallet plus the currently eligible parent wallet, not cumulative recharge or a configured quota limit. Pending reservations have already reduced these available balances.

Chat completion usage preserves upstream cache-hit details. `prompt_cache_hit_tokens` (or `prompt_tokens_details.cached_tokens`) is stored as `cached_prompt_tokens` in usage history and appears in member/admin usage records. For configured cache pricing, only these hit tokens use the model's separate cache input price; other prompt tokens use the ordinary input price. Providers without cache usage fields are billed as zero cached tokens.

Both fields are exact credit decimal strings with at most six decimal places. With 2100 credit recharged, 1324.54808 consumed and 775 manually deducted, and no pending reservation, the response includes `"used_credit":"2099.54808"`, `"total_credit":"0.45192"`, and `"balance_credit":"0.45192"`.

They share the existing read-only snapshot and eligible-parent rules; the main wallet is counted once. An inactive/missing parent or a missing parent wallet contributes neither balance nor history. Historical usage paid by the parent belongs to that wallet pool even if initiated by another child. Used totals may exceed Int64 and are formatted with arbitrary-precision integers. Invalid positive administrative deductions, invalid cumulative amounts or database failures return the existing quota HTTP 503 error. No schema migration or desktop/admin change is required.

The existing `used_percent` retains its settled-model-consumption / cumulative-recharge definition below. It is separate from the remaining quota percentage and is not used to calculate the quota gauge.

## Remaining quota percentage

The quota response returns `quota_baseline_credit` as an exact credit string and `remaining_percent` as a JSON number from 0 to 100, rounded half up to two decimal places. The server reads each included wallet's latest `admin_recharge` or `order_recharge` ledger entry and uses its persisted `balance_after_micro` as that wallet's baseline. For an eligible member/parent pool, the per-wallet baselines are summed; a main account is counted once. Successful top-up transactions already write the post-recharge balance to the same ledger row as the balance change, so no new schema column is needed.

`remaining_percent` is current `balance_credit / quota_baseline_credit * 100`. Spending, refunds/releases and administrator deductions do not reset the baseline; the next successful recharge does, including that wallet's remaining balance. A missing baseline is returned as `quota_baseline_credit: "0"` and `remaining_percent: null`. A current wallet balance above its latest recharge baseline is treated as inconsistent data and makes the quota request fail with HTTP 503. The read shares the balance's REPEATABLE READ snapshot, so the baseline and balance cannot come from different concurrent recharge states.

The EC client validates and displays `remaining_percent`; it does not derive the ratio from cumulative `used_credit` or `used_percent`. `used_percent` remains available for its distinct historical meaning and is not the quota gauge.

## Quota usage percentage

`GET /v1/account/quota` adds `used_percent` without changing the existing balance, unit, status or pricing fields. It represents cumulative settled model consumption divided by cumulative recharge, multiplied by 100. The numerator uses negative `settlement` / `settlement_parent` ledger deltas as positive costs; the denominator uses positive `admin_recharge` / `order_recharge` deltas. Reservations, release/refund entries and `admin_deduction` adjustments are excluded from both sides. The result is not calculated from balance changes or the retained 30-day usage list.

The scope matches `balance_credit`: the member's own wallet plus the currently eligible parent wallet. A main account is counted once. A missing or inactive parent account, or missing parent wallet, contributes neither balance nor historical recharge/consumption. It is a percentage for the available wallet pool, not just the current user's recent chat requests.

Balances, parent eligibility and both cumulative ledger totals are read within one read-only REPEATABLE READ transaction. The server reads aggregate totals as decimal integer text and uses arbitrary-precision integer basis-point arithmetic, rounding half up before converting the final value to a JSON number. Values are capped at 100; JSON does not pad trailing decimal zeros. With no recharge, `used_percent` is `null`, never an invented 0%. For example, 1 credit settled out of 3 credit recharged returns `33.33`.

Database/transaction failures, negative or overflowing wallet balances, negative recharge entries, positive settlement entries, or malformed cumulative totals return HTTP 503 through the existing quota error response. The server does not substitute a percentage on failure. Clients can read the field directly; this change adds no desktop display or management-page element and requires no schema migration. The independent usage-retention migration is unaffected.

## Model types

Each enabled model in `GET /v1/models` and quota `pricing_models` carries `model_type`, one of `text`, `image` or `video`. These catalogs expose the configured category; they are not restricted to chat-capable types. The model-management API validates and persists the category and includes it in `model.save` audit details.

`POST /v1/chat/completions` accepts only `text` models. An available `image` or `video` model returns HTTP 400 before price resolution, reservation, billing or upstream forwarding. An invalid stored model type fails as unavailable configuration (HTTP 503); missing types are not assumed to be text at runtime. This change provides classification and a chat boundary, not image/video generation endpoints.

`migrations/008_model_type.sql` adds the non-null ENUM field with default `text`. Existing models and newly imported models initialize as text; administrators must choose the actual category when configuring them. The new Go startup explicitly requires the column before opening the store. Before startup, require the 008 column definition; apply the script only if the column is absent, and verify rather than rerun it when present. Production `cx_ec` already has `ENUM('text','image','video') NOT NULL DEFAULT 'text'` and both models are text, so no further 008 ALTER was needed or executed for this release. Go and the model-management PHP/Vue have been published, with Go running `cx-ai-relay-local:model-type-20261027`; the service does not automatically execute migrations.

## Model context limits

The model policy field `max_context_tokens` is one of 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, or 1048576 tokens (4K–1M; K = 1024). `context_tiers` contains only these tiers up to the configured maximum; it does not expose the provider's credentials or internal mapping. A 128K model returns `max_context_tokens: 131072` and `context_tiers: [4096,8192,16384,32768,65536,131072]`.

Chat JSON is limited to 64 MiB. Go estimates input from the serialized messages, tool definitions and tool choice plus per-message overhead. If the estimate reaches or exceeds the configured context maximum, the request fails with HTTP 400 before rate reservation, billing or upstream forwarding. The relay caps output to the remaining context window, then applies TPM limits. The estimate is not a provider tokenizer count; upstream model limits remain authoritative. Invalid stored context tiers fail the request/catalog rather than using an assumed capacity.

EC uses the authenticated model catalog to bound each model's local context budget. Client-selected budgets do not change the server's model maximum. Apply `migrations/005_model_context.sql` before upgrading Go/PHP; the service does not add columns automatically.

## Model reasoning policy

Each public model's `reasoning_efforts` contains only administrator-enabled tiers from `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`. The administration API saves them in that fixed order; the effective default comes from the service default bounded by that whitelist. `none` means reasoning disabled. If the model's `allow_thinking` master switch is off, public catalogs return `[]` and the relay explicitly disables reasoning upstream. Stored choices are retained for later configuration, but cannot be used while the master switch is off. Invalid stored arrays, duplicate/unknown tiers, unsupported formats, or an enabled switch with an empty array fail as unavailable configuration.

Chat callers select `reasoning_effort` from the model's current public whitelist. An optional `thinking` object must contain exactly `{"type":"enabled"}` or `{"type":"disabled"}`. Malformed values, unauthorized tiers and contradictory toggles/effort fail with HTTP 403 before quota reservation or upstream forwarding. With no whitelist, only reasoning disabled is accepted. A caller omitting both effort and thinking toggle receives the service default bounded by the current whitelist, or `none` for an empty list; an explicit thinking toggle is still checked for consistency and must resolve to an allowed selection.

The server-only model field `reasoning_format` determines upstream serialization. `reasoning_effort` sends the resolved effort explicitly. `deepseek` sends `thinking.type=disabled` for `none`, or `thinking.type=enabled` plus the resolved `reasoning_effort` for an active tier. Incoming thinking JSON is validated, not forwarded unchecked. The format and stored provider settings are not public model fields.

Apply `migrations/006_model_reasoning.sql` after 005 before upgrading Go/PHP. Administrators must enable only tiers that the upstream model actually supports; the relay does not infer model capacity or reasoning capabilities from names.

## Member JWT endpoint (PHP)

`GET /?route=ai/token` (EC product 17). Requires the existing authenticated member session. PHP uses that session's EC UID as the JWT `sub` and registers the short-lived JTI directly in shared `cx_ec` through the existing PHP database connection. Registration atomically creates `ai_account` and a zero-balance `ai_wallet` on first access, keyed by that same UID; repeated registration preserves the account and wallet values. Returns `{token, token_type, expires_in}` in the existing `outputCxJson` envelope. Lifetime defaults to 15 minutes and is configured by PHP constant `CX_AI_JWT_TTL_SECONDS` (60 to 1800 seconds); invalid configuration fails issuance. Go rejects lifetimes above 30 minutes. EC keeps JWTs only in memory, checks every 30 seconds while an Eder page is loaded, and renews 60 seconds before expiry. Responses never contain internal service secrets or provider credentials. Never takes a UID or password in the request body. JWT claims: `iss`, `aud`, `sub`, `parent_uid`, `iat`, `nbf`, `exp`, `jti`, and product scope.

## Management API

The member admin manages providers, provider credentials, models, accounts and wallet balances, and usage directly in `cx_ec`. JWTs are issued from the EC member session and validated by Go; they are not exposed as an admin management feature. After its normal product-12 session check, the PHP controller reuses the existing framework MySQL connection and selects the `cx_ec` schema. Provider credentials are encrypted with AES-GCM before storage.

The admin's model import calls the selected provider's `GET /models` endpoint using its server-side credential. Newly imported models are created disabled with zero prices; administrators must set prices and policy before enabling them. Existing model rows are not overwritten. The official `DeepSeek官方` provider is inserted by migration `migrations/002_seed_deepseek_provider.sql`.

### Administrative wallet deduction

The authenticated product-12 PHP management route `ai/manage/adjust_quota` reduces or clears an existing user's credit wallet. It is not a Go user endpoint and is not authorized by a member JWT. The request uses the existing management envelope and fields:

| Field | Contract |
| --- | --- |
| `uid` | Target existing wallet UID; browser callers preserve it as a string. |
| `operation` | `reduce` or `clear`. |
| `reduce_credit` | Exact credit decimal text, at most six fractional digits; positive for `reduce` and `"0"` for `clear`. |
| `expected_balance_credit` | Exact wallet balance snapshot confirmed by the administrator. |
| `adjustment_id` | UUID v4 retained unchanged for retries of the same operation. |

A successful response's `val` contains `ok`, `duplicate`, `deducted_credit` and `balance_credit`; amounts are exact decimal strings. An idempotent retry returns the original deduction receipt, including its resulting balance, rather than applying the deduction again. Reusing a UUID with a changed UID, operation, amount or confirmed balance fails.

The PHP transaction uses READ COMMITTED and locks only the existing target wallet with `FOR UPDATE`. A missing wallet, negative balance, mismatched confirmation snapshot, insufficient funds, zero balance for clear, or an unfinished `reserve`/`reserve_parent` against that wallet rejects the operation. It never creates an account, draws from a parent wallet, deletes the account or modifies chat usage. Wallet update, negative `admin_deduction` ledger entry and `wallet.deduct` audit commit atomically. Audit details record the request ID, operation, original requested amount/snapshot, actual deduction and balances before/after. Ledger `operator_id` and audit `admin_id` use the authenticated `config.member.uid`; missing or invalid administrator identity aborts and rolls back the operation. Idempotent receipts also verify that audited actual deduction and before/after balances agree with the ledger. No schema migration or Go/client code change is required. The admin account summaries expose `total_deducted_credit` separately from settled model consumption and recharge totals.

## Scheduled pricing

Model management saves independent standard, daily peak, and date-limited promotional input/output prices. Multipliers are not configurable request fields; internal storage coefficients are fixed to 10000. Public prices use `input_credit_per_1k` and `output_credit_per_1k` decimal strings, in credit per 1,000 model tokens. Activity pricing overrides daily peak pricing, which overrides standard pricing. The model IANA time zone defaults to `Asia/Shanghai`; intervals include their start and exclude their end. Go locks one rate before queuing and uses it for reservation and settlement. The admin usage endpoint includes the rate snapshot and UTC lock time; the OpenAI chat response contract is unchanged. Deploy migration `003_model_pricing.sql` before the updated APIs. See [pricing](pricing.md).

## Error responses

The common `msg` is not replaced by a status-based generic message. In particular, a suspended account returns the exact administrator-configured `disabled_message` with `code` equal to the HTTP status and `type: "account_disabled"`; the EC client displays that text and appends the request ID. Model capability failures report their own reason and cannot mask an account-status failure because account status is checked before model lookup, reasoning and tool capability validation.

Internal configuration names, database/transport diagnostics, stack traces, authorization headers, provider secrets, signing material, session cookies and token contents are never returned to callers. Upstream diagnostics remain in server logs; user-facing provider errors use a bounded, credential-redacted message.

金额接口以 credit 十进制字符串返回，不返回原余额 TOKEN 数或旧的倍率字段。模型原始输入/输出用量仍为 token；用量金额为 `charged_credit`。详见 [Credit 计费单位](credit.md)。

## 官网 credit 充值与主账号共享

官网产品 10 支持发货类型 `12`：`{"12":[{"credits":"1000"}]}`，每份金额为最多六位小数的正数，按购买数量相乘，不按人民币价格推算。官网登录账户为默认收款账户；主账号可通过已有 `pay_for` 表单指定直属子账号。下单与发货都校验账号归属。充值与官网发货记录在同一 MySQL 连接事务中提交，写入 `cx_ec.ai_account`、`ai_wallet`、`ai_wallet_ledger`，流水类型为 `order_recharge`，订单行锁与发货流水唯一键阻止重复充值。

会员 PHP 从当前会员库记录取得 `parent_uid` 并签名到短期 JWT，主账号使用自己的 UID；聊天请求不能指定主账号。Go 先冻结本人钱包，不足时冻结启用的主账号钱包，合计不足则整笔失败。结算与退款使用预扣流水确定原钱包，使用记录仍属于实际发起聊天的 UID。主子账号钱包按 UID 顺序加锁；一个请求只能完成一次结算或退款。主账号停用或没有钱包时不提供共享额度。主子关系变更在下次签发凭据时生效。

## Credit 版本切换

新版本要求先执行 `migrations/004_credit_units.php --apply`，将原 100 万余额 TOKEN 等值换算为 1000 credit；数据库金额字段按真实 microcredit 保存，旧整数统一乘 1000。此迁移必须和后台、官网、Go、EC 协同切换，不能只换界面单位。Go、后台和官网充值均校验计费单位标志；旧单位接口字段不保留兼容分支。详见 [迁移与发布](credit.md#迁移与发布)。

AI account management uses ai_account.status and disabled_message; apply migrations/012_ai_account_disable_message.sql before starting this relay version. Suspended accounts can authenticate to read account status, but chat is rejected before wallet reservation and returns the administrator's disable message. GET /v1/account/quota includes status and disabled_message. The admin API requires a non-empty disable message.

2026-10-27 release: migration 012 is applied to production cx_ec, and cx-ai-relay-current runs cx-ai-relay-local:ai-account-disable-20261027 on 127.0.0.1:8088. Health is 200; unauthenticated POST /v1/client/initialize and GET /v1/account/quota return 401, confirming the routes exist and remain authenticated; embedded-origin preflight returns 204. The previous container is retained for rollback. The public HTTPS base and reverse-proxy route are not verified by these local checks.


## Request correlation and error messages

Every relay response receives `X-Request-ID` before authentication, routing or request validation. Clients may send an ASCII `X-Request-ID` of 1–36 characters from `[A-Za-z0-9._:-]`; invalid values return 400 with a newly generated error ID. Missing IDs are generated by the relay. Unmatched routes retain normal 404/405 behavior and log the same ID. JSON errors include `error.request_id`; stream error events also include it. Client errors should show the response ID; if no response arrives, a locally generated ID identifies the attempted request without proving it reached the server.

For chat requests with an explicit `Idempotency-Key`, the existing billing identifier remains authoritative and is returned in `X-Request-ID` after validation. Without that key, billing uses the request trace ID. This preserves ledger and usage correlation rather than introducing a separate charge identifier.

Embedded-page CORS allows optional `X-Request-ID` and exposes the response header. The built-in quota page receives display data from the native host; the host makes authenticated quota requests and keeps JWTs outside the page, so it does not depend on browser CORS or local-network permission. Service/blocked-client and suspended-account 403 responses use `service_disabled` and `account_disabled` and retain the configured business message; they must not be reported as unsupported model capabilities. Capability restrictions retain their separate model messages.

## 管理后台错误记录

`ai_usage` 的失败行保存 `error_code`、可空 `error_detail`（JSON文本）和可空 `elapsed_ms`；详情键包括 stage、http_status、provider、upstream_model、upstream_status、message、cause、upstream_body、body_truncated、started_at。错误正文先脱敏，最多64 KiB；0上游状态表示尚未收到响应。普通 `GET /v1/account/usage` 不暴露 error_detail；产品12 get_usage/get_account_usage 只读取摘要；点击详情后由 get_usage_error(usage_id) 在后台登录校验后单独读取一条错误正文。聊天客户端原有HTTP/SSE错误协议保持不变。

## 全局对话提示词

产品12已认证接口 `ai/manage/get_service_config` 返回 `global_prompt`；`ai/manage/save_service_config` 要求显式提交同名 UTF-8 字符串，最多10000个Unicode字符，空字符串清除。多行、空格、引号原样保存，null/缺失/非字符串拒绝。配置和审计同事务提交，审计仅保存长度与SHA256，不记录提示词正文。

每次 `POST /v1/chat/completions` 从数据库读取最新配置。非空白全局提示词作为第一条且唯一的 system 消息，客户端原有 system/developer 消息保留内容及位置，但以 user 角色提交，确保其优先级低于全局规则。其他消息、工具调用和工具响应保持顺序。空白配置不注入也不改变客户端角色。每次请求只注入一次，不修改客户端历史；新对话及既有对话下一轮均使用最新配置。

注入在输入估算之前完成，因此占用上下文和TPM，并纳入预扣及缺少上游usage时的估算；实际结算仍以上游usage为准。普通会员配置、模型和额度接口不返回提示词。仅作用于官方Eder中转对话，自定义供应商直连不受此配置控制。供应商自身系统规则仍由供应商控制。

## 图片输入（多模态）

`/v1/models` 与 `/v1/account/quota.pricing_models` 的每个模型返回布尔 `allow_images`。该能力属于文本对话模型，与生成图片的 `model_type=image` 不同；默认false，后台必须按供应商实际能力显式开启。迁移为016_model_image_input.sql，启动不自动建列。

允许的Chat Completions消息为用户 `content` 数组：`{"type":"text","text":"问题"}` 和 `{"type":"image_url","image_url":{"url":"data:image/png;base64,...","detail":"auto"}}`。图片只接受PNG/JPEG内嵌data URL，不接受远程URL、本地路径、音频或视频。单图上限由模型max_image_bytes指定（默认4MiB，可配置1KiB至32MiB，须为1024字节整数倍）、每边最多2048像素，请求中历史与当前图片合计最多32MiB解码数据、最多32张；EC新输入张数不得超过当前模型max_images_per_request。类型、真实图像、大小、像素和能力在预扣/上游调用之前验证，拒绝不修改钱包；图片内容原样转发。

Go上下文、TPM和预扣估算时，官方DeepSeek Flash视觉模型每图按1024输入token上限预算，其他模型按8192预算，文本/工具继续原规则，不把base64长度当文本token。实际结算仍使用供应商usage，缺失usage继续按文档的估算规则；供应商的真实图片token算法可能与预算不同。图片没有按张附加费，含图请求按该模型独立多模态输入/缓存/输出token单价及时段价结算。切换到不支持图片的模型后，仍含图片的历史请求会被明确拒绝，不静默丢弃历史图片。

## 多模态价格目录与快照

模型目录及quota.pricing_models新增vision_pricing，与原pricing并列，结构同为kind/unit/input_credit_per_1k/cache_input_credit_per_1k/output_credit_per_1k。vision_pricing展示当前时段生效的含图请求价，pricing展示当前纯文字价；均不包含成本。后台模型配置返回vision_input_price_credit_per_1k、vision_cache_input_price_credit_per_1k、vision_output_price_credit_per_1k及peak_/limited_前缀组，保存字段去掉_credit_per_1k，价格必须显式提供。三组vision_*_cost_yuan_per_1m字段可空，用于独立计算器。

含图请求的所有实际输入/缓存/输出usage仅按多模态表结算一次。普通用户usage金额读取方式不变；后台get_usage和get_account_usage.recent返回billing_input_kind=text/vision及实际快照价格。该字段代表采用的价格表，与price_kind=standard/peak/limited的时段维度独立。高级规则、迁移和初始值见pricing.md的多模态token独立价格表。


图片/视频生成的接口、独立模型能力、按张/按秒分时计费、持久任务恢复及客户端设置见 [图片与视频生成](media-generation.md)。


## Eder 默认思考与上下文

`GET /v1/service/config` 返回 `default_reasoning_effort`（none/minimal/low/medium/high/xhigh/max）及 `default_context_tokens`（4096、8192、16384、32768、65536、131072、262144、524288、1048576），不返回global_prompt。后台ai/manage/get_service_config与save_service_config在既有管理员鉴权下读取/保存同名字段，保存与提示词、开关和版本策略同事务审计；缺字段或非法类型/档位明确失败。

`GET /v1/models`及quota.pricing_models每个模型返回已按能力约束的同名默认字段：上下文=min(服务默认,模型最大上下文)；思考按none→minimal→low→medium→high→xhigh→max排序，取不超过目标的最高可用档位，目标高于模型能力即取模型最高值，无较低档位时取模型最低值，禁用思考统一none。结果不依赖白名单排列顺序。

默认值用于EC尚未手动保存的服务/模型配置，不写回个人设置；每次读新目录均能使用最新服务默认。个人上下文/思考选择优先；已存上下文仍受模型上限限制，已存思考档位被后台撤销时要求用户重新选择。上下文是客户端SDK整理历史的默认预算，不是降低模型最大能力，也不限制用户在模型上限内另行选择。直接chat调用省略思考与thinking参数时使用服务默认，显式选择仍须通过白名单校验。


## 模型图片文件大小限制

021_model_image_size.sql增加ai_model.max_image_bytes（INT UNSIGNED NOT NULL，默认4194304）。模型目录及额度返回max_image_bytes，服务端严格要求1024–33554432且为1024倍数；配置无效则拒绝目录/请求。每张图片在预扣前按模型限制校验，历史加当前图片合计32MiB，最多32张、单边2048px；Chat JSON上限64MiB。EC按模型max_images_per_request校验单次图片张数，官方读取目录限制，自定义按模型MaxImagesPerRequest设置；单张大小按MaxImageKilobytes设置。大小配置不改变token/按张计费规则。


## 模型单次图片张数

ai_model.max_images_per_request为每次实际模型请求的图片总数上限，范围1–32，默认4，包含随请求发送的历史图片；不是只限制当前新添加的附件。022_model_image_count.sql由用户备份ai_model后执行，新增非空unsigned TINYINT，已有模型初始化4，不改图片大小、价格或钱包。导入的新模型沿用数据库默认4。

后台在图片输入能力区域编辑该字段，保存严格要求字符串整数1–32，与大小、模型参数及审计在同一事务提交。模型目录和quota.pricing_models返回整数max_images_per_request，缺失或无效配置明确失败。Go启动要求022字段存在，聊天在预扣之前检查完整messages的总图片数；超限不调用供应商、不扣费。发布顺序为022→Go及后台→EC，不能在字段缺失时发布新接口。

EC选图、粘贴、草稿发送、队列分发和插队按当前模型上限检查新附件；自定义模型MaxImagesPerRequest默认4并可在设置中修改，官方读取模型目录。原生图片入口最多接收平台上限32张，实际发送前按模型再次校验。SDK对每次准备发送的完整消息进行计数，覆盖历史导入、会话恢复和工具续轮，自定义AI同样受限；超限显示本地化image_too_many，不自动删除历史或重试。图片总字节32MiB、单边2048px、单张大小配置保持独立生效。

# Local development

## Requirements

For model-management publishing, apply `migrations/013_period_supplier_costs.sql` only through the user's explicit database operation, then read-only verify six nullable unsigned cost columns before publishing PHP. No time-range schema migration is required. Verify provider filtering, preview-without-inserts, selected-only import, batch deletion rollback/history retention, strict single-range normalization, independent period cost calculation and exact six-decimal round trips in isolated test systems. Publish the scoped backend and isolated frontend, then refresh the local relay Docker image without changing its environment/port bindings; keep the old container for rollback. Do not execute model deletions or imports on business data as a release test.

The quota API also provides exact string `used_credit` (settled model costs plus administrative deductions) and `total_credit` (currently available member/eligible-parent balance). Verify these in an isolated database, including manual clearing, parent scope, pending reservations, six-decimal precision, cumulative usage beyond Int64 and consistent concurrent snapshots. This addition requires no schema migration; it does not change the existing model-only `used_percent` calculation or write business wallets.

- Go 1.23 or newer
- MySQL 8 compatible database

## Configuration

Set environment variables outside source control:

| Variable | Purpose |
| --- | --- |
| `HTTP_ADDR` | Listen address, defaults to `:8088` |
| `DATABASE_DSN` | MySQL DSN for the existing `cx_ec` database, with parseTime enabled |
| `JWT_HS256_SECRET` | Shared with PHP; at least 32 random bytes |
| `JWT_ISSUER` | Expected member issuer |
| `JWT_AUDIENCE` | Expected relay audience |
| `ADMIN_BEARER_TOKEN` | Temporary bootstrap admin credential; replace with managed admin identities before production |
| `ENCRYPTION_KEY` | Go-side key for provider credential encryption/decryption; set the same value as the `ENCRYPTION_KEY` PHP constant in `cx_member_new_admin/config/config.php` (same base64 key or passphrase) |
| `UPSTREAM_TIMEOUT` | Provider request deadline |

PHP product 17 reads the existing `ENCRYPTION_KEY` constant from its protected `config/config.php`. Go `JWT_HS256_SECRET` must match that raw string. Optional PHP constants `CX_AI_JWT_ISSUER` / `CX_AI_JWT_AUDIENCE` default to `cx-member` / `cx-ai-relay` and must match Go. PHP atomically creates the account/wallet if absent and inserts each short-lived JTI directly into shared `cx_ec` through its framework connection. It needs no internal HTTP callback to Go, so remote sandbox PHP also works with Go running locally. The member-admin controller likewise selects `cx_ec` after admin authentication. Its PHP `ENCRYPTION_KEY` constant must match Go `ENCRYPTION_KEY` for provider credential decryption. EC reads the public relay origin from app setting `AI_SERVICE_BASEURL`.

Never commit real values. PHP signing and Go verification must use the same secret, issuer, and audience.

The CxApp native host calls the configured relay; the conversation WebView receives display-only bridge messages from `https://cxec-agent.invalid`. Go enables CORS only for this origin on `GET /v1/service/config`, `POST /v1/client/initialize`, `GET /v1/account/quota` and `POST /v1/chat/completions`. Custom OpenAI-compatible endpoints used in the embedded conversation must permit this origin and the `Authorization`/`Content-Type` request headers. The C# host obtains the Eder JWT through the existing member session and keeps it in native HTTP requests; EC cookies are never sent to Go. Apply migration 011 before starting the relay with client version policy enabled.

## Development commands

Forwarding performance checks use a simulated upstream over real HTTP/TLS sockets: `go test -count=1 -run TestForward -v ./internal/relay`. They cover two rounds of eight concurrent TLS connections, independent header/content arrival, the anti-buffering response header, fixed keepalive comments and cancellation propagation. `go test -run '^$' -bench BenchmarkSafeCompletionFrame -benchmem ./internal/relay` measures the per-frame validation cost. Compare the same payload, reasoning mode and upstream schedule; local scheduling differences are not evidence of faster model inference. A real deployment comparison must additionally measure its authentication/database work, rate-queue waits, network path and upstream model time.

Account suspension verification must cover both an account already suspended at chat entry and suspension between the initial account check and wallet reservation. Both return HTTP 403 with `account_disabled` and `您的账户异常：` followed by the configured explanation, without upstream access or financial records. A genuinely missing wallet remains a service error rather than insufficient quota or account suspension; this distinction requires no schema migration.

Scheduled pricing requires `migrations/003_model_pricing.sql` before upgrading PHP and Go. It initializes existing models with peak/promotional pricing disabled and retains their standard prices. See [pricing](pricing.md) for migration checks, rollback boundaries and tests.

```powershell
go mod download
go run ./cmd/relay
```

Apply `migrations/007_usage_retention.sql` after 006 and before deploying the retention cleanup. It adds the date index used to find expired records efficiently. Verify the target is `cx_ec` and `idx_ai_usage_created_at` is absent before applying; MySQL DDL implicitly commits, so inspect the index state if execution fails rather than rerunning blindly.

The relay runs usage retention cleanup once on startup and every 24 hours, deleting `ai_usage` records older than 30 days together with their `ai_usage_pricing` snapshots in batches of 500. Wallet ledger rows are retained. Cleanup failures are logged and retried at the next scheduled run.

Database migrations target the existing `cx_ec` database and are stored in `migrations/`; apply them in order. `001_initial.sql` creates only `ai_*` tables and `002_seed_deepseek_provider.sql` adds the default DeepSeek provider. Configure its API credential through the member admin so it is AES-GCM encrypted, import models from the provider's `/models` endpoint, then set prices and policy before enabling imported models. Use the same JWT/encryption secrets in PHP and Go, and credit a wallet before enabling user traffic.

The Eder availability switch and optional unavailable message require `migrations/010_eder_service_config.sql`. Apply the migration to `cx_ec` before deploying the Go and admin updates. It creates the singleton row enabled with a blank message; the Go API does not create or backfill missing configuration at runtime.

## Quota percentage verification

`used_percent` remains the historical settled model consumption divided by cumulative recharge. The quota gauge instead uses `remaining_percent`, calculated by the server from `quota_baseline_credit`. For each wallet, the baseline is the `balance_after_micro` from its newest `admin_recharge` or `order_recharge` ledger row. Those recharge rows are already committed with the wallet update, so no schema migration is required. The EC client validates and displays the server value without calculating it from cumulative consumption.

Verify `quotaSnapshot` against an isolated database rather than crediting or reducing a business wallet. Coverage should include partial consumption followed by recharge resetting the baseline to the new post-recharge balance, member/parent wallets with independent latest recharge baselines, half-up rounding, zero baseline returning null, and rejection when current balance exceeds the baseline. Verify same-snapshot behavior during concurrent recharge and settlement.

The historical `used_percent` calculation retains its own independent verification: ordinary ratios and half-up boundaries, zero cumulative recharge, values capped at 100, totals above Int64 and invalid ledger signs. Reservations, refunds/releases and administrative deductions do not affect that historical metric; deleting old chat usage must not shorten its ledger history.

Independent cache-hit pricing requires `migrations/009_cache_pricing.sql` after migrations 003 and 008. Verify all four new price columns and both usage columns are absent before applying; MySQL DDL is not transactional. The migration initializes cache prices and historical cached-token counts to zero. Deploy the Go and admin changes only after all four tables have the expected columns. DeepSeek cache hits are read from `usage.prompt_cache_hit_tokens` or `usage.prompt_tokens_details.cached_tokens`; settlement charges cache hits at the selected standard/peak/limited cache price and other prompt tokens at the regular input price. Verify request reservation uses the higher input price and cache price so settlement cannot exceed reservation.

Concurrent recharge/settlement/administrative adjustment must not cause the balance and ledger totals to come from different snapshots: the implementation uses a read-only REPEATABLE READ transaction, with the original wallet overflow checks preserved. SQL, transaction or financial-validation errors must return the quota 503 response rather than 0/null as a fallback. Build and run relevant Go tests before delivery; do not deploy or alter business balances without explicit authorization.

## Model-type schema and rollout

`migrations/008_model_type.sql` targets `cx_ec` and adds `ai_model.model_type ENUM('text','image','video') NOT NULL DEFAULT 'text'` after `upstream_model`. Existing rows and future imports explicitly receive the database default `text`; new admin forms also start at text. The administrator must assign a model's real category before using the catalog. No image/video generation endpoint is introduced.

Before execution, verify the actual database/server, `SHOW CREATE TABLE ai_model`, current model counts/settings and whether `model_type` exists; back up the schema and model records and pause model editing/chat for the schema/code switch. If absent, apply the versioned script once. If present, verify its exact ENUM/default/nullability and model counts grouped by type instead of rerunning ALTER; an incompatible existing definition stops rollout. Start updated Go/PHP only after verification. `OpenStore` refuses startup without the column and does not create it. MySQL DDL implicitly commits; failure requires inspecting the actual column state, not blindly repeating the ALTER or claiming transactional rollback.

For rollback, first export any category choices and restore the previous Go/PHP version, then drop `ai_model.model_type` as shown in the script. Do not drop the column while the new code still queries it. The independent 007 usage-retention migration and its deployment status remain unchanged. The authorized model-type release has verified production `cx_ec` on MySQL 5.7.40-log: `model_type` already has the exact `ENUM('text','image','video') NOT NULL DEFAULT 'text'` definition and both models are text. That verified schema already satisfies 008; no further execution of 008 is required for this production database, and this release did not repeat ALTER. Go uses `cx-ai-relay-local:model-type-20261027`; health 200 and unauthenticated quota 401 passed. The previous container is retained as `cx-ai-relay-before-model-type-20261027`. PHP and the isolated admin frontend build were published; no functional tests were run for this iteration. Other independent, unpublished work is not implied to be deployed.

## Model context schema and rollout

The context policy requires `migrations/005_model_context.sql` on `cx_ec` before the new Go/PHP version. It creates the nine-row `ai_context_tier(token_count)` dictionary and adds `ai_model.max_context_tokens` as a non-null unsigned integer, explicitly initializing existing rows to 65536 (64K). Index `idx_ai_model_context` and foreign key `fk_ai_model_context` restrict model values to the nine dictionary tiers, 4K/8K/16K/32K/64K/128K/256K/512K/1M. The business database is MySQL 5.7.40, where CHECK constraints are not enforced, so the migration uses the dictionary foreign key. Imported models use the same database default and remain disabled until an administrator configures them. New model forms start at 64K. Administrators must verify supplier capabilities and choose the actual upper tier before enabling a model.

Before execution, verify the active database, `SHOW CREATE TABLE ai_model`, existing model rows and the absence of `ai_context_tier`, `max_context_tokens`, `idx_ai_model_context` and `fk_ai_model_context`. Pause model editing and chat traffic, back up the schema/model settings, execute the migration once, and verify all nine dictionary values, the column default/initialized values, index and foreign key before deploying Go/PHP. MySQL DDL implicitly commits: a failure can leave the dictionary table or part of the migration committed. Stop rollout and inspect the real table/column/foreign-key state; do not retry blindly, skip constraints or claim transactional rollback. To roll back, first restore earlier Go/PHP and export model context values, then drop `fk_ai_model_context`, `idx_ai_model_context` and `max_context_tokens` from `ai_model`, and finally drop `ai_context_tier`, in the order shown in the migration. Local development does not apply the business-database migration or deploy services automatically.

Verify catalog tiers, invalid model policy rejection, input at/above the context boundary, output clamping, and independence from shared TPM/RPM. EC settings and chat gear must retain independent model choices and restrict them to the latest server maximum.

## Model reasoning schema and rollout

`migrations/006_model_reasoning.sql` follows 005 on `cx_ec`. It adds `ai_model.reasoning_efforts VARCHAR(128) NOT NULL DEFAULT '[]'`, storing a JSON string array, and `reasoning_format ENUM('reasoning_effort','deepseek')` with default `reasoning_effort`. The explicit initialization transaction sets existing `allow_thinking=1` models to `["high"]`; other models keep `[]`. Providers whose normalized base URL is exactly `https://api.deepseek.com` or `https://api.deepseek.com/v1` initialize as `deepseek`. Administrators must inspect actual supplier/model support and adjust tier choices before enabling traffic; this initialization does not establish that every tier is supported.

Before applying, verify the active database/version and existing model/provider policy, confirm both new column names are absent, back up model settings, and pause chat/model editing. Execute once, then verify column definitions and every model's initialized array/format before deploying Go/PHP/client. MySQL DDL implicitly commits separately from the subsequent initialization transaction; any failure stops rollout and requires inspecting actual schema/data. Do not retry blindly, silently default malformed JSON, or claim that transaction rollback removes added columns. To revert, restore the earlier Go/PHP version, export the new reasoning policy, then drop the two columns as documented in the script. Development does not apply 006 to the business database or publish services automatically.

Verification must cover whitelist serialization, master-switch shutdown, default first-tier selection, all seven valid tier names, invalid/duplicate JSON entries, disabled or unknown requested tiers, contradictory thinking/effort values and malformed thinking objects. Rejections must happen before reservations and upstream calls. Client verification also covers per-service/model isolation, hidden choices for an empty whitelist, and explicit reselection after a stored tier is revoked; there must be no automatic upgrade to another tier.

## Credit 发货与共享额度验证

类型 12 与共享额度的钱包/账本复用既有表，但本轮 credit 单位切换必须执行 004 金额迁移并建立计费单位标志。先部署产品 17 签发主账号身份的接口，再启用 Go 共享额度结算，最后配置后台充值套餐并绑定官网商品变体。官网连接必须能够在同一事务内操作官网表和 `cx_ec.ai_account`、`ai_wallet`、`ai_wallet_ledger`；不能通过额外连接替代原子事务。开发修改不自动发布或充值业务账户。

验证需覆盖本人/主账号独立和混合余额、无主账号钱包、主账号停用、合计不足、并发子账号、上游失败退款、取消请求退款、重复请求和重复支付回调。只使用独立测试 MySQL；不要通过给真实钱包加款来验证。

## Credit 版本开发与切换

读取 [Credit 计费单位](credit.md)。004 不是只改标签：原整数金额乘 1000 转为真实 microcredit，外部显示原值除 1000 后的 credit；后台与官网输入最多六位小数，EC 解析 decimal。Go `OpenStore` 要求迁移完成标志，未迁移数据库不能启动新版本。先备份并暂停全部计费/充值/支付发货入口，再执行 PHP 迁移和全套版本切换。2026-10-25 已完成业务库迁移及官网、后台、Go 发布并恢复入口；EC Release x64 安装包已在本地生成，未上传客户端自动更新。具体核验与备份位置见 credit 文档。

## 失败请求记录部署

先核对活动连接为 cx_ec、ai_usage 是 InnoDB、request_id 唯一索引存在，且 error_detail/elapsed_ms 不存在，再单独执行 migrations/014_usage_error_details.sql。两列均可空，历史成功行保持NULL，无数据回填或计费改写；DDL不可事务回滚，执行前保存原表结构，发生碰撞直接停止。随后部署Go及产品12后台PHP/前端，不能仅部署要求新列的查询。业务库迁移和发布须明确授权后执行；部署完成后核对两列可空、历史数据未改写、Go健康200、后台鉴权以及前端HTTP/SHA256一致。

用隔离MySQL和本地模拟上游验证404、超时、取消、JSON/SSE错误、凭据脱敏、原文截断及退款。不要用真实供应商付费请求测试。故障分析需对照Request ID和预扣/退回时间，客户端及Go当前均有90秒模型请求期限；失败详情用于区分等待上游响应、流读取及客户端取消，不能仅凭耗时猜测供应商原因。

## 全局提示词部署

先备份cx_ec.ai_eder_service_config，确认global_prompt不存在、config_id=1唯一记录存在，再由用户执行migrations/015_global_prompt.sql。该脚本先加可空MEDIUMTEXT，初始化为空，再收紧非空；MySQL5.7 DDL隐式提交，失败时检查实际状态，不盲目重跑。它不更新余额、模型价格或其他配置。回退前导出提示词，先恢复旧Go/PHP/前端再删除字段。Go启动要求字段为非空MEDIUMTEXT，运行时不会补表补值。

验证空值关闭、多行Unicode原样保存、10000字符边界、类型拒绝、配置与审计回滚、每轮唯一system注入、客户端角色降级、工具链保持、上下文估算及会员API不泄露。SQL执行并核对字段完成前不得部署要求该列的新PHP/Go。

## 多模态部署与验证

016_model_image_input.sql由用户执行前备份ai_model，确认allow_images不存在。只读核验真实库134模型后生成脚本；用户执行后已核验TINYINT(1) NOT NULL DEFAULT0。新旧模型均默认关闭，不自动猜测供应商图片能力。Go启动要求字段存在，PHP与Go应在SQL之后发布；恢复旧代码并导出能力配置后才可回退列。DDL隐式提交，失败检查实际结构，不盲目重跑。

使用隔离数据库和模拟HTTP供应商验证PNG/JPEG、坏编码/伪MIME/像素与大小限制、远程URL拒绝、能力禁用时不扣费、图片原样SSE/JSON转发、usage结算和目录能力。EC验证图片选择/粘贴/移除/纯图发送、会话草稿、前台/后台队列、模型撤销、重试及恢复；不向真实付费模型发送测试图片。

## 多模态价格部署

017由用户执行后，核对9个售价列为unsigned BIGINT NOT NULL DEFAULT0、9个成本列可空unsigned BIGINT、快照billing_input_kind为ENUM(text,vision) NOT NULL DEFAULTtext；三表InnoDB，模型定价行无缺失。初始化复制当前普通表的各时段价格，不操作余额和历史扣费。先schema，再Go/PHP/前端；启动缺字段直接失败，不隐式回填。

只在隔离MySQL与模拟上游验证text/vision价格隔离、总usage单次扣费、缓存高价预扣、时段/跨午夜/活动优先、排队和改价后的锁价、退款/幂等/快照事务、历史图像选价，以及PHP精确金额和Vue独立成本计算器。不得以真实钱包或真实付费图片请求作为发布测试。


DeepSeek视觉适配无需迁移或后台/EC更新。验证官方HTTPS端点、合法路径、443端口和两个已记录模型的匹配，拒绝伪造域名/userinfo/query改变预算；验证每图1024预算、original细节参数、实际usage单次结算及缺usage估算，不通过真实供应商付费调用验证。参考官方vision指南，不推断其他DeepSeek模型视觉能力。


图片/视频生成的接口、独立模型能力、按张/按秒分时计费、持久任务恢复及客户端设置见 [图片与视频生成](media-generation.md)。


Eder默认配置需先由用户备份并执行migrations/020_eder_chat_defaults.sql，新增思考枚举及默认上下文，初始none/65536，不修改已有提示词/开关/用户选择/钱包。发布前只读验证字段及单例，Go启动要求020列存在；新EC要求模型目录提供合法的两个默认字段，需先发布Go再使用新EC。验证应覆盖默认高于模型上限、无序白名单、禁用思考、显式个人选择、动态修改服务默认及错误配置失败。


## 模型图片文件大小限制

021_model_image_size.sql增加ai_model.max_image_bytes（INT UNSIGNED NOT NULL，默认4194304）。模型目录及额度返回max_image_bytes，服务端严格要求1024–33554432且为1024倍数；配置无效则拒绝目录/请求。每张图片在预扣前按模型限制校验，历史加当前图片合计32MiB，最多32张、单边2048px；Chat JSON上限64MiB。EC按模型max_images_per_request校验单次图片张数，官方读取目录限制，自定义按模型MaxImagesPerRequest设置；单张大小按MaxImageKilobytes设置。大小配置不改变token/按张计费规则。


## 模型单次图片张数

ai_model.max_images_per_request为每次实际模型请求的图片总数上限，范围1–32，默认4，包含随请求发送的历史图片；不是只限制当前新添加的附件。022_model_image_count.sql由用户备份ai_model后执行，新增非空unsigned TINYINT，已有模型初始化4，不改图片大小、价格或钱包。导入的新模型沿用数据库默认4。

后台在图片输入能力区域编辑该字段，保存严格要求字符串整数1–32，与大小、模型参数及审计在同一事务提交。模型目录和quota.pricing_models返回整数max_images_per_request，缺失或无效配置明确失败。Go启动要求022字段存在，聊天在预扣之前检查完整messages的总图片数；超限不调用供应商、不扣费。发布顺序为022→Go及后台→EC，不能在字段缺失时发布新接口。

EC选图、粘贴、草稿发送、队列分发和插队按当前模型上限检查新附件；自定义模型MaxImagesPerRequest默认4并可在设置中修改，官方读取模型目录。原生图片入口最多接收平台上限32张，实际发送前按模型再次校验。SDK对每次准备发送的完整消息进行计数，覆盖历史导入、会话恢复和工具续轮，自定义AI同样受限；超限显示本地化image_too_many，不自动删除历史或重试。图片总字节32MiB、单边2048px、单张大小配置保持独立生效。

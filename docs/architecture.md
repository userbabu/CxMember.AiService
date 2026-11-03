# CX AI 中转服务架构

## Components

```text
CxApp WPF host -- session token endpoint --> cx_home PHP (17 product)
CxApp WPF host -- Bearer JWT, OpenAI compatible --> Go AI relay
                                               |--> provider credential registry
                                               |--> upstream OpenAI-compatible vendors
                                               |--> wallet reservation / usage ledger (MySQL)
cx_member_new_admin ---------------------------|--> cx_ec (reuse framework MySQL connection, switch schema after admin auth)
```

会员 PHP 系统负责识别既有登录会话并签发短期用户 JWT。CxApp WPF 宿主仅调用该会话令牌接口；C# 宿主统一访问模型聊天和 Eder 额度接口，聊天 WebView 只通过受控消息桥接收展示数据、提交用户输入，工具由宿主执行。Go 服务只信任配置的 issuer、audience 和 HS256 密钥及已登记的 JTI，并只向固定页面来源 `https://cxec-agent.invalid` 的额度与聊天接口开放 CORS。浏览器/桌面端不接触供应商凭据。后台 PHP 使用自身登录检查并直接访问 `cx_ec`；用户 JWT 不具备后台管理权限。

普通 JSON API 的成功与错误都遵循 CxWebServer `ResponseService` envelope：`code/msg/data/errors`，错误另带 `request_id`；消息文本由 Go 原样保留并由 EC 优先展示。Chat Completions 的成功 HTTP/SSE 响应继续遵循 OpenAI 协议，错误响应在 OpenAI `error` 字段之外同时提供统一 envelope。

禁用账户在聊天入口及预扣时被拒绝，HTTP 状态为 403，错误类型为 `account_disabled`。服务端统一以“您的账户异常：”加后台禁用说明生成提示，`msg` 与 `error.message` 一致，客户端原样展示；不执行上游调用和扣费。

## Chat and billing lifecycle

The server owns an upstream HTTP transport cloned from Go's default transport, retaining its proxy, TLS and HTTP/2 behavior and keeping up to 32 idle connections per upstream host. Concurrent conversations can reuse these connections in subsequent rounds instead of repeatedly establishing TLS sessions. Streaming requests negotiate uncompressed responses. Once upstream headers arrive, the relay flushes downstream headers with `X-Accel-Buffering: no`; validated completion events and fixed, sanitized keepalive comments are then flushed as they arrive.

1. Validate JWT and enabled account/model access.
2. Enforce request size and model capability flags. Reserve against a shared, model-wide RPM/TPM window; wait for the next one-minute window when the current one is full, and stop waiting if the client disconnects.
3. Reserve the configured maximum request charge using a per-request idempotency key.
4. Forward only allowed OpenAI-compatible fields to the selected provider. Stream chunks to the client and propagate cancellation.
5. Settle against upstream prompt/completion usage using model prices and multiplier; refund unused reservation. If upstream fails, refund the reservation.

Wallet amounts use integer microcredits (1 credit = 1,000,000 microcredits). Public amounts and model prices are exact credit decimal strings. Provider costs and user prices are distinct. If upstream omits usage, Go estimates prompt tokens from the request and completion tokens from the output limit; those estimates are charged and stored in the usage ledger.

## Quota snapshot and usage percentage

The same quota response exposes `used_credit` for settled model consumption plus `admin_deduction`, and `total_credit` for the currently available member/eligible-parent wallet balance (equal to `balance_credit`). These values describe completed quota depletion and what can be used next; `total_credit` is not historical recharge. Reservations affect available balance immediately but do not become completed usage until settlement. All fields use the same transaction snapshot, with arbitrary-precision formatting for cumulative usage. The separate `used_percent` keeps its original model-consumption-only definition.

`quotaSnapshot` reads the member wallet, eligible parent wallet, latest per-wallet recharge baselines and cumulative ledger totals in a read-only REPEATABLE READ transaction, so concurrent reservation, settlement, recharge or administrator deduction cannot mix balance and percentage from different database snapshots. The eligible wallet pool is the same for balance and `used_percent`, and the main account is not counted twice.

The quota gauge uses `remaining_percent = current balance / latest post-recharge baseline * 100`. Each baseline is read from the latest `admin_recharge` or `order_recharge` row's persisted `balance_after_micro`; eligible member and parent baselines are added once each. Recharge ledger insertion shares the wallet update transaction, so a successful top-up establishes the next baseline without a separate balance-history calculation or new schema field. Spending, refunds/releases and administrator deductions leave the baseline unchanged. The server returns the combined baseline as `quota_baseline_credit` and the rounded percentage as `remaining_percent`; a zero baseline yields null, and a balance above baseline is a quota data error.

`quotaUsedPercent` divides actual settled `settlement` / `settlement_parent` consumption by `admin_recharge` / `order_recharge` inflows. It excludes reservations, releases/refunds and administrative deductions. SQL aggregate values are parsed into `big.Int` and rounded half up using integer basis points; zero recharge yields null and the public number is capped at 100 with at most two decimal places. Wallet balances retain the existing Int64 overflow/negative checks. Invalid financial signs, SQL/commit failures or invalid totals abort the quota response instead of returning a fabricated percentage. The percentage depends on retained financial ledger entries, so the 30-day chat-usage cleanup does not shorten its history. This is a Go API addition only, without a schema or UI change.

## Model policy

The admin model workflow separates upstream discovery from mutations. Preview is read-only; selected import revalidates the provider catalog and active credential, while preserving existing model IDs. Provider filtering is executed by prepared SQL. Bulk deletion locks the complete requested set and commits deletion plus audit as one transaction, retaining financial and usage history. Model IDs and provider/credential IDs are represented as browser strings.

Supplier costs are stored independently for standard/low, peak and limited periods. Standard costs remain on `ai_model`; migration 013 adds nullable peak/limited cost columns to `ai_model_pricing`, with no customer-price or wallet updates. The single UI interval is normalized into the existing peak start/end fields. The relay still locks existing standard/peak/limited cache/non-cache/output prices; cost editing does not itself change a published customer price until the administrator saves that price.

Daily peak and date-limited promotional prices are configured independently per model in `ai_model_pricing`. A request locks its price before the rate queue, with promotion > peak > standard priority. Settlement persists `ai_usage_pricing` in the wallet/usage/ledger transaction. See [pricing](pricing.md) for time-zone, interval and migration rules.

Cache-hit input tokens are a separate price component for standard, peak and limited pricing. The relay reads DeepSeek's `prompt_cache_hit_tokens` or OpenAI-compatible `prompt_tokens_details.cached_tokens`, records the count in `ai_usage.cached_prompt_tokens`, and snapshots the cache unit price. Settlement charges ordinary prompt tokens at input price and hit tokens at cache input price; when reserving before upstream usage is known, it reserves at the higher of those two prices.

Model category is owned by `ai_model.model_type`, a non-null ENUM with `text` / `image` / `video` values. The admin editor validates and audits it, and both model/quota catalogs publish it. Chat validates the category before price resolution or financial reservation and accepts only text; image/video registration does not create generation routes. Go startup requires the model-type column from migration 008, and missing/invalid runtime types fail rather than receiving a fallback.

Each published model has provider/model mapping, enabled state, input/output unit prices, multiplier, TPM, RPM, a fixed-tier maximum context window, and flags for reasoning and tool calls. `ai_model.max_context_tokens` is the server policy, constrained by foreign key `fk_ai_model_context` to the nine-row `ai_context_tier` dictionary; `/v1/models` and the quota pricing catalog publish that maximum plus all available lower context tiers. EC stores a per-service/per-model budget bounded by this policy. Before rate reservation or billing, Go rejects an input estimate that fills the context window and limits output to the remaining window. These context and TPM limits are independent. TPM/RPM apply across all users and relay processes through a database-backed per-model window. The relay rejects a prompt estimate that cannot fit the model TPM, clamps `max_tokens` to the remaining TPM budget, and queues a request when the active minute is full.

## Reasoning policy ownership

`ai_model.allow_thinking` is the master switch; `reasoning_efforts` stores the fixed-tier whitelist independently, and `reasoning_format` selects explicit OpenAI-compatible or DeepSeek serialization. Public model/quota catalogs publish only the effective whitelist; disabling the master switch publishes an empty list. The relay resolves and validates effort plus an optional thinking toggle before reservation, then writes the allowed upstream fields explicitly. Administrators control supported tiers; the client stores a user selection per service/model and must not replace a revoked selection with a stronger tier automatically.

## Eder service availability

The singleton `ai_eder_service_config` row stores the Eder availability switch and the message shown when it is disabled. The admin controller updates the row with an audit record. `GET /v1/service/config` is authenticated and reads MySQL on every request; database errors or a missing row fail the request. EC fetches the setting before each Eder conversation turn and does not start the model request if the service is disabled or its configuration cannot be read. A blank custom message or failed read displays the localized default explaining that Eder is unavailable and a custom AI service can be bound. Custom AI mode does not use this switch.

## Cross-project integration boundaries

- `cx_home/17`: issue JWT from a valid EC member session, with the current member id and product context. Read the existing `ENCRYPTION_KEY` configuration constant and register account/wallet/JTI atomically in shared `cx_ec`; Go verifies the same raw signing secret.
- Go: validate JWT and registered JTI; own provider routing, rate limits, wallet, usage ledger, and chat APIs.
- `cx_member_new_admin`: authenticated PHP controller connects directly to `cx_ec`; Vue pages call that controller. Never expose database credentials, signing secrets, or stored provider API keys to Vue.
- `CxApp`: request a short-lived JWT from the authenticated account session and use it as the bearer token when the selected AI base URL is the official relay. Existing custom AI provider configuration remains supported.


## 额度归属与发货

官网产品 10 的 AITOKEN 套餐发货使用类型 12，由官网连接在自身发货事务内访问限定表名 `cx_ec.ai_*`，不调用 Go 管理接口、不创建第二个可独立提交的充值事务。要求官网库与 `cx_ec` 位于同一 MySQL 实例且官网连接有表访问权限；权限或表缺失时订单发货失败并回滚，不返回充值成功。

PHP 凭据包含当前会员库确认的主账号 UID，Go 仅连接 `cx_ec`。本人优先、主账号补足的预扣分别写 `reserve` / `reserve_parent`；结算分别写 `settlement` / `settlement_parent`（delta 为实际消费的负数，余额快照包含预扣返还），失败分别写 `release` / `release_parent`。预扣表示冻结，不能与结算重复累计消费。结算/退款使用原预扣流水中的钱包身份与金额，行锁及唯一请求键保护并发和幂等。`ai_usage` 及价格快照保留聊天发起者 UID，后台钱包累计消费计入实际承担扣费的账户。

## Administrative wallet adjustments

Credit reduction and clearing belong to the authenticated product-12 PHP management service, not the Go member API. The management service uses the existing `cx_ec` wallet row lock under READ COMMITTED, refuses an administrator-confirmed balance mismatch or any unfinished reservation against that wallet, and atomically writes the balance, negative `admin_deduction` ledger entry and `wallet.deduct` audit. UUID v4 plus original operation/amount/balance audit values make retries idempotent; the original actual deduction and before/after balances must also agree with the ledger. Ledger and audit attribution uses the authenticated `config.member.uid`, and missing identity fails the transaction. Only the selected existing wallet is changed; parent sharing, account identity, JWTs and chat usage are not rewritten. Administrative deductions are reported as `total_deducted_credit` and remain separate from model settlements. This capability requires no schema migration and no Go/client behavior change.

## Credit 数据边界

原 100 万余额 TOKEN 转为 1000 credit；模型 token 统计不变。API、后台表单、官网套餐及 EC 统一使用 credit，Go 与 PHP 只用整数 microcredit 持久化和扣费。`ai_billing_unit` 防止新代码把未迁移数据当作新单位。完整经济等值转换、事务、溢出与部署边界见 [Credit 计费单位](credit.md)。

## 失败请求诊断

认证后的聊天请求失败时，独立于已取消的请求上下文，在最长5秒的数据库期限内向 ai_usage 写入 status=failed、error_code、error_detail、elapsed_ms；不写入商品或聊天正文，不将失败伪装为成功结算。平台非2xx响应及 HTTP200/SSE 中的错误帧保存脱敏后的原文，单次最多64 KiB并记录截断标记；未返回正文的网络失败保存实际异常原因，区分超时与取消。诊断包含阶段、供应商、上游模型、上下游状态和开始时间，不保存请求头或明文凭据。入库失败以 Request ID 记录服务错误，不改变原错误结果。成功请求仍由账务事务保存，失败预扣沿原链路退回。

错误详情与用量记录一起保留30天并由现有清理删除；仅产品12已认证管理接口读取。会员用量接口继续只返回用量摘要，不返回平台错误正文。现有历史记录没有的错误正文不能补造。

## 服务端全局提示词

`ai_eder_service_config.global_prompt` 由后台服务设置管理，需先执行015迁移。每次聊天读取同一配置快照并在上下文估算、排队与预扣之前注入。配置非空白时，服务端system消息独占最高应用指令层级，客户端system/developer消息降为user，原有内容及工具链顺序保留；配置为空白时保持原请求。只构造本次转发消息，不向客户端历史回写，因此后续轮次不会累计服务端提示词。供应商自身规则仍不由中转控制。

提示词不进入会员配置响应或专用AI审计正文；管理保存记录长度和哈希。数据库读取失败、非法编码或超长数据直接拒绝请求。后台和Go均限制10000个Unicode字符，真实用量包含提示词。

## 多模态输入能力

ai_model.allow_images默认0，仅对text对话模型启用。后台配置、公开目录、EC选择器、发送前模型策略检查和Go聊天入口使用同一字段；自定义直连模型在本地设置独立AllowImages，不按名称推断能力。模型生成类别保持text/image/video，不把视觉输入误当图片生成。

EC通过用户文件选择或粘贴取得PNG/JPEG，将内容作为DataContent加入真实用户ChatMessage。图片草稿随会话切换保存，队列、后台会话发送、中断、重试和SDK历史携带同一图片，展示记录保留缩略图和预览。图片不进入业务日志或工作记忆摘要，存在本地对话缓存；图片本身会随聊天请求交给所选供应商。文本估算去掉base64，每图使用8192token预算；供应商usage仍为结算来源。

## 多模态选价与财务证据

图片请求与纯文字请求各用一套完整token价格，三个时段沿用同一时间策略。选择基于经过验证的完整messages，包含历史图片时仍选vision；选定的输入/缓存/输出价在队列前冻结，预扣与结算只使用该rate，不在响应后重新读当前配置。结算事务在原价格快照中写入实际单价与billing_input_kind，支持后台区分两个价格族，普通余额、账本和客户用量聚合无需变更。


DeepSeek视觉策略由真实provider base URL和上游model映射决定，不使用客户端展示名称。仅文档明确的官方Flash视觉请求按1024/图估算并接受original细节参数；不改变独立vision token价格族、实际usage结算和历史快照。其他供应商维持通用预算，平台上传限制保持不变。


图片/视频生成的接口、独立模型能力、按张/按秒分时计费、持久任务恢复及客户端设置见 [图片与视频生成](media-generation.md)。


Eder服务单例保存default_reasoning_effort/default_context_tokens；Go每次读取配置并在模型目录按能力限幅，聊天缺省思考使用同一规则。EC仅对未保存的个人选择采用服务默认，目录值不写入个人设置缓存；具体协议见[API默认配置](API.md#eder-默认思考与上下文)。


## 模型图片文件大小限制

021_model_image_size.sql增加ai_model.max_image_bytes（INT UNSIGNED NOT NULL，默认4194304）。模型目录及额度返回max_image_bytes，服务端严格要求1024–33554432且为1024倍数；配置无效则拒绝目录/请求。每张图片在预扣前按模型限制校验，历史加当前图片合计32MiB，最多32张、单边2048px；Chat JSON上限64MiB。EC按模型max_images_per_request校验单次图片张数，官方读取目录限制，自定义按模型MaxImagesPerRequest设置；单张大小按MaxImageKilobytes设置。大小配置不改变token/按张计费规则。


## 模型单次图片张数

ai_model.max_images_per_request为每次实际模型请求的图片总数上限，范围1–32，默认4，包含随请求发送的历史图片；不是只限制当前新添加的附件。022_model_image_count.sql由用户备份ai_model后执行，新增非空unsigned TINYINT，已有模型初始化4，不改图片大小、价格或钱包。导入的新模型沿用数据库默认4。

后台在图片输入能力区域编辑该字段，保存严格要求字符串整数1–32，与大小、模型参数及审计在同一事务提交。模型目录和quota.pricing_models返回整数max_images_per_request，缺失或无效配置明确失败。Go启动要求022字段存在，聊天在预扣之前检查完整messages的总图片数；超限不调用供应商、不扣费。发布顺序为022→Go及后台→EC，不能在字段缺失时发布新接口。

EC选图、粘贴、草稿发送、队列分发和插队按当前模型上限检查新附件；自定义模型MaxImagesPerRequest默认4并可在设置中修改，官方读取模型目录。原生图片入口最多接收平台上限32张，实际发送前按模型再次校验。SDK对每次准备发送的完整消息进行计数，覆盖历史导入、会话恢复和工具续轮，自定义AI同样受限；超限显示本地化image_too_many，不自动删除历史或重试。图片总字节32MiB、单边2048px、单张大小配置保持独立生效。

# Implementation status

## 模型分时计费表单布局（2026-10-30 已发布）

官网后台模型编辑先列分时开关、时区、高峰区间与活动日期，再按统一或低峰/高峰/活动分组；每时段内分别列普通及多模态TOKEN的未命中输入、缓存命中输入、输出成本和售价。生成模型同样按时段展示按张/秒价格，隐藏无关TOKEN输入；图片能力开关前置，计算器与原保存字段契约不变，隐藏配置不清空。此次只变更AiManage.vue及说明文档，不需要SQL、Go或EC升级，不改现有价格/钱包/结算。

真实Vue2/Ant DOM177项及工作区/隔离生产构建通过，覆盖显示顺序、全部价格字段独立绑定、开关隐藏后恢复、已有模型回显、计算器隔离及原save_model完整参数。254个生产静态资源已发布并FTP/HTTPS逐字节核验，首页和未登录后台鉴权通过；PHP保持与上一发布逐字节一致，未重传或修改。备份和证据为data/pricing-form-layout-20261030。测试临时目录递归清理被自动审批拒绝，仅返回blocked by policy，未绕过。


## Eder默认思考与上下文（2026-10-30 已发布）

后台AI服务设置增加默认思考模式及默认上下文，020由用户执行，真实cx_ec只读确认两个非空列、明确初始none/65536及唯一单例正常。Go的service/config公开服务默认，models/quota逐模型返回按能力限幅的默认：上下文不超过最大值，思考按等级取不超过目标的最高可用值，目标高于全部能力时取最高、低于全部档位时取最低，禁用思考为none。直接chat省略思考与thinking参数时使用同一规则；显式选择继续严格校验。

EC实际调用及聊天参数面板使用相同目录默认值，只有未手动保存时使用后台默认，不落个人配置；个人选择保持优先，已存上下文仍限幅，已撤销的个人思考选择要求重选。官方目录只接受九个正式上下文档位，自定义768K档位不混入官方配置。

Go build/vet与全量race433项通过；PHP104项、真实Vue/Ant55项及PHP7.4 lint、隔离Vue生产构建通过；EC75项真实方法加9项调用契约检查及标准Debug x64 MSBuild通过。未真实付费调用、未修改业务钱包/模型价格。官网后台PHP及254个资源FTP/HTTPS逐字节、首页与后台未登录鉴权校验通过；本机cx-ai-relay-current运行cx-ai-relay-local:eder-defaults-20261030，healthz200及成员接口401，原环境键值、端口、网络和重启策略保持，回退容器cx-ai-relay-before-eder-defaults-20261030保留。备份/发布证据位于data/eder-defaults-20261030，客户端未上传自动更新渠道。测试临时目录递归清理被自动审批拒绝，夹具留在temp，未绕过策略。


## 图片与视频生成（2026-10-30 已发布后台及本地服务）

官方/自定义对话、图片、视频默认模型独立配置；自定义图片理解和两类生成能力分开，图片/视频协议与尺寸分别保存。Go支持OpenAI/方舟图片生成及异步视频，图片按成功张数、视频按供应商成功整数秒计费，低峰/高峰/活动独立单价及成本。任务与预扣同事务，结果持久化后原子结算，失败退款、幂等、防重复创建和重启恢复均已实现。EC使用media_generation工具、原生下载和不透明token图片/MP4卡片，可列出和恢复任务；自定义任务凭据受DPAPI保护，原供应商快照不随配置改绑。详细接口和边界见[图片与视频生成](media-generation.md)。

用户已执行018及019，真实cx_ec只读核对任务表/字段/120字符ID通过，任务表为空，MySQL max_allowed_packet为1GiB。未自动修改实际模型类型、启用状态、单价或钱包；当前没有启用的生成模型，管理员配置后才能选择默认生成模型。

Go build/vet及独立MySQL8.4全量race382项通过；PHP7.4语法及实际MySQL/PHP44项、真实Vue2/Ant及计算器42项通过；EC原生/实际设置/渲染/视频Range/三语言149项及Vue生产构建、标准Debug x64主程序MSBuild通过。未调用真实付费生成接口或修改业务钱包。

官网后台PHP及254个静态资源已发布，FTP/HTTPS逐字节、首页及后台未登录鉴权核验通过。cx-ai-relay-current运行cx-ai-relay-local:media-generation-20261030，healthz200且成员接口401；原环境值、端口、网络及重启策略保持，回退容器cx-ai-relay-before-media-generation-20261030保留。首次切换的验证脚本把环境变量顺序当作差异而自动回退，改为键值比较后发布通过。备份和发布证据在data/media-generation-20261030。EC输出为CxEc/CxEc.Main/bin/x64/Debug/CxEc.exe，已包含最新嵌入式网页；未上传自动更新渠道。

DeepSeek Flash官方图片预算1024token校准包含在本次Go镜像中；真实模型的allow_images开关未修改，因电脑工具无法可靠确认当前浏览器URL而停止。部分测试temp目录的递归清理遭自动审批拒绝，仅返回blocked by policy，相关夹具保留在temp，不绕过策略。


## 多模态独立 token 计费（2026-10-30 已发布）

用户确认“含图请求整次使用独立token价格表”，不按张叠加。普通及多模态各有低峰/高峰/活动输入、缓存输入、输出价格和供应商成本，共享时间策略；独立计算器使用自身成本，允许手填覆盖，不相互改价。完整上下文含图片时选vision，包括历史追问和工具续轮；在排队前锁价，预扣和按供应商usage结算只用同一rate，快照记录billing_input_kind。后台用量明细区分价格类型和时段。

017由用户执行，真实库只读核验9售价unsigned BIGINT NOT NULL DEFAULT0、9可空unsigned BIGINT成本及ENUM(text,vision)非空快照标记，三表InnoDB、模型定价无缺失。初始复制现有普通各时段价格和成本，不改余额、账本或历史单价。当前实现无运行时兼容回填。

独立MySQL8.4 Go全量race313项、build/vet；PHP63项、真实Vue2/Ant31项及PHP lint、工作区与隔离Vue生产构建通过。覆盖实际整次usage单次计费、时段/跨午夜/活动/历史图像、较高缓存价预扣、改价后的锁价、失败退款/重复结算拒绝/快照冲突回滚、精确金额、计算器隔离、手工改价和审计原子性。未运行MySQL5.7同版本隔离验证或真实付费图片请求，未操作业务钱包。

发布备份为data/vision-pricing-20261030，客户端协议和收费回执不变，现有多模态客户端无需为价格表再次升级。 官网后台PHP与254个静态资源已发布，FTP/HTTPS逐字节、首页和后台鉴权核验通过。本地cx-ai-relay-current已切换cx-ai-relay-local:vision-pricing-20261030，healthz200、未认证models/quota/service-config401通过；环境/端口/网络和重启策略保持，回退容器cx-ai-relay-before-vision-pricing-20261030保留。


## 全局对话提示词（2026-10-29 已发布）

后台 `/ai/service` 支持全局提示词，最多10000个Unicode字符、原样保存、空文本清除。Go在每轮上下文估算和预扣之前注入唯一system消息，客户端system/developer降为user并保留内容和工具顺序；每轮读取最新配置，不修改客户端历史。会员API不返回提示词，AI审计只存长度与哈希。015由用户执行，真实库只读核验MEDIUMTEXT NOT NULL及单例记录成功。PHP39项、真实Vue/Ant20项断言通过；独立MySQL8.4.13的Go race276项通过、无失败或跳过，Go build/vet、PHP lint与隔离Vue production build通过。Vue保留现有体积与Browserslist警告；未在MySQL5.7运行隔离测试。发布备份与验证记录位于 `data/global-prompt-20261029`。

官网后台PHP与250个静态文件已发布，FTP/HTTP逐字节校验、首页及配置接口未登录鉴权通过。本地 `cx-ai-relay-current` 已切换 `cx-ai-relay-local:global-prompt-20261029`，`/healthz` 200及service-config/models/quota未认证401通过；环境、端口、网络及重启策略保持，回退容器 `cx-ai-relay-before-global-prompt-20261029` 保留。首次切换因临时探针误用`/health`自动回退，修正为实际`/healthz`后重新切换成功。未通过真实付费聊天或业务配置写入测试。

## 模型管理增强（2026-10-28 已发布）

2026-10-28 模型管理增强已发布：供应商筛选、只预览再勾选导入、模型批量删除、单个高峰区间输入、低峰/高峰/活动独立供应商成本和客户价。用户自行执行013；只读核对六个新增列成功。PHP238项、Go race258项、真实Vue/Ant交互30项通过；PHP lint、Vue production build、Go build/vet通过。250个生产静态文件FTP/HTTP逐字节一致，PHP SHA256、首页、新管理接口鉴权通过。本地Docker镜像cx-ai-relay-local:model-management-20261028健康200、未登录接口401、额度CORS预检204；环境、端口、网络和重启策略保持；回退容器cx-ai-relay-before-model-management-20261028保留。未执行生产模型导入/删除或钱包写入。 发布备份：`data/model-management-20261028-111841`。SQL 013 由用户执行，本轮没有自动执行业务库DDL。

## Forwarding transport and streaming latency (workspace)

The upstream transport retains 32 idle connections per host. SSE headers flush immediately after upstream acceptance, `X-Accel-Buffering: no` disables supporting reverse-proxy buffering, upstream streaming compression is disabled, and upstream comments become fixed flushed keepalives. Four real-socket tests with a simulated provider pass, together with the non-database suite, build and vet. Two rounds of eight concurrent TLS requests reduced second-round new connections from six to zero; in a controlled 220 ms upstream-delay case, headers arrived in about 1 ms rather than about 222 ms, while first content remained around 222 ms. These are simulated-provider measurements, not a live DeepSeek throughput improvement. This implementation is included in the 2026-10-28 model-management Docker refresh; the latency figures above remain simulated-provider measurements, not production performance measurements.

## Eder 客户端版本准入（工作区实现，尚未发布）

版本策略迁移位于 `migrations/011_eder_client_version_policy.sql`，为 `ai_eder_service_config` 增加 Eder 组件版本和软件版本禁止列表。Go 新增 `POST /v1/client/initialize`，认证后校验客户端 Eder 版本、主程序版本、SHA-256 和系统信息；后台 `/ai/service` 页面可保存两个精确版本黑名单及提示文本。EC 登录后初始化，官方聊天发送按钮按可用状态禁用；每轮对话前和 Go 聊天入口均实时重新检查服务与版本策略。需要先在目标 `cx_ec` 执行迁移 011，再协同升级 Go、后台与 EC；本条状态仅记录工作区代码，不代表迁移或生产发布已执行。

## Eder 服务开关（2026-10-27 发布状态）

新增 `migrations/010_eder_service_config.sql` 和已认证的 `GET /v1/service/config`。产品 12 AI 管理后台可设置 Eder 服务开关及最多 500 字的不可用提示，保存写入审计；Go 每次读取配置均实时查库，缺表、缺少单例或数据库异常返回 503。EC 每轮 Eder 对话调用前检查；关闭或读取失败时不请求聊天接口，显示配置文案或默认提示“当前 Eder 服务不可用，您可以绑定自定义的 AI 服务来使用 Eder。”发布状态（2026-10-27）：数据库迁移已由用户执行；官网后台 PHP 与隔离构建的前端已发布，249/249 个静态资源 HTTP 200，入口引用 `js/app.a3b4d868.js`；Go 镜像 `cx-ai-relay-local:eder-service-config-20261027` 已运行于本机 `cx-ai-relay-current`，健康检查 200、未认证接口 401、来源 CORS 预检 204，回退容器 `cx-ai-relay-before-eder-service-config-20261027` 保留。Go 仍仅绑定本机 `127.0.0.1:8088`，没有公网入口。EC 2026.00.17 Release x64 安装包已生成于 `CxEc/CxEc.Main/bin/打包/创想电商采集助手2026.00.17.exe`，未上传自动更新渠道。

## Implemented in this workspace

- 缓存命中 Token 独立计费及成本/利润率配置已发布：后台模型表单分别保存普通输入、缓存输入、输出成本（元/百万 Token）、利润率和 1000 credit 售价基准；自动计算回填客户单价后仍可手动调整，分时计费可关闭，缓存价可单独设置。Go 根据上游缓存命中数拆分普通输入与缓存输入费用，并将命中数及锁定价格存入用量和计费快照。生产 `cx_ec`（MySQL 5.7.40-log）执行 009 后，10 个新列全部存在；模型 2、价格时段 2、用量 354、快照 354 行数保持不变；新字段默认值/空成本逐行核验通过，现有客户价格与余额未由迁移更新。Go 镜像 `cx-ai-relay-local:cost-margin-20261027` 已切换至本机 `cx-ai-relay-current`，健康检查 200、未认证额度 401、EC 来源 CORS 预检 204；回退容器 `cx-ai-relay-before-cost-margin-20261027` 保留。后台 AI PHP 单文件已上传并核验；前端采用隔离构建排除未发布的授权转移路由，248/248 资源上传成功且 HTTP 200，固定资源 MD5 一致，入口引用 `js/app.b219380a.js`，设置文案位于异步分片。Go Docker build、PHP 语法及 Vue production build 通过；前端 lint 插件缺失（`command "lint" does not exist`），本轮未运行测试。后台站点已发布至官网生产；Go 服务仍只绑定本机 `127.0.0.1:8088`，未发布公网入口或 EC 客户端。

- 额度剩余比例已发布至本机 Go relay：`GET /v1/account/quota` 返回 `quota_baseline_credit` 和服务端计算的 `remaining_percent`。每个参与额度合计的钱包以最近一笔 `admin_recharge` / `order_recharge` 流水的 `balance_after_micro` 为充值后基准；充值流水与余额原本就在同一事务提交，无需新增表或列。EC 客户端只校验和展示服务端比例，不再以累计 `used_credit` 计算。只读生产核验 `cx_ec`（MySQL 5.7.40-log）确认 1 个钱包有余额、对应基准存在且不低于余额；未修改业务数据。Go `build ./...` 与 EC 主项目 Debug x64 MSBuild 均通过；本轮未运行功能测试。镜像 `cx-ai-relay-local:quota-baseline-20261027`（`sha256:16839d09db3d818110f8eab5e1d5e83d44a99a821e9c52bf41e958d0a7484b91`）已切换至 `cx-ai-relay-current`，健康检查 200、额度未认证请求 401、EC 对话来源 CORS 预检 204；回退容器为 `cx-ai-relay-before-quota-baseline-20261027`。服务仍只映射 `127.0.0.1:8088`，本次未发布 EC 客户端或公网入口。

- 模型类型已完成授权发布：`ai_model.model_type` 为 text/image/video，后台模型列表及表单支持类型选择，PHP 严格校验、持久化并写入 `model.save` 审计；Go 模型目录与额度 `pricing_models` 返回 `model_type`，聊天只接受 text，图片/视频模型在预扣及上游调用前返回 400。新 `OpenStore` 要求 008 类型列存在，不自动迁移或回填类型；`migrations/008_model_type.sql` 为既有及导入模型显式默认 text。Go `build ./...`、PHP 7.4 语法检查及 Vue production build（独立 temp 输出）均通过；Vue lint 脚本失败，提示 `command "lint" does not exist`，未通过 lint 检查。发布前生产 `cx_ec`（MySQL 5.7.40-log）已核验类型列存在且定义为 `ENUM('text','image','video') NOT NULL DEFAULT 'text'`，2 个模型均为 text；本轮未重复执行 ALTER。`cx-ai-relay-current` 已切换至 `cx-ai-relay-local:model-type-20261027`，healthz 200、额度接口未认证 401，旧容器保留为 `cx-ai-relay-before-model-type-20261027`。后台 `12/controller/ai/manage.php` FTP 上传后回读 48691 bytes 成功；前端仅发布排除授权转移路由的隔离构建，248/248 文件上传无异常，index.html 和两个固定资源 MD5 一致，线上首页 200 且引用 `js/app.eb2b31d9.js`，包含 model_type 的分片 200。FTP 初次受本机代理拦截，设置 NO_PROXY 后发布成功，未写入敏感凭据。本轮未运行功能测试，未变更会员服务或客户端；并行 007 用量保留及其他未发布更改保持各自状态。

- 额度接口 `GET /v1/account/quota` 新增 `used_credit`（已结算模型消费＋后台手动扣减）和 `total_credit`（当前本人及可用主账号钱包的实际可用余额，与 `balance_credit` 一致，不是累计充值）。金额均为精确十进制字符串，累计已用支持超过 Int64；与余额及既有百分比使用同一只读快照，排除临时预扣/退款的重复消费，拒绝非法扣减流水。既有 `used_percent` 维持模型消费/累计充值口径。独立 MySQL 8.4 全量 race 测试 220 项通过（49 个顶层、171 个子测试），无用例跳过或失败，Go build/vet 通过；覆盖 775 扣减实例、精确六位、超过 Int64、主子范围、真实预扣/退款/结算、并发快照及错误返回。未写入业务钱包；本次无 schema、管理后台或客户端变更，无需迁移。`used_credit`、`total_credit` 与保留原口径的 `used_percent` 已发布到 Go 服务 `cx-ai-relay-current`，当前镜像为 `cx-ai-relay-local:model-type-20261027`，运行二进制 SHA 与构建一致；本地健康检查 200、额度接口未认证 401、嵌入来源 CORS 预检 204。容器端口仍仅映射 `127.0.0.1:8088`；服务已在该本地路由更新不代表已完成公网暴露，外部客户端可达性尚未解决，依赖正式 HTTPS `AI_SERVICE_BASEURL` 及仓库外反向代理配置，当前仓库没有该官方入口依据。

- Go 额度接口 `used_percent` 已发布到生产 `cx-ai-relay-current`：按与可用余额一致的本人及可用主账号钱包范围，使用已结算消费 / 累计充值，排除预扣、退款和管理员扣减；无充值为 null，整数基点四舍五入至最多两位并封顶 100。`quotaSnapshot` 使用只读 REPEATABLE READ 事务，`quotaUsedPercent` 以 big.Int 处理累计账本，非法财务符号或数据库/提交异常直接返回 503。Go build、vet 与 gofmt 检查通过；独立 MySQL 8.4 全量 217 项测试通过（47 项顶层、170 项子测试）。`quota_test.go` 覆盖 number/null、四舍五入、超过 Int64 的累计量及 MIN_INT64、父钱包范围、非法账本和 SQL/余额溢出失败；通过真实生产代码路径验证 reserve/refund 不计消费、settle 计入消费，并发 REPEATABLE READ 核验旧快照余额 8/已用 20% 与新快照余额 5/已用 50% 不混用。临时目录及独立容器/卷已清理；发布前功能验证未访问业务库。当前生产容器 `cx-ai-relay-current` 使用 `cx-ai-relay-local:model-type-20261027`，运行二进制 SHA 与构建一致；回滚容器为已恢复旧二进制的 `cx-ai-relay-before-quota-percent`，旧二进制备份在 `data/quota-percent-release-20261026/relay-before`。健康检查 200、额度接口未认证 401、CORS 预检 204，无需 schema 迁移，也未新增客户端百分比界面。发布镜像基于已上线 model-policy；仅在隔离发布构建中跳过尚未执行 007 索引迁移的 `RunUsageCleanup` 启动钩子，工作区仍保留完整 retention 源码，007 用量清理未随本次镜像启用。后台减少/清零控制器及对应 AI Vue 已同时发布，其他未发布功能保持各自状态。

- 后台额度调整接口 `ai/manage/adjust_quota` 已在本地实现减少/清零：仅对既有目标钱包使用 READ COMMITTED 与行锁，核对确认余额、阻断未结算预扣，以 UUID v4 和原参数审计保护幂等；余额、`admin_deduction` 负流水与 `wallet.deduct` 审计原子提交，管理汇总独立返回 `total_deducted_credit`；扣减流水操作者与审计身份来自已认证的 `config.member.uid`，缺失时失败并回滚。Go/客户端代码与表结构不变，不需要迁移；前端已增加逐用户减少/清零、最新余额快照、确认与预览、同弹窗 UUID 重试和手动扣减统计；PHP 7.4 语法检查与 Vue production build 通过；真实 PHP 框架和独立 MySQL 8.4 集成首轮 203 项断言通过，覆盖并发、退款及预扣阻断、审计回滚、真实 `config.member.uid` 操作者和清零重试不扣后续充值。真实 Vue 2/Ant Design 本地浏览器交互 44 项断言通过，覆盖大 UID 字符串、BigInt 余额预览、明确确认、取消与待决提交防重复、同 UUID 同载荷网络重试、清零传零以及列表和已开明细刷新；布局正常，JavaScript 错误为零。本轮功能验证通过，无需 schema 迁移。功能验证仅只读查看业务钱包、账本、审计三表结构，无业务钱包写入。减少/清零控制器与对应 AI Vue 已发布，PHP 回读 MD5 一致，前端 249/249 个资源发布无失败，线上入口引用 `app.4cedc36f.js`；无需 schema 迁移。此前模型策略与输入默认参数修复的已发布状态保持。

- Go relay periodically deletes chat usage records older than 30 days at startup and every 24 hours, including their pricing snapshots; financial wallet ledger records are retained.

- 模型思考策略保存 `reasoning_efforts` 白名单与 `reasoning_format` 协议，保留 `allow_thinking` 总开关；模型目录与额度报价只公开当前有效档位，Go 在预扣前校验选择及思考参数的一致性，按 OpenAI 兼容或 DeepSeek 格式显式写入上游请求。Eder 齿轮提供白名单选择并按服务/模型保存，已选档位被撤销时要求重新选择。Go build/vet 与隔离 MySQL 9.6 的全量测试 160 项通过；PHP 7.4 语法检查、后台 Vue、EC WebView 及主项目 Debug x64 独立 `OutDir` 构建通过。客户端实际程序集检查 88 项通过（设置/目录 29 项、请求管线 59 项），真实 WebView 交互检查 32 项通过且无 JS 错误；撤销档位阻断、持久化隔离、正文/工具/摘要请求档位传递以及自定义请求保持均已核验。临时探针、SQLite、浏览器 profile 和隔离数据库容器已清理。业务 `cx_ec`（MySQL 5.7.40）已在确认无未结算预扣及活跃聊天连接后依次执行 005、006，九档字典和外键已核验；原模型既有字段及价格逐项一致，启用状态已恢复。Go 当前服务使用 `cx-ai-relay-local:model-type-20261027`（包含 model-policy 策略），保留 `cx-ai-relay-before-model-policy` 容器回退；健康检查 200，模型/额度未认证请求 401，嵌入额度 CORS 预检 204。业务库只读目录核验两个模型均为 64K 上限、可选 4K 至 64K、思考 `["high"]`。后台 AI PHP 已上传并通过 SHA256 回读，维护已解除；后台前端 248/248 个静态资源完整覆盖，上传异常为零，固定 avatar2/index/logo 的 FTP MD5 一致，线上入口已引用 `js/app.2e19fb9a.js`。独立全量 HTTP 核验 248/248 均为 200，逐文件 SHA256 全部一致、失败为零；本次模型策略后台发布及资源核验已完成，未纳入其他任务的授权转移路由。模型原既有字段、价格及启用状态最终逐项核对均保持不变。EC Release x64 构建、实际 AI DLL 哈希一致性及自解压安装包归档测试通过，本地 `CxEc.Main/bin/打包/创想电商采集助手2026.00.16.exe` 尚未上传自动更新。发布备份及核验证据位于 Go 项目 `data/model-policy-release-20261025-234021`，包含 `release-manifest.json`、`admin-publish-verification.txt` 和 `frontend-verification.json`。

- 模型上下文策略使用 `ai_model.max_context_tokens`，固定 4K 至 1M 档位；模型目录与额度报价目录公开上限及可用档位。后台模型编辑可设置并审计上限，Go 在预扣前验证输入估算并按剩余上下文限制输出。需要先执行 `migrations/005_model_context.sql`；现有及导入模型默认 64K。005 使用九档 `ai_context_tier` 字典和 `fk_ai_model_context` 外键约束，适用于业务库 MySQL 5.7.40；不依赖 CHECK。Go、PHP 语法、后台 Vue 和 EC WebView 构建通过；EC 主项目 Debug x64 使用独立 `OutDir` 构建通过，常规目录被运行中的调试程序锁定，未终止用户进程。隔离 MySQL 9.6 的全量 Go 测试 84 项通过，覆盖档位字典外键、超限不扣费及输出裁剪；实际 EC 程序集的独立 SQLite 持久化检查 23 项、真实 WebView 构建产物的界面检查 23 项通过。MySQL 5.7 镜像下载失败，未运行同版本数据库测试；race 检查因缺少 gcc 未执行。后台 lint 插件缺失，未运行 lint。业务库 005、006 已执行，Go 服务与只读目录已核验，后台静态资源完整上传并通过 248/248 HTTP 200 与 SHA256 一致性检查；服务启动仍不自动执行迁移。

- Credit 单位版本已在本地实现：原 100 万余额 TOKEN = 1000 credit，内部 1 credit = 100 万 microcredit；后台充值/价格/计算器、Go 金额 API/精确扣费、官网类型 12 和 EC decimal 显示统一切换。004 PHP 迁移覆盖余额、账本、用量、时段价、快照、审计和正式/沙盒充值套餐，并通过单位标志防止混用。2026-10-25 已完成业务库迁移及正式/沙盒官网、后台、Go 发布；当前镜像为 cx-ai-relay-local:model-type-20261027，全部入口已恢复。EC Release x64 安装包已生成，未上传自动更新。详细规范见 `docs/credit.md`。

- 官网产品 10 的类型 12 AI TOKEN 充值、指定直属子账号购买与产品 17 的主账号身份令牌已发布到官网生产，5 个文件 SHA256 回读一致；EC 使用的沙盒令牌接口也已同步并校验。Go 已切换到 `cx-ai-relay-local:aitoken-sharing`，健康检查 200、未认证额度接口 401，旧容器 `cx-ai-relay-before-aitoken` 保留回退。后台两个 PHP 文件已发布并通过哈希与未登录鉴权校验；248 个静态文件完整覆盖成功、固定文件 MD5 一致，全部资源 HTTP 200；线上首页与 AITOKEN 编辑器分片 SHA256 均与构建产物一致，入口已引用本次构建。无需数据库结构迁移，未操作业务钱包或测试充值。官网生产连接与密码来源统一记录在后台项目 `cx-admin-dev/SKILL.md` 的官网生产连接一节。

- Independent standard, daily peak, and date-limited promotional pricing with model time zones, start-inclusive/end-exclusive intervals, cross-midnight peak periods, locked reservation/settlement rates, and transactional usage price snapshots. Schema and deployment requirements are in `docs/pricing.md` and `migrations/003_model_pricing.sql`.

- `migrations/001_initial.sql` creates the AI account, wallet, ledger, provider, encrypted credential, model policy, JWT JTI, rate window, usage, and audit tables in `cx_ec`.
- Go exposes health, internal JTI registration, authenticated model/quota/usage APIs, and OpenAI-compatible chat completion with SSE proxying, model tool/thinking controls, shared per-model TPM/RPM reservation and wait-until-next-window queuing, wallet reservation, and settlement. Only the quota and chat endpoints allow CORS from the embedded EC conversation origin; internal token registration remains private. Output tokens are clamped to the model's remaining estimated TPM; requests whose prompt alone exceeds TPM are rejected.
- `cx_home/17/controller/ai/token.php` issues a short-lived JWT from the existing EC login session and registers the JTI atomically in shared `cx_ec`.
- `cx_member_new_admin` has authenticated AI administration pages for providers, models, wallet quota, provider credentials, usage, and importing models from a provider's `/models` endpoint. Create/edit forms use modal dialogs. JWTs are not managed by the admin; Go validates the server-issued tokens. The default `DeepSeek官方` provider uses `https://api.deepseek.com`; provider keys are encrypted before storage. Imported models are disabled with zero pricing until an administrator configures them.
- CxApp has an Eder/custom AI selector, keeps the existing custom API key storage, obtains the relay JWT through the EC protocol session, uses the Eder model/chat endpoints, and can query account quota.
- Each affected repository has AI integration docs; the root skill records architectures, read-first requirements, shared settings, and local URL.

## Verification and remaining setup

- 本轮 credit 验证通过：Go 构建、隔离 MySQL 全量 race 测试及 vet；迁移经济等值、跨库回滚、重复拒绝、单位标志、审计与 JSON 结构；PHP 金额/套餐/序列化 77 项、真实隔离数据库充值/事务 29 项、Node 计算器/精度 41 项；后台生产构建、EC WebView 构建与主项目 Debug x64 MSBuild；实际 EC 程序集在三种区域设置下的 decimal 解析和余额状态 96 项。业务库正式迁移通过（7 财务表、2 套餐、8 财务审计），479 个金额字段及全部套餐、审计逐项核验；248 个线上资源 HTTP 200，HTML/JS/CSS 内容与构建一致；正式/沙盒 PHP 哈希与 Go 健康/鉴权检查通过。EC Release x64 构建及自解压归档测试通过。

- 本地类型 12 与共享钱包验证通过：6 个 PHP 文件语法检查；新增 PHP 充值/归属/JWT/统计 29 项、后台套餐校验 9 项、前端套餐往返 11 项及既有套餐/升级回归、Twig 模板解析通过；后台 Vue 生产构建通过（已有体积警告，CLI 无 lint 命令）。Go `go build ./...`、启用真实隔离 MySQL 的 `go test -race -count=1 ./...` 和 `go vet ./...` 均通过，覆盖本人优先、主账号补足、余额不足回滚、原钱包退款、并发子账号、同请求并发终结、超预扣与溢出边界。已发布官网生产与 EC 沙盒令牌接口、后台生产和当前 Go 服务；文件哈希、后台鉴权、248 个静态资源 HTTP 200、首页及 AITOKEN 编辑器脚本哈希、Go 健康检查通过。未进行业务钱包测试充值或表结构修改；旧 Go 容器保留回退，临时环境已清理。

- Local Docker service `cx-ai-relay-current` runs `cx-ai-relay-local:model-type-20261027` on `127.0.0.1:8088`. Container `cx-ai-relay-before-quota-percent` retains the previous binary for rollback; its file backup is `data/quota-percent-release-20261026/relay-before`. Container `cx-ai-relay-before-model-policy` is retained for the model-policy rollback. Container `cx-ai-relay-before-aitoken` is stopped and retained for rollback. The active service includes scheduled pricing, short-lived JWT validation and sanitized customer errors. Runtime signing and provider decryption settings match the verified PHP configuration constants; secret values are never written into source or logs.
- Member token controller is deployed and hash-verified in both the official production site and `development_sandbox`, including the signed parent UID from current member records. It reads `ENCRYPTION_KEY` from `config/config.php`, requires the existing EC software session, and registers account/wallet/JTI atomically in `cx_ec`, without a remote-to-local callback. It returns only token/type/lifetime. A real isolated MySQL/PHP check verifies 15-minute TTL, signature, large UID precision, preservation of wallet/account status and rollback on duplicate JTI. PHP syntax checks pass.
- Admin PHP and pricing UI are published to production and hash-verified. Migration 003 is applied to `cx_ec`; the two existing model schedules start disabled. Independent peak and date-limited activity pricing, interval boundaries, locked settlement and transaction rollback pass Go tests and isolated MySQL integration checks.
- Go compilation/tests, Vue production build and EC Debug x64 build pass. Vue lint is unavailable because the configured CLI lint plugin is missing. EC holds temporary credentials only in memory and refreshes them before expiry; logout/account/server changes discard cached or in-flight credentials.
- EC integration uses the authenticated member session to obtain temporary JWTs and refresh account quota through `AI_SERVICE_BASEURL`. No test identities, provider keys or automatic credits are added to the business database. The new local credit client requires the coordinated 004 migration and server release.

Task temporary build/test files and the temporary admin deployment backup have been cleaned; isolated test containers have been removed.

- New public model/quota responses expose exact `input_credit_per_1k` / `output_credit_per_1k` strings; EC displays decimal credit quota and model names. Old public balance and ratio fields are removed from the new contract.

- Admin model forms accept final credit prices per thousand model tokens and write the fixed coefficient 10000 internally. The calculator uses the RMB price of 1000 credit and returns six-decimal credit prices.

- PHP registration uses the actual framework query-result contract (`row`), not native mysqli_result methods. An isolated test with the real `system/db.php` reproduces the prior failure and verifies successful issuance/JTI commit, exact UID, wallet preservation and collision rollback after the fix. The sandbox controller was uploaded and hash-verified; the live EC quota refresh then succeeded.


AI account disable: migration 012 adds the administrative disable message to ai_account. The admin quota manager can suspend/activate accounts and set a user-facing message with audit logging. Go quota responses expose status/message; authenticated chat rejects suspended accounts before reservation and returns that message. On 2026-10-27, migration 012 was applied to production cx_ec and verified on MySQL 5.7.40. Go is running in cx-ai-relay-current using cx-ai-relay-local:ai-account-disable-20261027, bound to 127.0.0.1:8088; health returned 200, unauthenticated initialize/quota returned 401, and embedded-origin CORS preflight returned 204. The previous container is retained as cx-ai-relay-before-account-disable-20261027. External HTTPS/reverse-proxy reachability is not verified.

The account-disabled chat check runs before Eder service and model policy checks so a suspended account receives its configured disable message first. Relay JSON APIs use the CxWebServer `code/msg/data/errors` envelope; chat success keeps the OpenAI protocol and errors include both the envelope and OpenAI `error` shape. EC reads `msg` first and preserves the returned message in the conversation. On 2026-10-27, Go was deployed into the existing `cx-ai-relay-current` container on `127.0.0.1:8088` by replacing its binary while preserving its environment and port binding. The release image is `cx-ai-relay-local:response-envelope-20261027`; the old binary and SHA256 manifest are retained in `data/response-envelope-release-20261027/`. Health returned the standard success envelope, unknown route returned 404 envelope, and unauthenticated model query returned 401 envelope. External HTTPS/reverse-proxy reachability and a real suspended-account request were not verified. The EC source changes build in Debug x64, but no updated EC installer or automatic-update upload was published in this server release.

Current workspace account-suspension responses prefix the administrator explanation with `您的账户异常：`. Suspension between the initial chat check and wallet reservation also returns the same HTTP 403 account error; a genuinely missing wallet remains HTTP 503. The corresponding image `cx-ai-relay-local:account-session-fix-20261027` is built, but this binary has not been deployed to the running service. EC source preserves the credential version during same-account profile/authorization refreshes and invalidates old providers on re-login, logout or account/service changes; the running desktop process must use a new build for this behavior. Go's isolated MySQL suite passes 241 tests without skips, and build/vet pass. The EC Debug x64 build and 20 assertions against the actual assembly pass, including provider use after real profile/status events and single request-ID formatting; token renewal was simulated by replacing the cached token, not by issuing a real member-token HTTP request.


## 失败请求详情

实现范围：Go对认证后聊天失败写入ai_usage，保存脱敏上游错误、状态/阶段、耗时、取消/超时原因；产品12的用量列表及用户明细提供错误详情弹窗。迁移014的两个可空列已在cx_ec核验并执行，原403条成功记录保留；用户授权后的本机Go镜像为cx-ai-relay-local:error-records-20261028，旧容器cx-ai-relay-before-errors-20261028-120540保留。PHP及隔离构建的后台已发布，250个HTTP文件逐项SHA256一致、详情接口未登录拦截通过、Go健康200。27个隔离HTTP/MySQL用例及Go/PHP/Vue构建通过，无真实付费模型测试。部署备份和清单位于data/error-records-20261028-120513。历史错误正文未保留，无法回补。

## 请求延迟与思考展示核验（2026-10-30）

客户端已支持供应商SSE的reasoning_content实时独立回调，在对话中以默认折叠的纯文本面板展示；正文、思考、工具调用保持独立，保留现有40ms界面合并。新会话的独立AI命名移至业务答复之后，不阻挡首字；命名失败明确提示并保留待命名状态，不重复执行业务。详细实现见CxApp的CxEc/.documents/cxec.main/15.采集助手与工具调用.md。Go本轮未修改，无需更新Docker或后台。

同Docker网络与代理环境下的只读测量：真实数据库SELECT1约18–25ms、参数预处理查询36–49ms；聊天鉴权/配置及事务往返静态估算约0.4–0.6秒，未执行真实钱包预扣。无凭据GET https://api.deepseek.com/models（401、无模型推理/费用）冷连接首响应2.5–6.1秒、复用约0.69–0.75秒，其中一次DNS48.8ms/TCP1.4ms/TLS1776.7ms；这是网络连通性样本，不代表真实聊天首token时间。采样时模型限流未满，现有日志无法确认历史请求是否排队。Go逐SSE事件Flush，choices内思考字段保留，不等待整段完成才转发。

验证：CxEc主项目Debug x64（隔离与标准本地输出）构建成功，WebView Vite生产构建成功；原始SSE25项、Vue折叠12项断言及真实AgentRunner模拟HTTP工具/会话恢复smoke通过。完整WPF业务命名失败路径仅核对源码，未做端到端UI测试；未使用真实付费聊天或修改业务数据。可运行本地新构建CxApp/CxEc/CxEc.Main/bin/x64/Debug/CxEc.exe；未发布自动更新安装包。

## 多模态图片输入（2026-10-30）

新增ai_model.allow_images，用户执行016后只读核验真实库字段为TINYINT(1) NOT NULL DEFAULT0；134个既有模型默认关闭，没有自动开启供应商能力。后台模型管理可勾选图片输入，仅text对话模型允许，保存和审计原子提交，严格拒绝缺失/null/非字符串开关。Go目录和quota返回布尔能力，聊天在预扣之前验证PNG/JPEG data URL、真实图片、模型指定单张大小、单边2048px、请求总32MiB/32张历史上限；拒绝远程URL和不支持的模型。图片原样转发，估算每图8192tokens，按上游usage及原有token单价结算，不按base64文本收费。

EC支持选择/粘贴、移除、纯图发送、队列/后台会话/中断/重试和历史缩略图查看；自定义模型有独立AllowImages。图片进入SDK DataContent并随工具续轮及会话恢复，不写业务日志。缓存上限128MiB/历史256MiB覆盖图片的多份序列化。源码与配置文档已同步，Debug x64本地主程序及两个Vue生产构建通过（保留项目既有警告）；后续复核碰到另一正在编辑商品组件的C#9语法，改为C#7.3等价写法后最终主构建成功，没有回退他人业务改动。

隔离MySQL8.4的Go race301项、build/vet，PHP45、后台真实Vue15、EC主程序集19/主DOM20/图片Vue7项断言及真实SDK图片HTTP/工具续轮/恢复/估算检查通过；修复恰好4MiB JPEG前缀长度边界后新标准bin补验5项通过。浏览器图像解码在DOM测试中使用替身，真实图像由C#/Go解码验证；完整WPF交互未运行，没有真实付费图片请求或业务钱包写入。发布备份为data/multimodal-20261030，客户端标准bin为CxApp/CxEc/CxEc.Main/bin/x64/Debug/CxEc.exe，未发布客户端自动更新包。

已发布官网后台PHP与254个静态文件，FTP全量字节回读、HTTPS全量字节回读、首页与后台鉴权通过；站点HTTP现301到HTTPS，回读校验使用https://cxadminn.cxsup.com。本地cx-ai-relay-current运行cx-ai-relay-local:multimodal-20261030，healthz200及未认证models/quota/service-config401通过，环境/端口/网络/重启策略保持，回退容器cx-ai-relay-before-multimodal-20261030保留。EC源HTML及Vite构建确认CSP允许data图片来源，避免选图解码与缩略图被浏览器策略阻止。


## 模型图片文件大小限制

021_model_image_size.sql增加ai_model.max_image_bytes（INT UNSIGNED NOT NULL，默认4194304）。模型目录及额度返回max_image_bytes，服务端严格要求1024–33554432且为1024倍数；配置无效则拒绝目录/请求。每张图片在预扣前按模型限制校验，历史加当前图片合计32MiB，最多32张、单边2048px；Chat JSON上限64MiB。EC按模型max_images_per_request校验单次图片张数，官方读取目录限制，自定义按模型MaxImagesPerRequest设置；单张大小按MaxImageKilobytes设置。大小配置不改变token/按张计费规则。

模型图片限制及分组按钮已发布至官网后台，PHP与254静态资源均FTP/HTTPS逐文件内容核对，鉴权检查通过。本地Docker为cx-ai-relay-local:model-image-limits-20261030，健康、鉴权和运行环境一致性通过，回退容器cx-ai-relay-before-model-image-limits-20261030保留。Go race440、PHP28、真实后台Vue127、EC82项检查通过；Go build/vet、两项Vue生产构建、EC主项目Debug x64标准目录构建成功。未发布客户端自动更新、未调用真实付费模型、未修改价格或余额；证据data/model-image-limits-20261030。临时测试目录清理被自动审批拒绝（blocked by policy），保留temp且未绕过。


## 模型单次图片张数

ai_model.max_images_per_request为每次实际模型请求的图片总数上限，范围1–32，默认4，包含随请求发送的历史图片；不是只限制当前新添加的附件。022_model_image_count.sql由用户备份ai_model后执行，新增非空unsigned TINYINT，已有模型初始化4，不改图片大小、价格或钱包。导入的新模型沿用数据库默认4。

后台在图片输入能力区域编辑该字段，保存严格要求字符串整数1–32，与大小、模型参数及审计在同一事务提交。模型目录和quota.pricing_models返回整数max_images_per_request，缺失或无效配置明确失败。Go启动要求022字段存在，聊天在预扣之前检查完整messages的总图片数；超限不调用供应商、不扣费。发布顺序为022→Go及后台→EC，不能在字段缺失时发布新接口。

EC选图、粘贴、草稿发送、队列分发和插队按当前模型上限检查新附件；自定义模型MaxImagesPerRequest默认4并可在设置中修改，官方读取模型目录。原生图片入口最多接收平台上限32张，实际发送前按模型再次校验。SDK对每次准备发送的完整消息进行计数，覆盖历史导入、会话恢复和工具续轮，自定义AI同样受限；超限显示本地化image_too_many，不自动删除历史或重试。图片总字节32MiB、单边2048px、单张大小配置保持独立生效。


模型单次图片张数配置已发布官网后台与本地Go，SQL022由用户执行并核验为非空unsigned TINYINT默认4，134个模型值均合法。当前镜像cx-ai-relay-local:model-image-count-20261031，回退容器cx-ai-relay-before-model-image-count-20261031保留；健康、鉴权和运行环境检查通过，PHP与254静态资源FTP/HTTPS逐文件内容核对通过。EC标准Debug x64已构建更新，自动更新包未发布；相关回归Go451、后台229、EC142项及最终Go定向11项均通过，证据data/model-image-count-20261031。

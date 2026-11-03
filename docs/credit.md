# Credit 计费单位

## 单位与精度

- 余额使用 **credit**，模型输入与输出用量仍使用 **token**。
- 原 1,000,000 余额 TOKEN 等值转换为 1,000 credit，即 1 credit = 原 1,000 余额 TOKEN。
- 内部 1 credit = 1,000,000 microcredit，所有余额、价格、预扣、结算和账本按整数 microcredit 运算，最小扣费为 0.000001 credit。
- 数据库现有 `*_micro`、`*_micro_per_1k` 字段现在表示真正的 microcredit。迁移将原整数值乘以 1000，保留金额经济价值；模型 token 数、人民币售价和购买份数不变。

例如，原余额 1,000,000 迁移后存储 1,000,000,000 microcredit，对外为 `"1000"` credit。原每千输入价 320 迁移后存储 320,000，对外为 `"0.32"` credit / 千模型 token。

## 扣费与计算器

金额计算不经过浮点数：

```text
扣费 microcredit = ceil((输入 token × 输入每千 microcredit 单价
                       + 输出 token × 输出每千 microcredit 单价)
                      × 内部系数 / (1000 × 10000))
```

内部系数由后台固定为 10000，不接受请求调整；模型的常规、高峰和活动价格都使用同一单位。Go 使用任意精度整数计算中间结果，最终金额超过 Int64 范围立即失败。本人优先、主账号补足、原钱包退款和价格锁定规则保持一致。

后台计算器填写“1000 credit 售价（元）”及“每百万模型 token 的成本和目标毛利（元）”：

```text
每千模型 token 的 credit 单价 = (每百万成本 + 每百万目标毛利) / 1000 credit 售价
```

结果向上取整到 0.000001 credit。例如售价 5 元、输入成本 1 元、目标毛利 0.6 元，得到每千输入 0.32 credit。金额最多六位小数，超精度、负数或越界金额直接拒绝；credit 文本输入不接受科学计数法。

## API 与充值

额度 API 返回 `balance_credit`、`self_balance_credit`、`parent_balance_credit` 和 `unit: "credit"`，金额为精确十进制字符串。用量返回 `charged_credit`；公开报价返回 `input_credit_per_1k`、`output_credit_per_1k`，不再返回旧余额字段或 token 消耗倍率。

后台模型价格输入 `input_price`、`output_price` 及对应时段字段均为 credit / 千模型 token；充值输入为 `recharge_credit`。后台响应将财务字段转换为 `*_credit` / `*_credit_per_1k` 字符串，不暴露内部整数供前端自行猜测换算。

官网发货类型 12 配置为 `{"12":[{"credits":"1000"}]}`，每份金额最多六位小数，按购买份数相乘，以 microcredit 入账。购买记录显示实际 credit 数额，默认本人或已验证归属的直属子账号。

EC 使用 decimal 解析 credit 字符串，显示最多六位小数；WebView 不把余额转为 JavaScript number。旧单位响应、负余额或非法小数触发友好错误，不按新单位解释旧数字。

## 额度已用百分比

`used_percent` is the historical metric for settled model consumption divided by cumulative recharge. It is distinct from and is not used for the remaining quota gauge.

`GET /v1/account/quota` 的 `used_percent` 是 JSON number，范围 0–100，按整数基点四舍五入保留最多两位小数；没有累计充值时返回 `null`。该字段不是金额字符串，`balance_credit` 等金额仍保持原精确字符串格式。

口径为已结算的实际消费 / 累计充值：分子只累加 `settlement`、`settlement_parent` 的负数账本金额，分母只累加 `admin_recharge`、`order_recharge` 的正数金额，均按整数 microcredit 比较。预扣、退款/释放和 `admin_deduction` 不参与计算，手动减少或清零余额不会被伪装成聊天消费。比例大于 100 时公开值为 100；没有充值分母时不能用余额或默认值伪造比例。

范围与可用余额相同，包含本人及当前可用主账号钱包的历史充值/结算，主账号自身不重复计数；主账号停用、账号或钱包缺失时不纳入其历史。余额和统计共享只读 REPEATABLE READ 快照。累计账本可能超过单个钱包 Int64 范围，Go 以 `big.Int` 计算比例；余额本身仍校验 Int64 溢出及负数。负充值、正结算、非法累计值或数据库/提交失败直接返回额度不可用，不回填百分比。统计依赖财务账本，不因 30 天聊天用量清理而丢失历史。

## 额度剩余比例

额度接口另外返回 `quota_baseline_credit` 和 `remaining_percent`。服务器对本人及当前可用主账号钱包，分别读取最近一笔 `admin_recharge` / `order_recharge` 流水的 `balance_after_micro`，将可用钱包的基准相加后计算 `当前可用余额 / 充值后基准余额 × 100`，四舍五入到最多两位小数。充值流水与余额在同一事务写入，因此无需新增表或列；后续每次充值成功的新流水自动成为该钱包的新基准，包含充值前剩余余额。消费、退款/释放和后台扣减不重置基准。没有基准时 `remaining_percent` 为 null；当前余额超过基准属于数据异常，额度接口返回 503。

EC 客户端只校验和展示服务端返回的比例，不从累计已用金额重新计算。

## 后台减少与清零

后台 `ai/manage/adjust_quota` 沿用管理员鉴权和既有 `cx_ec` 钱包、账本、审计，不新增表结构或 Go 用户接口。`operation=reduce` 按 `reduce_credit` 精确扣减正数；`operation=clear` 要求 `reduce_credit="0"`，将当前已确认的目标钱包余额扣到零。两种模式均提交 `expected_balance_credit` 快照和 UUID v4 `adjustment_id`；金额最多六位小数，内部转换为整数 microcredit，不经浮点数。

事务采用 READ COMMITTED，先对已有目标钱包加行锁，再读取账本终态并核对确认快照。不存在的钱包、负余额、余额已变化、余额不足、已为零的清零，以及该钱包仍有未结算 `reserve` 或 `reserve_parent` 都直接失败。对子账号操作只修改子账号自己的钱包；清零主账号自己的钱包也不修改子账号钱包，不删除账户、Token 或历史用量。不能通过管理员扣减绕开正在进行的预扣和结算。

成功扣减以统一原因 `admin_deduction` 写负数 `delta_micro`，同时写 `wallet.deduct` 审计，包含 `adjustment_id`、`operation`、`requested_credit`、`expected_balance_credit`、`deducted_credit`、`balance_before_credit` 和 `balance_after_credit`。余额、账本和审计在同一事务内提交，任一步失败回滚。流水 `operator_id` 与审计 `admin_id` 均使用后台已认证的 `config.member.uid`，不写占位管理员；身份缺失或无效时直接失败并回滚。重复回执还须核对审计的实际扣减额及前后余额与账本完全一致。相同 UUID 的同参数重试返回原扣减及余额回执；更换 UID、模式、金额或确认快照拒绝，不再扣一次，也不能把清零重试解释成另一次新清零。

管理统计将此类流水单独累计为 `total_deducted_credit`，不计入模型实际消费、模型 token、结算次数或累计充值。`get_accounts` 和 `get_account_usage` 使用同一账本汇总；消费进度仍为模型结算消费 / 充值入账。减少与清零是明确的财务调整，不通过伪造聊天用量或负充值实现。

## 迁移与发布

入口为 `migrations/004_credit_units.php`，使用既有格式的 `DATABASE_DSN`，必须指向 `cx_ec`。默认仅只读预览：

```text
php migrations/004_credit_units.php
php migrations/004_credit_units.php --apply
```

当前已核对的数据库为 MySQL 5.7，财务库 `cx_ec`、正式会员/官网库 `cx_member` / `cx_home_new`、沙盒库 `sb_cx_member` / `sb_cx_home_new` 的目标表均为 InnoDB。迁移前再次核对连接及库名、备份这些库，并暂停 Go 聊天、后台充值/改价和官网支付发货；处理完所有未结算预扣再执行。

迁移检查金额范围、负余额、未完成预扣和 JSON 单位，统一换算七张财务表、所有审计 JSON 金额快照、两个会员库的套餐、两个官网库的自定义套餐及类型 12 发货记录。非金额字段、JSON 对象/数组结构、token 统计、人民币价格、订单身份与时间不变。

`004_credit_units.sql` 只建立 `ai_billing_unit` 标志表，不能替代 PHP 迁移。创建表是独立 DDL；金额更新和完成标志写入在同一个跨库事务内，任一步失败即回滚。发生失败后标志表可能为空，应查明原因；不得手工填完成标志。标志表的存储引擎、主键和字段类型都必须符合定义，已有完成标志时拒绝重复执行，避免二次放大。

迁移完成后部署 Go、后台、官网及 EC 新版本，再恢复充值与聊天。Go 启动、后台 AI 管理和官网充值要求单位标志为 credit/1000000；缺失时直接失败。不能混用旧服务和新数据库。恢复业务前可以同时还原数据库备份和全套旧版本；一旦已发生新单位交易，不能简单除以 1000 或只回退旧容器，必须依据完整账务记录制定回滚方案。

2026-10-25 已暂停全部入口并停止旧 Go，保存账务与远端文件备份后执行 `--apply`：7 张财务表、2 份充值套餐、8 条财务审计完成转换，479 个金额字段逐项核对为原值的 1000 倍，非金额字段及官网其余数据保持一致。正式/沙盒官网、后台 PHP 与全部 248 个前端资源已发布并回读验证，Go 使用 `cx-ai-relay-local:credit-units`，健康检查和未登录鉴权通过，入口已恢复。备份保存在 `data/credit-release-20261025-150348/`。EC Release x64 已构建并生成本地安装包；未执行客户端自动更新上传。

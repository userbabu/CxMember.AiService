# 图片与视频生成

生成能力与图片理解分开：text 模型的 allow_images 决定图片输入；image/video 模型通过 generation_api 和 generation_size 配置独立生成接口。生成模型不接受 chat/completions，普通对话模型目录应只选择 text。管理员导入模型后显式修改类型、协议、尺寸和单价，再启用；不根据模型名称猜测能力。

## 接口与调用

所有接口沿用会员 JWT、产品范围和账户身份；创建额外检查服务开关及客户端版本。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| POST | /v1/generations | model、prompt、units、client_info，X-Request-ID 为幂等编号 |
| GET | /v1/generations | 当前用户最近20个任务的摘要，不返回密钥、供应商地址或资产正文 |
| GET | /v1/generations/{id} | 当前用户任务、成功资产、数量和精确 credit 回执 |
| GET | /v1/generations/{id}/content | 原生客户端下载已完成的 OpenAI 视频，仅使用原任务加密凭据 |

普通响应为 code/msg/data/errors。创建返回 submitting/queued/completed/failed；同编号同用户同 model/prompt/units 只返回原任务，参数冲突返回409。图片 units 为1–4；方舟图片仅1；视频为4–12整数秒，OpenAI仅4/8/12。提示词最多16000字节；生成尺寸在后台配置，客户端不能提高分辨率改变成本。

供应商 Base URL 是 API 根目录，接口路径直接追加：OpenAI通常 https://api.openai.com/v1；方舟通常 https://ark.cn-beijing.volces.com/api/v3。不能填完整 chat/completions 地址。支持 openai-images、ark-images、openai-video、ark-video；OpenAI视频使用 multipart，方舟视频使用 content 文本数组及 duration/resolution。创建不自动重试，不携带密钥跟随重定向。

## 计费与恢复

迁移018增加生成配置、六个分时价格/成本字段、生成任务和计费快照；019将任务模型ID上限统一为120字符。SQL由用户执行；启动检查必要元数据及 MySQL max_allowed_packet 至少64MiB。当前真实库只读核验为1GiB。

图片售价单位 credit/张，视频售价 credit/秒；成本单位元/张或元/秒。活动优先于高峰，其他时间低峰，共用时区和单个每日区间。单价是最终客户价，不叠加 token 计费或旧倍率；有效时段必须配置正数。计算器按成本×(1+利润率)×1000÷每1000credit售价换算，向上取整到 microcredit。

排队前锁定价格，任务与钱包预扣同事务落库，自己钱包优先、签名主账号共享钱包其次。成功按供应商返回图片数量或视频 duration/seconds 整数结算，多退少补中的实际金额不得超过预扣；无可信数量则失败退款，不估算成功时长。方舟返回的 duration 是供应商整数秒口径，可能与按视频帧计算的播放时长略有差异。

成功资产先持久化 result_json/actual_units，再将任务终态、实际钱包变化、用量及生成单价快照原子提交；结算失败由后台重试，不重新创建付费任务。视频任务持久轮询，与客户端断开独立。15分钟仍处 submitting 的未知提交结果退款且不重发；48小时未完成任务退款。终态任务保留30天，按小时分批清理，钱包账本不删除。

图片JSON响应最多48MiB，单张base64解码最多8MiB，PNG/JPEG尺寸不超过16384边长及3200万像素；供应商HTTPS资产URL保留原地址，可能过期。OpenAI视频下载代理最多256MiB。历史只保存加密凭据；非OpenAI视频的完成任务清除密钥副本，OpenAI视频为后续下载保留原任务加密凭据，模型改绑不会串用新供应商密钥。

## EC 配置与显示

官方和自定义各保存对话、图片、视频默认模型；自定义保留现有供应商/模型选择作为对话默认，图片/视频使用独立配置ID。自定义模型图片输入、图片生成、视频生成是独立能力，图片/视频生成协议及尺寸也分别配置。免费版对话仍只允许官方服务。

media_generation 技能提供 generate_image、generate_video、get_media_generation、list_media_generations。只在用户要求生成时调用默认模型；未配置明确失败，不自动选择另一个付费模型。图片立即显示占位卡并在客户端后台生成、自动更新成图；视频等待约一分钟后返回持久任务编号，可列出和恢复查询，不能因 pending 重复提交。

官方生成由Go扣额度；自定义请求直接调用供应商，由供应商计费，不重复扣官方额度。自定义视频任务在本地按登录UID保存原地址、原协议和受Windows DPAPI保护的密钥/代理快照；停止或重启后仍可查询，后续配置变更不改投其他供应商。

原生下载图片到 app_data/EderMedia/{uid}/Images，视频与任务元数据保存在上级账号目录；图片生成/载入时显示颜料翻滚动画，失败显示失败状态，成图支持原生复制与另存为。网页只接收不透明token，图片预览和MP4视频播放复用同源资源入口，支持字节范围请求。账号、授权实例或官方服务地址在途变化会拒绝晚到响应，不能把旧账号结果写入新账号。待完成的官方生成不展示不完整的本轮总消耗，历史查询回执不重复计入新一轮费用。

## 协议依据

- [OpenAI图片生成](https://developers.openai.com/api/reference/resources/images/methods/generate)
- [OpenAI视频创建](https://developers.openai.com/api/reference/resources/videos/methods/create)
- [方舟视频创建](https://docs.volcengine.com/docs/ark/create-video-generation-task-api?lang=zh)
- [方舟视频查询](https://console.volcengine.com/ark/region%3Acn-beijing/docs/get-video-generation-task-api)

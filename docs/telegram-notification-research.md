# Telegram 监控通知样式调研

## 结论

成熟项目的共同做法不是把更多字段堆进消息，而是先让状态一眼可见，再按重要性逐层展开：标题表达事件和数量，正文只保留短列表，诊断信息放在次级区域，跳转操作交给按钮。对本项目最合适的基准样式来自 Smart Group Bot：粗体标题、空行分区、引用块承载紧凑字段、可展开引用块收纳详情，并避免装饰性分隔线。

建议将 Telegram 通知固定为三种版式：实时异常、实时恢复、每日全模型状态。三者共享同一套视觉语法，但展示不同信息；日报必须逐项覆盖订阅内的所有模型，不能只列异常模型。

## 主要样式来源：Smart Group Bot

Smart Group Bot 将系统消息抽象成少数几种稳定布局，最值得复用的细节如下：

- 标题仅使用一行粗体文本，标题与正文之间留一个空行，不使用横线、字符框或连续 emoji 装饰。[`render_summary_notice`](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L419-L448) 按“标题、摘要引用块、上下文引用块、强调内容、可展开详情”的顺序组装消息。
- 字段行使用 `<b>label</b>　value`，标签和值之间是一个全角空格，不尝试用普通空格手工对齐比例字体。[字段实现](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L332-L357)
- 摘要或元数据放进普通 `<blockquote>`；长证据或解释放进 `<blockquote expandable>`，默认折叠。[可展开详情实现](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L405-L448)
- 列表型数据采用“标题、元数据引用块、普通文本列表、页脚”，列表不再为每项重复字段名。[`render_data_brief`](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L528-L566)
- 行动入口使用内联按钮，可按行排列 URL、复制、分享和关闭动作；按钮文字保持短促。[按钮构造](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L152-L185)
- HTML 格式发送失败时降级为纯文本，按钮失败时去掉按钮重试，并默认关闭网页预览，保证通知正文优先送达。[发送降级逻辑](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/message_templates.py#L193-L250)
- 项目明确规定：短消息不要为了装饰添加标题、表格和分隔线；逻辑块之间只留一个空行，不堆叠分隔线。[排版规则](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/bot/services/reply_output.py#L28-L37)

其测试用例展示了最终结构：`<b>标题</b>` 后接普通引用块和可展开引用块，字段使用粗体标签与全角空格，而不是模拟表格。[渲染快照](https://github.com/Hamster-Prime/Smart_Group_Bot/blob/25e2c5ea498e534ceb7bcf5dba1ca6bb0b3a8291/tests/test_message_template.py#L49-L136)

## 监控项目对照

### Healthchecks

Healthchecks 的第一行直接用红/绿圆点、检查名和 `DOWN`/`UP` 表达状态；恢复时紧接停机时长。项目、标签、周期、最近 ping 等信息随后再展示。[Telegram 模板](https://github.com/healthchecks/healthchecks/blob/49653c350cddc47fc00a471bd1b08b5771a7967c/hc/integrations/telegram/templates/telegram_message.html#L1-L40)

它还在单次事件尾部补充其他仍异常的检查：不超过 10 个时逐项列出，超过 10 个只显示数量。这说明事件通知应给出必要的整体上下文，但必须设数量上限。[异常检查汇总](https://github.com/healthchecks/healthchecks/blob/49653c350cddc47fc00a471bd1b08b5771a7967c/hc/integrations/telegram/templates/telegram_message.html#L42-L56)

可直接复用的原则：状态色放在第一行；恢复消息必须优先显示故障持续时间；附带上下文要限量，不展开所有模型的完整指标。

Healthchecks 的官方通知文档还说明，其周期报告会按项目覆盖所有检查，并列出检查当前状态、周期内故障次数和总故障时长。[Weekly and Monthly Reports](https://healthchecks.io/docs/configuring_notifications/#weekly-and-monthly-reports) 这为本项目日报提供了直接的信息模型依据：日报主体是全部订阅模型的状态清单，故障数据只是每个模型的补充，而不是用“异常模型列表”代替日报。

### Uptime Kuma

Uptime Kuma 的默认事件正文只有 `[monitor name] [emoji status] detail`，状态分别为 `🔴 Down`、`🟢 Up` 等，说明实时告警首先追求可扫读，而不是统计报表。[消息构造](https://github.com/louislam/uptime-kuma/blob/b980621689b2e3b978dcdd3a99a3ad8cf81c9b9b/server/model/monitor.js#L1460-L1464)

Telegram 发送端默认关闭链接预览，并允许模板化后再选择解析模式。[Telegram provider](https://github.com/louislam/uptime-kuma/blob/b980621689b2e3b978dcdd3a99a3ad8cf81c9b9b/server/notification-providers/telegram.js#L51-L104)

可直接复用的原则：模型名与状态必须在第一屏出现；状态页 URL 不应生成占空间的网页预览。

### Gatus

Gatus 将消息分为 triggered 和 resolved 两种状态，并显示连续失败/成功阈值；每个条件结果只用一行 `✅` 或 `❌` 加条件文本。[Telegram 请求正文](https://github.com/TwiN/gatus/blob/0fd2520088f839ae194b8c4108a66c409a47b7cf/alerting/provider/telegram/telegram.go#L127-L159)

可直接复用的原则：异常原因适合做逐行短结果；连续失败次数是有用的可信度信息，但不应为每个模型重复打印完整日统计。

### Prometheus Alertmanager

Alertmanager 按 `Alerts Firing` 和 `Alerts Resolved` 分组，再在组内枚举告警，而不是为同一批状态变化发送多个结构完全重复的消息。[Telegram 默认模板](https://github.com/prometheus/alertmanager/blob/ee6b5f4aba167fc95f9600b5ee29f26cd30c53fc/template/default.tmpl#L116-L125)

其通用明细会输出全部 labels、annotations 和 source，信息完整但不适合本项目直接照搬；对本项目有价值的是“先按状态分组、再列对象”的聚合方式。[通用告警明细](https://github.com/prometheus/alertmanager/blob/ee6b5f4aba167fc95f9600b5ee29f26cd30c53fc/template/default.tmpl#L7-L22)

Alertmanager 默认使用 HTML，关闭网页预览，并针对 Telegram 长度限制处理超长消息。[Telegram notifier](https://github.com/prometheus/alertmanager/blob/ee6b5f4aba167fc95f9600b5ee29f26cd30c53fc/notify/telegram/telegram.go#L83-L123)

## Telegram 官方约束

- `sendMessage` 的正文在实体解析后为 1 至 4096 个字符；超长日报必须按完整模型条目拆分，不能在条目中间截断。[Bot API `sendMessage`](https://core.telegram.org/bots/api#sendmessage)
- 官方 HTML 格式支持普通和可展开的 `<blockquote>`，可以把低频查看的故障原因折叠起来。[Bot API formatting options](https://core.telegram.org/bots/api#html-style)
- `link_preview_options`、`disable_notification` 和 `reply_markup` 都是 `sendMessage` 的正式参数。实时异常可以正常提醒，恢复与日报可根据产品策略静默；状态页入口应使用内联按钮并关闭链接预览。[Bot API `sendMessage`](https://core.telegram.org/bots/api#sendmessage)

## 推荐模板

下列模板沿用 Smart Group Bot 的“标题、引用摘要、普通列表、可展开详情、按钮”结构。示例文本是最终呈现，不是要求使用字符对齐的表格。

### 实时异常

```text
🔴 模型异常 · 2

OpenAI / GPT-5
Anthropic / Claude Opus 4

检测于 14:06 · 连续 3 次失败

[查看状态页]
```

HTML 结构建议：标题使用 `<b>`；模型列表放入一个普通 `<blockquote>`，每个模型一行；时间与连续失败次数放在列表后；各模型的短错误原因放入一个 `<blockquote expandable>`，仅在存在有效原因时添加；状态页使用单个 URL 内联按钮。不要附加每个模型的当日运行时长、异常时长、故障次数和可用率。

字段顺序固定为：事件与数量、模型列表、确认信息、折叠原因、状态页按钮。异常事件保留声音提醒。

### 实时恢复

```text
🟢 模型恢复 · 2

OpenAI / GPT-5　2m 14s
Anthropic / Claude Opus 4　8m 03s

恢复于 14:14

[查看状态页]
```

HTML 结构与异常相同，但每个模型行只额外显示本次故障持续时间。字段顺序固定为：事件与数量、模型和持续时间、恢复时间、状态页按钮。恢复消息不再重复日累计指标；可考虑静默发送，减少恢复风暴带来的打扰。

### 每日全模型状态

```text
📊 模型运行日报 · 2026-08-16

模型　24
状态　🟢 21　🟡 2　🔴 1　⚪ 0
可用率　99.72%

OpenAI
🟢 GPT-5　100%
🟡 GPT-4.1　99.82% · 2 次 / 2m 37s

Anthropic
🟢 Claude Sonnet 4　100%
🔴 Claude Opus 4　96.20% · 1 次 / 54m 43s

Google
⚪ Gemini 2.5 Pro　无数据

[查看完整状态]
```

日报必须列出订阅范围内的每个模型。摘要放在普通 `<blockquote>` 中，字段顺序固定为模型总数、状态计数、整体可用率；之后按提供商分组，每个模型只占一行。正常模型只显示日可用率；发生过异常的模型追加“故障次数 / 累计异常时长”；无数据模型明确显示“无数据”。

日报图标定义应稳定：`🟢` 表示整日无异常，`🟡` 表示当日发生过异常但日终已恢复，`🔴` 表示日终仍异常，`⚪` 表示没有足够观测数据。该定义同时表达 N 日整体表现和日终状态，不会把“曾异常但已恢复”误写成完全正常。

列表不要使用 Markdown 表格或手工补空格对齐；模型名长短不一，Telegram 客户端使用比例字体，伪表格在不同设备上会错位。百分比可以使用 `<code>` 保持局部数字清晰。超过 4096 字符时按提供商边界优先分片，必要时再按完整模型行分片；每片标题添加 `(1/2)`，摘要只在首片出现，状态页按钮只放在末片。

## 不采用的做法

- 不为每个模型重复“提供商、模型、运行时长、异常时长、故障次数、可用率”六行字段；这正是当前低密度问题的来源。
- 不使用长横线、方框字符、多个装饰 emoji 或 Markdown 表格。指定参考项目明确反对为了装饰增加结构。
- 不在实时事件中混入日报指标。实时消息回答“什么刚刚坏了或恢复了”，日报回答“N 日所有模型表现如何”。
- 不只列异常模型。每日汇报的价值是快速确认整个订阅范围，异常只是模型行上的附加信息。
- 不把状态页 URL 直接塞进正文并生成预览；使用一个内联按钮并关闭预览。

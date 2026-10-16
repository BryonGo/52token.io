# 外站模型价格与 52token 采购成本对比

生成时间：2026-10-16T20:44:46+07:00（Asia/Bangkok）。范围：开发环境，只读比较，没有修改渠道、分组或价格。

## 数据与计算口径

- 外站数据来自用户提供的《外站模型价格测试对比》，包含 43 条记录。按模型准确 ID 匹配到 19 条、10 个不同模型；24 条未匹配记录不推算、不借用近似版本。
- 只去掉外站渠道标签（`渠道/模型`、`模型@渠道`）和“正常／最低／旧配置”等表格备注，保留模型的版本、日期和变体。外站渠道与我方渠道是不同采购线路，名称不要求相同，也不据此宣称服务质量相同。
- 我方成本读取开发环境 `GET /api/pricing` 的渠道成本快照。HappyAPI 使用已同步的人民币成本；POJIA 的公开快照是基准价，本报告使用已核实的采购倍率 0.08 折算一次，充值比例为 1:1。官方 DeepSeek 使用快照中当前空闲价格。
- 原文充值口径：春水、Rili、桃花岛每元 10000 单位；AI 风月每元 8333.34 积分。不含赠送。以下按原文系数解释为“积分或花瓣／token”。AI 风月按 8333.34 精确计算，表格显示保留最多六位小数。
- 主表价格单位统一为 **人民币／百万 token**，每个价格对均为 **普通输入 / 输出**。未包含缓存、批处理优惠、工具费用或逐轮 ceil/floor 尾差。
- **差额 = 外站单价 − 我方采购成本；正数表示外站贵，负数表示外站便宜。倍数 = 外站 / 我方。** 这是零售单价与采购成本的比较，不能当作同一网站利润率，也不能据此断言回复质量、模型来源和计量口径等价。
- 外站多数记录缺少输入、输出及缓存明细，不能从“平均每轮多少钱”反推出我方同轮准确成本。本报告保留其系数及证据备注，不编造 token 数。
- GPT 主表列短上下文档，其长上下文档单独附列；原文未给上下文长度，不能据此判断那些测试轮次适用哪档。POJIA 峰时价格仍未取得实账验证，不推断高峰价。
- 我方“采购成本”指目前渠道采购参数与上游价格同步所得的单价。POJIA DeepSeek 已做实账核对；本次没有对所有 HappyAPI 型号逐个发起付费验证，也没有访问或复核外站会话链接。

换算公式：

```text
外站人民币单价／百万 token = 页面系数 × 1,000,000 / 每元获得单位数
我方普通渠道成本 = 已同步的人民币成本价
我方 POJIA 成本 = 已同步人民币基准价 × 0.08（只乘一次）
```

## 我方匹配渠道的基准

| 我方渠道 | 准确模型 ID | 采购成本输入 / 输出（¥/1M） | 条件 |
| --- | --- | ---: | --- |
| happyapi-x1-genmini（129） | `gemini-2.5-flash` | 0.08 / 0.32 | standard |
| happyapi-x1-claude（128） | `claude-opus-4-5-20251101` | 1 / 5 | standard |
| happyapi-x1-claude（128） | `claude-opus-4-6` | 1 / 5 | standard |
| happyapi-x1-claude（128） | `claude-opus-4-7` | 1 / 5 | standard |
| happyapi-x1-genmini（129） | `gemini-3-flash-preview` | 0.1 / 0.6 | standard |
| POJIAAI-DeepSeek（134） | `deepseek-v4.1-flash` | 0.08 / 0.32 | standard；已乘一次 0.08；空闲基准 |
| happyapi-x1（126） | `gpt-5.6-terra` | 0.4 / 2.4 | 上下文 ≤ 272000 |
| happyapi-x1（126） | `gpt-6-sol` | 0.4 / 2 | 上下文 ≤ 271999 |
| POJIAAI-DeepSeek（134） | `deepseek-v4-flash` | 0.08 / 0.32 | standard；已乘一次 0.08；空闲基准 |
| DeepSeek（133） | `deepseek-v4-pro` | 4.5 / 13.5 | off_peak |
| POJIAAI-DeepSeek（134） | `deepseek-v4-pro` | 0.36 / 1.08 | standard；已乘一次 0.08；空闲基准 |
| happyapi-x1（126） | `gpt-5.6-terra` | 0.8 / 3.6 | 超出短上下文阈值，Long context |
| happyapi-x1（126） | `gpt-6-sol` | 0.8 / 3 | 超出短上下文阈值，longcontxt |

## 春水Live

| 外站线路 | 准确模型 ID | 我方渠道 | 外站输入 / 输出 | 我方输入 / 输出 | 差额输入 / 输出 | 外站／我方倍数（输入 / 输出） |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| jiaoyanjuan | `gemini-2.5-flash` | happyapi-x1-genmini（129） | 1.0215 / 1.0215 | 0.08 / 0.32 | +0.9415 / +0.7015 | 12.76875 / 3.192187 |
| tangyuan | `claude-opus-4-5-20251101` | happyapi-x1-claude（128） | 1.46 / 1.46 | 1 / 5 | +0.46 / -3.54 | 1.46 / 0.292 |
| tangyuan | `claude-opus-4-6` | happyapi-x1-claude（128） | 1.461 / 1.461 | 1 / 5 | +0.461 / -3.539 | 1.461 / 0.2922 |
| tangyuan | `claude-opus-4-7` | happyapi-x1-claude（128） | 1.3867 / 1.3867 | 1 / 5 | +0.3867 / -3.6133 | 1.3867 / 0.27734 |

## Rilichat

| 外站线路 | 准确模型 ID | 我方渠道 | 外站输入 / 输出 | 我方输入 / 输出 | 差额输入 / 输出 | 外站／我方倍数（输入 / 输出） |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| pear | `gemini-2.5-flash` | happyapi-x1-genmini（129） | 0.8 / 6.6 | 0.08 / 0.32 | +0.72 / +6.28 | 10 / 20.625 |
| tomato_gemini_anti | `gemini-3-flash-preview` | happyapi-x1-genmini（129） | 0.4 / 2.6 | 0.1 / 0.6 | +0.3 / +2 | 4 / 4.333333 |
| pear_claude | `claude-opus-4-6` | happyapi-x1-claude（128） | 9.6 / 48 | 1 / 5 | +8.6 / +43 | 9.6 / 9.6 |
| cherry_china | `deepseek-v4.1-flash` | POJIAAI-DeepSeek（134） | 2.1 / 8.4 | 0.08 / 0.32 | +2.02 / +8.08 | 26.25 / 26.25 |
| tomato_deepseek | `deepseek-v4.1-flash` | POJIAAI-DeepSeek（134） | 1.5 / 6 | 0.08 / 0.32 | +1.42 / +5.68 | 18.75 / 18.75 |
| cherry_gpt | `gpt-5.6-terra` | happyapi-x1（126） | 5.2 / 31.2 | 0.4 / 2.4 | +4.8 / +28.8 | 13 / 13 |
| tomato_gpt_s | `gpt-5.6-terra` | happyapi-x1（126） | 3.7 / 22.5 | 0.4 / 2.4 | +3.3 / +20.1 | 9.25 / 9.375 |
| pear | `gpt-6-sol` | happyapi-x1（126） | 5.2 / 26 | 0.4 / 2 | +4.8 / +24 | 13 / 13 |

## AI风月

| 外站线路 | 准确模型 ID | 我方渠道 | 外站输入 / 输出 | 我方输入 / 输出 | 差额输入 / 输出 | 外站／我方倍数（输入 / 输出） |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| hiagi_google_anti | `gemini-2.5-flash` | happyapi-x1-genmini（129） | 7.199994 / 7.199994 | 0.08 / 0.32 | +7.119994 / +6.879994 | 89.999928 / 22.499982 |
| dian_google_cli | `gemini-3-flash-preview` | happyapi-x1-genmini（129） | 8.639993 / 8.639993 | 0.1 / 0.6 | +8.539993 / +8.039993 | 86.399931 / 14.399988 |
| manei_deepseek | `deepseek-v4-flash` | POJIAAI-DeepSeek（134） | 6.479995 / 6.479995 | 0.08 / 0.32 | +6.399995 / +6.159995 | 80.999935 / 20.249984 |
| portal_deepseek | `deepseek-v4-pro` | DeepSeek（133） | 11.759991 / 11.759991 | 4.5 / 13.5 | +7.259991 / -1.740009 | 2.613331 / 0.87111 |
| portal_deepseek | `deepseek-v4-pro` | POJIAAI-DeepSeek（134） | 11.759991 / 11.759991 | 0.36 / 1.08 | +11.399991 / +10.679991 | 32.666641 / 10.88888 |
| dian_deepseek_private | `deepseek-v4.1-flash` | POJIAAI-DeepSeek（134） | 8.159993 / 8.159993 | 0.08 / 0.32 | +8.079993 / +7.839993 | 101.999918 / 25.49998 |

## 桃花岛（旧配置历史参考）

| 外站线路 | 准确模型 ID | 我方渠道 | 外站输入 / 输出 | 我方输入 / 输出 | 差额输入 / 输出 | 外站／我方倍数（输入 / 输出） |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| teamorouter_deepseek | `deepseek-v4-flash` | POJIAAI-DeepSeek（134） | 8.4525 / 25.3575 | 0.08 / 0.32 | +8.3725 / +25.0375 | 105.65625 / 79.242188 |
| teamorouter | `gpt-5.6-terra` | happyapi-x1（126） | 8.7208 / 52.3248 | 0.4 / 2.4 | +8.3208 / +49.9248 | 21.802 / 21.802 |

## GPT 长上下文档补充

这里仍是相同模型的精确匹配，只列另一计费档；不把它冒充原文测试的实际适用档。GPT-5.6-terra 的短档为 ≤272000 token，GPT-6-sol 的短档为 ≤271999 token。

| 外站 / 线路 | 准确模型 ID | 我方输入 / 输出 | 差额输入 / 输出 | 外站／我方倍数（输入 / 输出） |
| --- | --- | ---: | ---: | ---: |
| Rilichat / cherry_gpt | `gpt-5.6-terra` | 0.8 / 3.6 | +4.4 / +27.6 | 6.5 / 8.666667 |
| Rilichat / tomato_gpt_s | `gpt-5.6-terra` | 0.8 / 3.6 | +2.9 / +18.9 | 4.625 / 6.25 |
| Rilichat / pear | `gpt-6-sol` | 0.8 / 3 | +4.4 / +23 | 6.5 / 8.666667 |
| 桃花岛 / teamorouter | `gpt-5.6-terra` | 0.8 / 3.6 | +7.9208 / +48.7248 | 10.901 / 14.534667 |

## 未匹配记录（24 条，不参与价格比较）

| 网站 | 原始模型与线路 | 原因 |
| --- | --- | --- |
| 春水Live | gemini-3.1-pro-preview@jiaoyanjuan | 开发环境没有准确同名模型 |
| 春水Live | gemini-3.6-flash@jiaoyanjuan | 开发环境没有准确同名模型 |
| 春水Live | gemini-3.7-flash@jiaoyanjuan | 开发环境没有准确同名模型 |
| 春水Live | gemini-3.8-flash@jiaoyanjuan | 开发环境没有准确同名模型 |
| 春水Live | claude-opus-4-8@tangyuan | 开发环境没有准确同名模型 |
| 春水Live | claude-opus-5@tangyuan | 开发环境没有准确同名模型 |
| 春水Live | deepseek-v4-flash-0731@kaohongshu | 开发环境没有准确同名模型 |
| Rilichat | tomato_gemini/gemini-2.5-pro | 开发环境没有准确同名模型 |
| Rilichat | kiwi_gemini_cli_2 / gemini-3.1-pro-preview（正常系数组） | 开发环境没有准确同名模型 |
| Rilichat | tomato_gemini_anti / gemini-3.1-pro-preview（最低系数组） | 开发环境没有准确同名模型 |
| Rilichat | tomato_gemini_anti/gemini-3.6-flash | 开发环境没有准确同名模型 |
| Rilichat | dragonfruit_gemini_anti/gemini-3.7-flash | 开发环境没有准确同名模型 |
| Rilichat | pear/gemini-3.8-flash | 开发环境没有准确同名模型 |
| Rilichat | blueberry_claude_mix/claude-opus-4-8 | 开发环境没有准确同名模型 |
| Rilichat | blueberry_claude_mix/claude-opus-5 | 开发环境没有准确同名模型 |
| Rilichat | blueberry_claude_mix/claude-opus-5-5 | 开发环境没有准确同名模型 |
| Rilichat | dragonfruit_glm / glm-5.3-flash（最低系数组） | 开发环境没有准确同名模型 |
| AI风月 | dian_google_cli / gemini-2.5-pro | 开发环境没有准确同名模型 |
| AI风月 | dian_google_cli / gemini-3.1-pro-preview | 开发环境没有准确同名模型 |
| AI风月 | dian_google_anti / gemini-3.7-flash | 开发环境没有准确同名模型 |
| AI风月 | dian_google_anti / gemini-3.8-flash | 开发环境没有准确同名模型 |
| AI风月 | hiagi_deepseek_bd / deepseek-v3.2 | 开发环境没有准确同名模型 |
| 桃花岛 | mnapi_gemini / gemini-3.1-pro-preview（正常；旧配置实测） | 开发环境没有准确同名模型 |
| 桃花岛 | soul_gemini / gemini-3.1-pro-preview（最低；旧配置实测） | 开发环境没有准确同名模型 |

未匹配的模型 ID：`claude-opus-4-8`、`claude-opus-5`、`claude-opus-5-5`、`deepseek-v3.2`、`deepseek-v4-flash-0731`、`gemini-2.5-pro`、`gemini-3.1-pro-preview`、`gemini-3.6-flash`、`gemini-3.7-flash`、`gemini-3.8-flash`、`glm-5.3-flash`。

其中 `gemini-3.1-pro-preview` 不匹配本地 `gemini-3.1-pro`，`deepseek-v3.2` 不匹配 `deepseek-v3.2-think`，`deepseek-v4-flash-0731` 不匹配 `deepseek-v4-flash`。

## 可复核性与限制

- 原始附件 SHA-256：`f3b0746fd73fe9e051e68284d0cca5d634764b4a879d083fed3beda4659d85de`。
- 本地价格快照 SHA-256：`e841e84055760d59908dd413049106f7c0c660488ae504950da265259b929dbb`。
- 本次比较使用 2026-10-16 的开发环境成本快照，对照原文 2026-10-08 及历史旧配置；不同日期的价格不能视为同时有效。
- POJIA 之前观测到部分流式账单只扣缓存费用。这是具体回包和账单的事实，不代表所有后续请求都只扣缓存。本报告仍比较完整的输入、输出正常单价，不把某次异常低扣费当成永久单价。
- 报告是只读分析。没有据此修改采购配置、生成新 key、增加分组，或把外站页面价格写回我方渠道。

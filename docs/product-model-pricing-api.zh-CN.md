# 产品组接入：分组、模型与人民币价格

适用：开发环境，2026-10-17。尚未上线，不能把以下路径视为生产已发布接口。

本地入口 `http://localhost:5173`，后端直连 `http://localhost:3000`。以下是公开只读接口，不需要调用 Key 或后台登录；真实生成请求仍须使用对应分组的 Key。公开目录不提供 Key，也不授予模型调用权限。

| 请求 | 用途 |
| --- | --- |
| `GET /v1/model-groups` | 拉取有启用模型的公开分组列表 |
| `GET /v1/model-catalog?group_id={固定分组ID}` | 精确拉取该分组的模型、渠道和查价 ID |
| `GET /v1/model-catalog` | 兼容原调用方式，拉取完整公开模型目录 |
| `GET /v1/model-prices/{pricing_id}` | 按一条具体线路查询当前人民币报价 |

产品组直接依次请求“分组 → 该分组的模型 → 选中模型的价格”，无需自行去重或筛选分组。不要把管理后台的 `/api/group/managed` 当公开接口。没有启用模型的空分组、停用渠道以及未注册分组不会出现在公开列表。

## 1. 拉分组

```bash
BASE='http://localhost:5173'
curl --fail --silent --show-error "$BASE/v1/model-groups"
```

真实 HTTP 返回节选：

```json
{
  "object": "list",
  "data": [{
    "group_id": "g2",
    "group_name": "POJIA · DeepSeek",
    "group_multiplier": 0.08,
    "model_count": 3
  }]
}
```

每个固定 `group_id` 返回一次；即使两个分组同名、同倍率，也分别返回。按固定 ID 排序。`model_count` 是该分组公开模型目录的记录数，同型号存在不同渠道时算不同线路记录，不代表跨渠道去重后的型号数量。

产品的选项值保存 `group_id`，显示 `group_name`，倍率用 `group_multiplier` 单独显示。不能用显示名或倍率当唯一键。

## 2. 拉这个分组下面的模型

通过接口按固定 ID 精确拉取，不能拿显示名代替。当前 POJIA 分组固定 ID 为 `g2`；保留 `--data-urlencode` 以兼容各环境：

```bash
GROUP_ID='g2'
curl --fail --silent --show-error --get "$BASE/v1/model-catalog" \
  --data-urlencode "group_id=$GROUP_ID" > models.json
cat models.json
```

等价请求：`GET /v1/model-catalog?group_id=g2`。

返回节选（仅包含该组模型，后续使用每条记录自带的 `pricing_url`）：

```json
{
  "object": "list",
  "data": [
    {
      "id": "deepseek-v4-pro",
      "model_uid": "mdl_052e4fc0bc88e2a5376eb7461a1ec3686311e42a1f2219ebf9beeac1414e252e",
      "group_id": "g2",
      "group_name": "POJIA · DeepSeek",
      "group_multiplier": 0.08,
      "channel_id": 134,
      "channel_name": "POJIA · DeepSeek",
      "model_family": "DeepSeek",
      "pricing_id": "prc_134_1fabf5d02f004860094a4431be7071d9488be024aacdf907a402d108aa662ce7",
      "pricing_url": "/v1/model-prices/prc_134_1fabf5d02f004860094a4431be7071d9488be024aacdf907a402d108aa662ce7"
    }
  ]
}
```

请求不带 `group_id` 或该值为空时，兼容返回完整目录；非空值按固定 ID 精确匹配，不做去空格、改大小写或显示名匹配。未知 ID、空分组或仅有停用线路的分组返回 HTTP 200 和 `{"object":"list","data":[]}`，不会回退到全部模型。

真实目录还包含 `object`、`supported_endpoint_types`。后者表示线路声明支持的协议，不代表每种模型、每种协议都已实测。渠道名称和分组显示名可以改，产品关联始终保存固定 ID。

## 3. 查询这个模型的人民币价格

拿选中记录的 `pricing_url` 直接请求，不拼模型名，也不自行计算价格 ID：

```bash
MODEL_ID='deepseek-v4-pro'
CHANNEL_ID=134
PRICE_PATH=$(jq -er --arg gid "$GROUP_ID" --arg model "$MODEL_ID" --argjson channel "$CHANNEL_ID" \
  '[.data[] | select(.group_id == $gid and .id == $model and .channel_id == $channel)]
   | if length == 1 then .[0].pricing_url else error("需要选中唯一一条目录记录") end' models.json)
test -n "$PRICE_PATH" && curl --fail --silent --show-error "$BASE$PRICE_PATH"
```

一个分组若出现多条渠道记录，应让用户选定目录记录，再使用该记录的 `pricing_url`；不能任意取第一条。本例的 POJIA 分组只绑定一个渠道。

等价的完整请求：

```http
GET /v1/model-prices/prc_134_1fabf5d02f004860094a4431be7071d9488be024aacdf907a402d108aa662ce7
Host: localhost:5173
```

实际开发接口返回节选（金额和时间只代表本次取样，不是永久固定价）：

```json
{
  "object": "model_price",
  "data": {
    "group_id": "g2",
    "group_name": "POJIA · DeepSeek",
    "id": "deepseek-v4-pro",
    "pricing_id": "prc_134_1fabf5d02f004860094a4431be7071d9488be024aacdf907a402d108aa662ce7",
    "currency": "CNY",
    "available": true,
    "price_source": "reference",
    "billing_type": "tokens",
    "unit_tokens": 1000000,
    "rates": {
      "input": 0.36,
      "output": 1.08,
      "cache_read": 0.012,
      "cache_write": 0.36,
      "cache_write_1h": 0.36
    },
    "cost_rates": {
      "input": 0.36,
      "output": 1.08,
      "cache_read": 0.012,
      "cache_write": 0.36,
      "cache_write_1h": 0.36
    },
    "applied_group_multiplier": 0.08,
    "current_tier": "standard",
    "quoted_at": 1792241136,
    "synced_at": 1792241123,
    "valid_until": 1792327523,
    "price_version": "a60d4a25043b7d5c199d6a17e5c5d81cdc4aec0f33a76417328295dc2b0e2df9"
  }
}
```

这里 `rates` 是平台当前按该分组收取的人民币单价，`cost_rates` 是人民币采购成本单价。正常无补贴配置下二者相同；若管理员设置补贴，必须分别展示，不能混称实际进货价。POJIA 本例 `price_source=reference`，单价来自已配置的参考价格与倍率；真实请求的上游账单另按第 6 节核对。`configured` 表示本地手动配置，也不能冒充上游核验成本。

## 4. 固定 ID 的用途

| 字段 | 是否固定 | 产品组如何使用 |
| --- | --- | --- |
| `group_id` | 固定；系统自动生成短 ID `g1`、`g2`…，系统默认组为 `default` | 分组选择、关联配置；改名后不换 ID |
| `group_name` | 可修改 | 只用于展示 |
| `channel_id` | 当前环境内固定的渠道编号 | 区分真实线路；不要拿名称作主键 |
| `id` | 精确上游模型名 | 真实调用请求里的 `model` 值；保留大小写和版本 |
| `model_uid` | 同一精确模型名稳定 | 跨渠道归集同型号，不能单独用来查价 |
| `pricing_id` | 固定分组＋渠道＋精确模型名的稳定组合标识 | 产品模型选项的唯一键与查价 ID；同名模型走不同渠道不覆盖 |
| `pricing_url` | 当前查价路径 | 原样拼接本地入口发请求 |
| `price_version` | 会随价格响应变化 | 识别报价更新，不是固定 ID |

开发环境已按管理员要求，将旧的名称型 ID 一次性迁移为短 ID；POJIA 为 `g2`，DeepSeek 官方为 `g3`。渠道、Key、自动路由及相关配置的绑定同步迁移，Key 本身不变。以后自动生成的 ID 不可编辑，显示名称可以修改，删除的序号不再复用。系统默认组保留 `default`。

这次明确更换了固定分组 ID，因此该组的 `pricing_id` 也已更新；产品组需要重新拉取目录，清理旧的分组和报价 ID 缓存。后续修改倍率或显示名称不会重建 `pricing_id`。不同环境应分别拉目录，不能硬编码开发环境 ID。未登记的废弃引用已清理；仍被渠道、用户、Key 或自动路由使用的分组不能直接删除。

## 5. 价格展示与费用计算

- `currency=CNY`，`unit_tokens=1000000`。本例普通输入为 ¥0.36 / 100万 token，输出 ¥1.08 / 100万 token，缓存读取 ¥0.012 / 100万 token。
- 返回价已经计入适用倍率和人民币换算。**产品端不要再乘 0.08，也不要再乘美元汇率。**
- 普通输入、缓存读取、适用的缓存写入和输出分别计算。若 usage 的输入总数已包含缓存 token，先按实际协议拆分，不能把缓存再按普通输入重复收费。
- 未命中缓存的 1000 输入 token＋500 输出 token，本例费用为 `1000 / 1000000 × 0.36 + 500 / 1000000 × 1.08 = ¥0.0009`。这是给定 usage 下的金额示例；最终账单以实际用量及计费条件为准。
- 按次模型读取 `request_price`（元/请求）与人民币 `expression`，不要套 token 单价公式。上下文阶梯看 `tiers` 和表达式，不能拿一个基础单价替代所有请求条件。
- 峰谷模型以接口本次返回的 `rates`、`current_tier` 为当前价；有 `peak_rates`、`off_peak_rates`、`schedule`、`timezone` 时保留这些条件。本次 POJIA 样例仅返回 `current_tier=standard`，没有峰谷字段，产品不得自行补出空闲价格。
- 时间戳均为 Unix 秒。分组列表与模型目录允许缓存 15 秒，产品缓存模型目录时必须把完整 `group_id` 纳入缓存键，不能把不同分组混用；查价 HTTP 响应为 `no-store`。`valid_until` 是报价有效性上限，调用前应刷新；过期后不能继续当有效报价使用，也不保证该时刻之前价格绝不更新。
- `available=false` 时展示“价格暂不可用”，保留 `unavailable_reason` 供排查，不展示成免费。`cost_rates` 不存在时不臆造采购成本。
- 未知、移除或停用的报价 ID 返回 HTTP 404，错误码 `pricing_id_not_found`；目录或数据库暂不可用返回 HTTP 503，错误码 `catalog_unavailable`。失败应保留原选择并允许刷新，不能换用别的渠道价格。

真实模型调用仍向对应协议端点提交 `model: "deepseek-v4-pro"`，并使用已绑定该固定分组的 Key；`pricing_id` 是查价标识，不是生成接口的模型名。公开目录是分组默认规则，账户特殊倍率可能影响实际结算。

开发验证（2026-10-17）：SQLite、MySQL、PostgreSQL 测试通过，覆盖匿名读取、同名同倍率分组不合并、固定 ID 精确筛选、未知 ID 空列表、改名保留 ID 及停用线路隐藏。本地 3000 / 5173 接口检查通过：9 个公开分组、144 条模型记录，各组 `model_count` 与筛选结果一致，模型名、价格及倍率保持不变；固定分组与报价 ID 按本次短 ID 迁移更新。POJIA 返回 3 条模型记录，Pro 报价保持输入 ¥0.36、输出 ¥1.08 / 百万 token，0.08 倍率只计入一次。未上线。


## 6. POJIA 单次实际扣费验证

开发环境实测（2026-10-17）：先匿名拉分组，再按 `group_id=g2` 拉出 3 个模型，从 `deepseek-v4-pro` 目录项的 `pricing_url` 读取人民币报价；使用原有绑定该组的 Key，发起一次 `/v1/chat/completions` 流式请求。只调用渠道 134 一次，没有生成重试，没有请求其他渠道。

- 客户端收到内容，流正常结束；上游实际报告输入 1697 token，全部缓存命中，输出 0 token。
- 当前缓存价格为 ¥0.012 / 百万 token，报价计算为 `1697 / 1000000 × 0.012 = ¥0.000020364`。
- POJIA 消费记录的实际费用增加 ¥0.000020364；本地钱包、Key 及独立开发日志库的消费记录均扣 10 配额点，即 ¥0.000020。日志编号 25336，分组 `g2`。
- 差额 ¥0.000000364 是小于一个配额点的向下取整差额，由平台承担；分组 0.08 只计算一次。
- 其他渠道及所有定价配置在本次请求前后一致。测试只验证本次上游实际报告的缓存费用，不把输出 0 的这次请求当成普通输入、输出费用和峰谷各档都已实账验证。

测试初次查询误用了主库中的旧日志表，随后只读核对正确的独立开发日志库 `token52_dev_log`，确认实际消费记录。未重新生成、未补扣、未调整价格，未上线。


分组管理验证：`go test ./controller -run TestManagedGroupsAndKeyProjectsMatrix -count=1 -v` 在真实 SQLite 3.50.4、MySQL 8.4.11、PostgreSQL 18.6 通过，覆盖递增短 ID、删除后不复用、旧编辑版本冲突、使用中分组拒删和已删除 Key 的旧引用清理。`go test ./model ./controller ./router -run 'TestPublicCatalog|TestManagedGroupsAndKeyProjectsMatrix|TestAccessTokenRouteRulesCoverEveryDashboardRoute' -count=1` 通过。前端 `bun run test src/features/system-settings/models/__tests__/group-binding-refresh.test.tsx src/features/system-settings/models/__tests__/group-settings.test.tsx` 共 15 项通过，类型检查、改动文件 lint、前后端构建通过。按仓库限制未使用浏览器验收；本地开发服务保持运行，临时数据库角色和临时 Go 测试文件已清理。


### 官方美元账户的人民币报价

开发环境 DeepSeek 官方 `g3` 采用 `deepseek-usd` 来源：每充值 ¥1 到账 US$0.14705882352941177（US$1 = ¥6.8）。公开接口仍返回 `currency: "CNY"`。每百万 token 的空闲价：Flash 输入 ¥1.02、输出 ¥4.08、缓存读取 ¥0.0204；Pro 输入 ¥4.488、输出 ¥13.464、缓存读取 ¥0.1496。高峰价均为对应空闲价的两倍，时间条件按官方规则处理；客户端以查询时的 `current_tier` 与有效期为准。

POJIA `g2` 继续使用其独立的人民币参考价与 0.08 倍率，不能拿官方美元来源的报价替换。中转站的按秒、特殊份数和条件表达式现在按下文返回完整的人民币计费合同，不混成普通每请求固定价；未通过验证的规则仍返回不可用及原因。


### 条件公式、按秒与计价份数

启用来源的 `advanced_pricing` 后，`billing_type` 可能为 `task_usage` 或 `expression`。这两类返回完整的人民币 `expression` 及采购成本 `cost_expression`，不编造 `rates` 或 `request_price`。任务公式同时返回 `usage_schema`，条件字段、物理单位和枚举都保持上游含义；`valid_until` 最迟到下一分钟，客户端应按时刷新。它是价格合同，实际金额要带入本次用量及参数，不能把条件公式简化成某个默认分支。

按秒示例（充值 1:1、上游 0.5 倍，示例字段省略普通标识）：

```json
{
  "currency": "CNY",
  "billing_type": "task_usage",
  "expression": "tier(\"second\", u(\"seconds\") * 0.5)",
  "cost_expression": "tier(\"second\", u(\"seconds\") * 0.5)",
  "usage_schema": {"seconds": {"type": "number", "unit": "second"}},
  "charge_unit": {"unit": "second", "units": 1, "price": 0.5}
}
```

代入 `seconds=5`，金额为 ¥2.50。任务表达式的结果已经是人民币金额，不再除以百万；token 表达式及 `fixed()` 的宿主约定保持不变。

NewToken 标价 0.025、计价份数 4 的请求模型仍返回 `billing_type: "request"`，`request_price` 和 `cost_request_price` 均为 `0.10`，表达式为 `tier("request", fixed(0.1))`。额外的 `charge_unit` 为 `{"unit":"request","units":4,"price":0.025}`，用于解释采购单价与份数；产品只按总请求价格收费一次。

执行网关会检查任务插件的用量字段、单位与枚举，保存冻结的采购公式，再按实际完成用量补退。模型调用失败、用量不兼容、接口尚未接通等情况不能据此假定为正常完成；本轮已验证的是价格读取及隔离环境的钱包/Key 账本流程，没有新的上游付费调用。

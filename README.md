# 关于

本项目是 [art-design-pro-edge](https://github.com/ChnMig/art-design-pro-edge) 的后端服务。
配合前端可以做到开箱即用, 但是具体的业务功能需要自己开发.

## 项目特点

- 项目的95%代码由 `github copilot` 辅助编写

### 人民币与港币资金兑换

- 数据库迁移新增客户 `hkd_balance`（初始为零）与 `currency_exchanges` 成交记录表；原 `balance` 始终是人民币，`strategy_balance` 不作港币账户使用。部署新接口前先执行现有迁移命令 `go run . --migrate`。
- 客户认证接口 `GET /api/v1/open/mobile/finance/exchange` 返回独立 CNY/HKD 余额、汇率及配置更新时间；`POST` 同路径执行兑换，参数为 `request_id`（16–64 位字母/数字/连字符）、`from_currency`（CNY/HKD）、`amount`、`rate`、`trade_pin`。
- 汇率取后台系统设置 `links.hkdRate`，含义为 **1 HKD = X CNY**，最多八位小数，合法范围 0.00000001–100。缺失或无效时拒绝兑换，不采用默认价格；提交汇率不同于当前配置时必须刷新后再次确认。时间是后台配置更新时间，不是实时外汇报价时间。
- 已实名认证、账户及资金状态正常且交易密码正确才能兑换；确认后即时到账，不额外收兑换费。兑出金额 0.01–5,000,000，最多两位小数；兑入按精确十进制计算并向下保留到分，过小及超过账户限制的兑入金额会拒绝。
- 事务内锁客户行，原子更新两个余额、成交汇率快照与双币种流水。相同客户同一请求编号重复提交返回原结果；同编号不同参数拒绝。收到网络错误必须用原编号重试，不要新建编号；客户端会在当前浏览器标签页保存待确认编号（不保存交易密码）。不同实例共享数据库约束与行锁。
- 后台客户列表/详情可查看港币余额，手动入账按 CNY/HKD 写入相应账户，资金明细保留两条币种流水、兑换编号及成交汇率。港币与人民币不直接相加，现有人民币资产列不包含港币。
- 资金兑换不代表所有港股都以港币计价。港股模拟交易的独立开关、支持币种与报价校验见下节。
- 定向验证：`go test ./api/app/v1/open/mobile -run "TestExchange|TestCurrencyExchangeIntegration" -count=1`。设置 `EXCHANGE_TEST_DSN` 可运行隔离数据库测试，使用随机独立模式（schema），结束删除该模式，不改业务客户。

### 独立港币模拟交易

- 先执行 `go run . --migrate`，新增 `hong_kong_trade_requests` 幂等回执表。系统设置 `hkTrade.enabled` 默认关闭；需后台开启且对应证券启用后才能交易。采集港股列表不会自动开放交易。
- 客户认证接口 `GET /api/v1/open/mobile/trade/hong-kong/quote/:code` 返回五位代码、HKD 报价、实际报价时间、数据来源、每手股数、费用参数、独立港币余额及可交易状态/原因。
- `POST /api/v1/open/mobile/trade/hong-kong/orders` 接收 `request_id`、`code`、`direction`（买入/卖出）、`quantity`，每次执行前重新查询腾讯报价。回执含 `currency`、`quote_at`、成交价格/数量、各项费用及 `balance_after`。这是公开延迟报价模拟，不是真实交易所撮合。
- 腾讯扩展报价当前使用字段 60（每手股数）、63（GP 普通股）、75（HKD 币种）；字段缺失、代码不一致、无效价格/整手、非港币柜台或非普通股均拒绝。报价最多允许延迟 30 分钟，未来时间最多容忍 60 秒；上游字段结构变化时应重新验证，不可猜测填值。
- 本机收市价测试为独立例外，默认关闭：仅当 `--dev` 开发模式、请求连接和客户端均为回环地址且 `hkTrade.localCloseTest=true` 时可能生效；生产模式及远程请求一律忽略该配置。工作日香港时间 16:10 后，只允许当日 16:00 起的收市阶段报价，跨日立即失效，上午/昨日/无效/未来报价不放行。不要求修改电脑时间，也不把上游报价时间改成当前时间；它不是官方收市价认证或历史盘口回放。
- 报价接口和成交回执返回 `local_close_test`，客户端显著显示“本机收市价模拟测试（非实时成交）”、真实报价时间；成交记录及资金流水保存测试标识与原始报价时间。此模式可跳过该收市时段及30分钟时效限制，但证券启停、币种/品种、整手、费用、余额、幂等等规则均不变。关闭配置后，新委托恢复正常校验，历史回执仍可按原编号重放。
- 买入按真实每手整数倍，使用港币全额支付且杠杆固定 1 倍。买入可当日卖出，卖出按整手或全部可卖数量。市价交易时段按香港工作日 09:30–12:00、13:00–16:00 校验，未实现完整交易所节假日日历；`trade.allDay` 测试开关仅对市价跳过时段，不跳过报价有效性检查。
- 费用使用后台港股佣金/最低佣金、买卖双向印花税与其余三项交易费，按项目规则分别舍入到分。这是模拟费用规则，不宣称覆盖交易所全部税费舍入、最低额或豁免规定。
- 事务锁定客户及相关配置/证券行，原子更新港币余额、港股持仓、成交、港币流水及回执，不使用人民币余额、人民币冻结资金或人民币累计盈亏。资金不足拒绝成交；A 股管理费、自动补仓与相应自动强平查询排除港股。
- 相同客户/请求编号重复提交返回原回执，同编号不同参数拒绝。已成功回执在数据源断线、交易关闭或时段结束后仍可重放；网络结果未知时必须重试原编号，不要新建编号。客户端在当前标签页保存待确认请求。
- `/trade/positions`、`/trade/records` 默认仅查询非港股，显式传 `market=HK` 查询港股；原 A 股下单接口不接受港股。账户汇总按 CNY/HKD 分离，不直接相加。
- 后台禁止直接新增、覆盖或删除港股持仓，须通过模拟买卖或后台强平结算。后台强平使用证券缓存价格，延续原强平不扣交易费用的规则，但港股只结算到 HKD 账户；操作前应确认缓存报价。后台持仓可查询腾讯公开延迟价格，不代表实时盘口。
- 港股持仓估值使用最近采集/成交更新的缓存价格；刷新持仓并不保证持续拉取所有持仓的最新市场报价。公开源约有 15 分钟延迟，商用及行情分发前须确认授权。
- 验证：`go test ./api/app/v1/open/mobile ./api/app/v1/private/admin/platform/trade -count=1`。设置 `HK_TRADE_TEST_DSN` 可启用随机隔离 schema 的数据库回归，覆盖幂等并发、回滚、费用/港币结算与 A 股风险任务隔离；结束删除隔离 schema，不改业务客户。连接账号须有创建/删除 schema 的权限。

#### 港股限价委托与严格盘口模拟撮合

- 上线前执行迁移，新增客户 `hkd_frozen_balance`、限价委托可空 `request_id` 与客户/编号唯一索引、成交盘口 `quote_at`；旧 A股委托编号为空，不改变现有委托。
- `POST /api/v1/open/mobile/trade/hong-kong/limit-orders` 接收 `request_id`、五位 `code`、`direction`、正整数 `quantity`、最多三位小数 `limit_price`。这是项目模拟精度，不代表港交所价位表。相同编号/参数返回当前原委托，参数冲突拒绝；重试不依赖新行情。不得用 A股下单接口提交港股。
- 提交时读取公开行情验证港币普通股和实际每手数量，允许收市挂单；买入冻结目标价本金及预估费用，仅影响 HKD 可用/冻结余额，卖出冻结可卖股数。非整手只允许一次委托全部可卖数量。冻结/解冻保存港币流水，冻结款不能兑换或重复使用。费用提高超出原冻结额时不动用其他可用资金，保留委托并报错，须撤单重下。
- `GET /trade/limit-orders` 默认只查 A股，`market=HK` 查港股，均分页；回执附币种、冻结金额、待成交说明及成交盘口时间。`DELETE /trade/limit-orders/:id` 验证所属客户；港股重复撤单不重复解冻，已成交不能撤。停用交易后仍可撤单。
- 延续每分钟撮合任务。港股只在香港工作日 09:30–12:00、13:00–16:00 检查有效买卖盘口，`trade.allDay` 和本机收市测试不绕过限价撮合规则。盘口使用腾讯扩展报价买卖档（9–28），只接受有限正价格及正整数挂量；无效档视为无可执行盘口，不拿最新价填充。超过30分钟、无盘口、零挂量、交叉盘口、目标价未满足均保持待成交。
- 买入取最低有效卖价且不高于目标价，卖出取最高有效买价且不低于目标价。延续 A股“有效档位触发整笔模拟成交”的简化模型，不模拟排队、部分成交或多客户流动性消耗；公开源常缺有效挂量，不能保证港股委托会成交，更不是实时交易所撮合。未实现完整休市日历。
- 客户行→委托行→持仓行锁定，成交、撤单竞争只允许一个结果；解冻、按盘口成交、费用、持仓、流水及成交状态在同一事务完成，失败全部回滚。成交复用独立港币结算及内部幂等回执，备注标注限价模拟和真实盘口时间。A股管理费/补仓撤单排除港股，后台强平会撤销对应卖出委托。

## TODO

- API层权限管制
- 接口文档
- 单元测试
- 持续的代码优化

## 部署配套服务

PostgreSQL 和 Redis 的 docker-compose 文件在 `docker` 目录下, 可以直接使用。

> 如果部署在云端, 务必修改有 TODO 标识的配置项, 防止密码泄露!!!

```bash
docker-compose -f docker/docker-compose.yml up -d
```

## 技术栈

`Golang` `Gin` `Gorm` `PostgreSQL` `Redis`

## build

`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o server`

## dev

`go run main.go --dev`

## 初次启动

### 修改配置文件

> 务必修改配置文件, 尤其是密码相关

复制配置示例并修改：

```bash
cp config.yaml.example config.yaml
```

然后修改 `./config.yaml` 中的配置（JWT/Redis/Postgres/Admin 等敏感项务必替换）。

## 执行数据库初始化

`go run main.go --migrate`

## start

`nohup ./server &`

## QA

TODO

## 财经新闻采集与管理

### 模块目录

- `domain/news`: 新闻采集适配器、标准化、去重与采集服务
- `api/app/v1/private/admin/platform/news`: 后台新闻管理接口
- `api/app/v1/open/mobile/news`: 公开新闻接口
- `cron/news_collect.go`: 定时采集任务
- `db/pgdb/system/news_model.go`: 新闻数据模型

### 已接入数据源

- 国务院政策发布公开数据: `https://www.gov.cn/pushinfo/v150203/pushinfo.json`

默认版权模式为 `metadata_only` 或 `excerpt`，保存标题、摘要和原文链接，不默认保存第三方完整正文。

### 配置项

在 `config.yaml` 新增 `news` 配置：

- `news.collection_enabled`: 是否启用定时采集
- `news.collection_cron`: 定时任务 cron（默认 `*/10 * * * *`）
- `news.request_timeout_ms`: 采集请求超时
- `news.max_retries`: 临时网络错误最大重试次数
- `news.default_language`: 默认语言

### 任务与手动采集

- 定时任务由 `cron/news_collect.go` 执行，按 `news.collection_cron` 触发
- 后台手动采集接口：`POST /api/v1/private/admin/platform/news/collect`

### 后台接口

- `GET /api/v1/private/admin/platform/news`
- `GET /api/v1/private/admin/platform/news/:id`
- `PUT /api/v1/private/admin/platform/news`
- `DELETE /api/v1/private/admin/platform/news`
- `POST /api/v1/private/admin/platform/news/batch-action`
- `GET /api/v1/private/admin/platform/news/sources`
- `PUT /api/v1/private/admin/platform/news/sources`
- `POST /api/v1/private/admin/platform/news/collect`
- `GET /api/v1/private/admin/platform/news/collect-logs`

### 公开接口

- `GET /api/v1/open/news`
- `GET /api/v1/open/news/:id`
- `GET /api/v1/open/news/categories`
- `GET /api/v1/open/securities/:code/news`

### 接口映射表

| 功能 | 接口 | 方法 | 权限 |
| --- | --- | --- | --- |
| 新闻列表 | `/api/v1/private/admin/platform/news` | GET | 管理员 |
| 新闻详情 | `/api/v1/private/admin/platform/news/:id` | GET | 管理员 |
| 新闻管理 | `/api/v1/private/admin/platform/news` | PUT | 管理员 |
| 手动采集 | `/api/v1/private/admin/platform/news/collect` | POST | 管理员 |
| 采集日志 | `/api/v1/private/admin/platform/news/collect-logs` | GET | 管理员 |
| 证券相关新闻 | `/api/v1/open/securities/:code/news` | GET | 公开 |

## 港股后台采集

后台“证券列表 → 证券信息”提供“同步港股（新浪＋腾讯）”按钮。采集默认停用新证券，不自动开启交易，也不加入 A 股定时同步任务；独立模拟交易需另开配置并启用证券。

- 新浪列表接口：`https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHKStockData`。
  使用 `page=1&num=60&sort=symbol&asc=1&node=qbgg_hk&_s_r_a=init`，逐页读取直到返回空数组。不得依赖总数接口提前停止；空响应、重复代码、乱序、上游错误或超过 100 页都会终止采集。
- 腾讯报价接口：`https://qt.gtimg.cn/?q=hk00700,hk09988`，每批最多 100 只，按 GBK 解码。
- 保留五位证券代码及前导零，完整代码为 `00700.HK`，市场为“港股”、交易所为“港交所”。来源列表未明确提供板块分类，采集不推断板块，也不覆盖人工分类。
- `quote_at` 保存上游报价时间的 Unix 秒数；解析时区为香港时间。`updated_at` 仅表示数据库更新时间，两者不能混用。现有 A 股数据未补历史报价时间，显示为 `--`。
- `source=sina/tencent` 表示采用腾讯报价；`sina` 表示采用新浪报价（腾讯未覆盖、无有效报价或时间更早）；`sina/unavailable` 表示没有有效报价。无报价证券的 `quote_at=0`，不会使用昨收价冒充现价。任务完成后返回各来源数量。
- 同时存在有效报价时采用时间较新的记录；一次网络或格式错误会终止本轮采集，不写入部分结果。全部数据获取成功后通过数据库事务批量保存，任一批失败时整体回滚。
- 新增港股默认为停用；重复同步不修改人工启停、软删除状态或板块。采集不会删除本次列表中缺失的历史证券，不会覆盖 A 股六位证券代码的数据。
- 客户端列表通过 `market=港股` 显式读取采集来源记录（含不可交易的停用记录）；默认列表仍仅返回已启用的非港股证券。港股换手率排行明确返回未接入提示，客户端禁用该排行。报价详情仅返回缓存报价、来源和报价时间，不触发 A 股盘口或图表接口；客户端显示三位小数及成交股数单位。分时、K 线与盘口尚未开放，模拟交易走独立接口。
- 公开港股报价可能延迟约 15 分钟，不能用于实时盘口撮合；商用及行情分发前须另行确认授权。该列表不等同于已经与港交所官方全部证券品种完成对账，且暂未提供换手率。

接口均沿用现有平台管理员权限：

| 功能 | 接口 | 方法 |
| --- | --- | --- |
| 启动后台任务 | `/api/v1/private/admin/platform/stock/securities/sync/hong-kong` | POST |
| 查询任务进度及结果 | `/api/v1/private/admin/platform/stock/securities/sync/hong-kong` | GET |

任务异步执行以避免页面请求超时，同一服务进程不允许重复启动。页面每两秒查询一次状态，刷新页面可继续查看运行进度。任务上限为 10 分钟；进度和本进程最后一次结果保存在内存中，服务重启后重置为 `idle`，已经提交的证券数据仍保留在数据库。多实例部署前需增加跨实例任务锁。

升级时执行现有数据库迁移 `go run . --migrate`，为证券表增加 `quote_at` 字段。数据库集成测试使用 `HK_SYNC_TEST_DSN` 指向本地测试库，测试数据在独立事务与临时命名的 schema（数据库模式）内创建，结束后整体回滚。

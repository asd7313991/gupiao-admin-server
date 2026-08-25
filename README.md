# 关于

本项目是 [art-design-pro-edge](https://github.com/ChnMig/art-design-pro-edge) 的后端服务。
配合前端可以做到开箱即用, 但是具体的业务功能需要自己开发.

## 项目特点

- 项目的95%代码由 `github copilot` 辅助编写

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

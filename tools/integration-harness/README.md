# 本地最小联调环境（仅虚构 fixture）

此环境验证 M1 的 MySQL 读取链路，R/I 由纯 Go 测试验证。不启动 HTTP API、
Fleet、模型、真实租户连接，不启用任何业务 feature flag。MySQL schema 只覆盖
盘点 SELECT 所需四张表，不代替 V1 完整 migration 验收。

```bash
# 在已合入 M1/R/I 的集成分支执行；Go 1.25、Python 3、Docker Compose required。
export GO_BIN=/home/mlamp/go-sdk/go/bin/go
./tools/integration-harness/run.sh up
./tools/integration-harness/run.sh health
./tools/integration-harness/run.sh smoke
$GO_BIN test ./internal/inventory/... ./internal/referencecontract/... ./internal/service/plugin/...
$GO_BIN build ./...
$GO_BIN test ./...
```

- `up` 生成忽略提交的随机本地密码，启动独立 Compose project，自动导入固定数据。
- MySQL 仅绑定 `127.0.0.1:33392`；盘点用户只有 SELECT 权限。
- `smoke` 读取自己的配置，覆盖环境中任何已有 DSN；验证写入被拒、盘点结果及数据库前后相同。
- 输出 `tools/integration-harness/report.json`：四个虚构资源、一个同名技能组、一个缺少 current snapshot 的技能。
- 停止 `./tools/integration-harness/run.sh down`，仅删除本任务容器；tmpfs 样例数据随容器删除。下次 up 重建 fixture。

健康检查是 MySQL fixture 可查询性，不代表完整 Marketplace HTTP/Fleet 安装流程通过。
本轮没有真实环境盘点或对象存储校验。数据库参数不接受用户提供的真实 DSN。

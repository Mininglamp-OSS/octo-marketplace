# 本地最小联调环境（仅虚构 fixture）

此环境验证 M1 的 MySQL 读取链路。不启动 HTTP API、
Fleet、模型、真实租户连接，不启用任何业务 feature flag。MySQL schema 只覆盖
盘点 SELECT 所需四张表，不代替 V1 完整 migration 验收。

```bash
# 在当前 M1 交付分支执行；需要 Go 1.25、Python 3、Docker Compose。
export GO_BIN=/home/mlamp/go-sdk/go/bin/go
./tools/integration-harness/run.sh up
./tools/integration-harness/run.sh health
./tools/integration-harness/run.sh smoke
$GO_BIN test ./internal/inventory/... ./cmd/marketplace-inventory/...
GIT_DIR="$(git rev-parse --absolute-git-dir)" GIT_WORK_TREE="$(git rev-parse --show-toplevel)" $GO_BIN build ./...
$GO_BIN test ./...
```

- `up` 生成忽略提交的随机本地密码，启动独立 Compose project，自动导入固定数据。
- MySQL 仅绑定 `127.0.0.1:33394`；盘点用户只有 SELECT 权限。
- `smoke` 读取自己的配置，覆盖环境中任何已有 DSN；验证写入被拒、盘点结果及数据库前后相同。
- 输出 `tools/integration-harness/report.json`：八个虚构资源、一个同名技能组、一个缺少 current snapshot 的技能。
- 停止 `./tools/integration-harness/run.sh down`，仅删除本任务容器；tmpfs 样例数据随容器删除。下次 up 重建 fixture。

健康检查是 MySQL fixture 可查询性，不代表完整 Marketplace HTTP/Fleet 安装流程通过。
本轮没有真实环境盘点或对象存储校验。数据库参数不接受用户提供的真实 DSN。

fixture 包含有意的负例：5 处附件元数据缺失、1 处附件 sidecar 使用了错误的数组格式
（fixture-skill-b）、1 个缺少 current snapshot 的技能。smoke 精确断言这三类发现的数量，
这些告警是验收结果，并不表示样例全部满足迁移条件。

## 2026-10-24 本地验证

历史基于 V1 `31a2759` 的组合验证记录：`up`、`health`、
`smoke` 均通过。已核验 SELECT-only 用户写入失败，盘点前后完整 fixture dump
SHA256 一致。上述对应包测试、`go build ./...`、`go test ./...` 全部通过。
构建显式指定当前 worktree 的 Git 元数据，避免宿主机上层 Git 仓库干扰 VCS stamping。

## 当前交付分支补充

基于 PR1 `348b603`；本分支单独 project `marketplace-inventory-delivery`、端口
33394，避免影响先前 33392 联调环境。八个固定虚构资源额外覆盖 space、system、
旧 public 和未知可见性/状态。`smoke` 精确核对分布，且分布总数等于资源总数。
Go 单元测试另覆盖 private/space/system × draft/published/delisted ×
embedded/top-level × deleted/not-deleted 全部 36 个组合，以及空输入和乱序稳定性。
不依赖 R/I 包，不声称已验证安装联调。

当前交付分支已重新执行 `up`、`health`、`smoke`、对应包测试、`go build ./...` 和
`go test ./...`，全部通过。全量测试没有注入 `TEST_MYSQL_DSN`，因此其中通用数据库
集成用例按仓库约定跳过；M1 自身的 SQL 链路由上述独立 MySQL smoke 实际验证。

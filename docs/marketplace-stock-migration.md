# M3 存量资源迁移工具

此工具属于第三个交付 PR（Refs #123）。只处理已经存在的 Marketplace 数据，
不增加旧 ZIP 上传入口，不在服务启动时执行，也不启用任何业务开关。
当前证据来自隔离 MySQL 和虚构文件；没有访问真实租户或生产环境。

迁移版本号每段最多 9 位，高于当前与历史数值版本，并为后续发布保留递增空间。
无法排序的旧标签（如 `v1`）从合法版本开始；patch 达上限时向 minor/major 进位。
若旧数值标签超出正常写入范围或版本空间耗尽，报告 `unresolved_version_label`
并隔离该根资源，不写入无效标签。

审计 schema 不支持降级：SQL `Down` 会显式拒绝，保留审计数据和迁移记录。
恢复数据指针应使用本工具的批次回滚功能；参见
[审核修复与索引说明](pr126-review-followup.md)。

## 1. 先盘点和预览

先用 M1 核对可见性、状态、内嵌/独立分布。数据库已安装对应 schema 后，使用
单独的 SELECT 账号，通过环境变量设置 `MARKETPLACE_STOCK_MIGRATION_DSN`。
不要将密码写入参数、命令历史或提交文件。

```bash
go run ./cmd/marketplace-stock-migration --all-spaces \
  --artifact-dir /absolute/isolated-object-copy > /tmp/stock-preview.json
```

没有 `--artifact-dir` 时，仅可迁移原本就是完整内联文本的技能。
该参数只能指向已经存在、明确授权的本地对象副本目录。工具只读文件，不跟随
HTTP/file URL，不运行脚本；对象 key 仍按原 Space/可信旧目录校验。
独立 `--fixture evidence.json` 模式不连接数据库，可以组合本地文件副本预览。

报告只含输入行数、根资源 ID、映射 ID/新版本、依赖顺序、证据 digest 和固定异常码，
不输出技能正文、MCP 配置、对象 key、环境变量或密钥。输入行数可与同一快照的
M1 `resources` 总数核对；迁移后的新增独立行也会计入后续盘点。

## 2. 核对异常，分根资源应用

G4 异常处理规则必须先单独确认。当前唯一实现策略是 **任一未解决异常即停止**，
没有自动放宽权限、猜版本、补造旧发布或清空配置。显式确认该策略只授权所选批次，
不意味着可以使用真实数据；真实环境执行仍须另行授权。

```bash
# 仅在隔离副本演练，按预览 dependencies 先应用技能/专家，再应用引用它们的团。
go run ./cmd/marketplace-stock-migration --all-spaces --apply \
  --batch-id rehearsal-001 --root-id ROOT_ID --expected-digest PREVIEW_DIGEST \
  --g4-stop-on-exceptions-approved --artifact-dir /absolute/isolated-object-copy
```

每个根图是一个事务：技能 → 每位专家一个 MCP 连接器 → 内嵌专家 → 顶层专家/团。
连接器维持当前配置，不创建版本；技能和专家获得新的正式发布身份。新容器版本按
原标签 patch+1，避开已保存和已发布历史标签；无法解析时使用确定的前向标签。
旧快照、标签、发布证据和文件不改写。抽出的独立行保留容器的 owner、Space、
visibility、listing；`public` 旧别名仅规范化为 `system`。

私有草稿仍是私有草稿；迁移为依赖建立正式版本证据，不会自动将这些草稿展示上架。
作者后续需要按技能/连接器 → 专家 → 专家团顺序完成正常发布，才能创建新引用或安装。
已发布私有资源仍只对本人可见。下架资源保留下架状态，系统迁移可记录已发布但未
展示的依赖；普通写入/审批规则仍禁止新选取已下架目标。

中断后重新预览剩余根图并继续同一批次。重复应用已映射根图返回 0，不创建重复行。
跨根引用只使用新生成的确定版本或已审计映射，绝不选择“当前最新版本”。
应用先在事务外验证并冻结文件字节，再锁定并重读清单。加锁后的来源必须与文件
解析使用的来源完全一致，再将冻结文件纳入预览 digest 比对；来源变化时整根拒绝。
持有清单锁时不做文件或对象存储 I/O。

**锁范围与规模限制：** 当前每个 root 的 apply 仍会对完整有界清单加排他锁
（plugins、关系、版本、发布证据和活动迁移映射，上限每表 10000 行），并非只锁
目标 root 子图。并发上传、审核、发布和安装记录写入中的相关资源读取可能等待或
超时；多 root 的窗口会累加。仅在隔离演练与明确授权的维护窗口执行，暂停并发
内容写入，逐根限速并观察锁等待。超过清单上限会拒绝；更大规模须先设计 root
子图锁定或分页证据协议。按 root 分事务不代表不同 root 的锁范围互相隔离。

## 3. 回滚与保留

```bash
go run ./cmd/marketplace-stock-migration --all-spaces --rollback \
  --batch-id rehearsal-001
```

回滚先比较根资源及新增子资源的版本、发布指针、内容摘要、可见性和展示状态。
存在后续编辑/发布/下架时整批回滚停止，不覆盖编辑者状态。确认一致后恢复原根指针、
包和关系；保留新行、新版本和文件，将曾发布的新资源下架，将从未发布的新增资源
恢复草稿；批次标记 rolled_back，不能再次 apply。没有删表、删历史或文件 GC。
回滚前应停止新引用写入；此工具只退休本批次，不会更改服务 feature flag。

## 4. 当前明确隔离的情况

- 所有权/Space 不明确、跨根共享内嵌资源、来源快照缺失或与当前内容摘要不一致。
- 当前草稿与活动发布版本分离、仍有待审申请、引用没有确定版本或审计映射。
- 缺失/损坏文件、越权对象 key、二进制、超限、不可安装的技能 YAML 或重名专家。
- MCP 非占位 env/header 值、URL 用户信息、已知密钥查询参数/命令参数。
  只报告 ID 和固定异常码，不复制或静默清空值；这不是通用密钥扫描器。
- 物化后的元数据包超过现有 1 MiB 契约、单文件超过 1 MiB、51 文件/技能、500 支持
  文件/安装图，以及保守转义后的安装载荷预算超限。不会截断内容或绕过原限制。

当前文件策略是把已验证文本复制到新包，未增加远端对象复制/大包再分片写入策略。
大于元数据上限的合法旧包进入异常清单，需要单独确定 spill/freeze 方案后处理。
同名技能由 ID+版本安装后缀解决；不同专家同名会隔离，不擅自修改市场展示名称。
默认每份内嵌副本创建独立资源，不自动去重；没有仅按名称/URL 复用的路径。

## 验证

```bash
go test ./internal/backfill/stockmigration ./cmd/marketplace-stock-migration ./internal/service/plugin
# 使用隔离 TEST_MYSQL_DSN，按仓库测试 helper 创建并清理独立库：
go test ./internal/db -run TestStockMigration -count=1 -v
go build ./...
go test ./...
```

实物演练涵盖中断后继续、重复执行、跨根精确 pin、SQL 中途失败无残留、根/子资源
后续编辑阻断回滚、私有/Space 状态保留、原历史不改、原本未发布版本不伪造 release、
带摘要的 stored 文本、可信旧 ZIP、来源文件变化阻断 apply，以及保留历史的回滚。

## P 模型存量 pin 回填（独立子命令）

`pin-backfill` 是 P 模型切换期的一次性显式变更，不属于上面的存量资源迁移，
也不会在服务启动或读路径自动执行。

```bash
# 只读预览：输出确定性 plan（内含 JCS SHA-256 plan_digest）
go run ./cmd/marketplace-stock-migration pin-backfill preview --output /tmp/pin-plan.json

# 离线授权后按 source version 逐事务应用
go run ./cmd/marketplace-stock-migration pin-backfill apply \
  --plan /tmp/pin-plan.json \
  --plan-digest <plan_digest> \
  --authorization /absolute/authorization.json
```

- 与 `preview/apply/resume` 共用 `MARKETPLACE_STOCK_MIGRATION_DSN` 和
  `MARKETPLACE_MIGRATION_TRUST_CONFIG`，但使用固定的
  `pin-backfill@v1` source identity 与独立 scope；授权 artifact 必须绑定
  plan digest。
- 只修复已正式发布 Expert/Team 快照中缺失的 Skill/Expert
  `target_version_id`：同时更新 `relations_json` 与
  `plugin_version_relation_refs`，并刷新
  `plugin_version_reference_index_state`。已有 pin 不会被改写。
- Connector 边永不写入版本；不创建版本、不移动
  `published_version_id`/`current_version_id`。目标不可用、来源在预览后变化、
  plan/authorization 不匹配时按 `plan_stale`/`authorization_invalid` 拒绝。
- 每个 source version 单独提交并写 `plugin_pin_backfill_runs` 审计行；同一
  plan 重放命中审计唯一键，幂等返回 no-op。

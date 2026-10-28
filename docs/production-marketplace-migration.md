# Marketplace 快照与隔离演练工具

`cmd/marketplace-migrate` 提供 `snapshot`、`plan`、`rehearse`、`verify`。本工具不会创建数据库、操作 Kubernetes、部署服务或触发通知。当前交付没有运行生产导出或 test 集群演练；数据校验通过仍不代表 API、权限、下载或 Fleet 安装验收通过。

## 凭据与隔离

连接串仅从环境读取：导出使用 `MIGRATION_SOURCE_DSN`，演练使用 `MIGRATION_TARGET_DSN`。不要把真实连接串写入命令行、仓库、报告或 shell 历史。示例假定凭据已通过运行环境安全注入，不展示其值。

导出账号必须仅有源数据库 SELECT 和对象 GET 权限。导出开启 repeatable-read、read-only 事务，在同一事务中读取表、结构与所有行；不在源建表或运行升级。源导出和目标原始恢复均使用独立的 `ParseTime=false` 连接，保留 MySQL 日期原始表示；转换与 Verify 使用另外的 `ParseTime=true` 连接；两种连接都拒绝 multiStatements、allowAllFiles、明文密码等危险开关。导出包不含 MySQL 账号、授权、存储过程或视图。

所有迁移连接强制使用 MySQL 会话时区 `+00:00`（覆盖 DSN 的 `time_zone`，Go 时间解析同样使用 UTC），确保 TIMESTAMP 导出、恢复及转换保留同一绝对时刻；DATETIME 仍保留原始墙上时间。快照 API 使用独立连接设置时区并在结束后丢弃连接，不把会话设置带回调用方连接池。

原始表行可能含历史凭据，因此目录必须保存在受控归档位置：快照目录 0700、文件 0600，禁止上传 Git 或直接挂载给演练 API。按用户明确要求，MCP 地址、传输配置、参数、环境变量和认证头原样保留，包括历史内容；不替换 test 地址或凭据、不自动禁用 Connector。它们不是导出所用的数据库/OSS 管理凭据，后者仍只能通过环境注入。MCP 结构与全部附件摘要仍须校验，报告和日志不输出配置内容。迁移命令不会连接或调用 MCP 服务。

演练进程不能持有 `MIGRATION_SOURCE_DSN` 或源 OSS access/secret key。目标 namespace 必须显式为 `dmwork-test`，目标库必须预先创建，名称须为 `octo_marketplace_rehearsal_<suffix>` 且不含 prod。连接后还核对实际数据库身份；不允许源目标相同或复用无本次回执的已有表。

这只是对明确参数和数据库实际身份的隔离检查，不会查询 Kubernetes 来证明某个 TCP 地址属于 test。运行人员仍须以目标独立凭据和网络策略限制写入范围。

## 导出完整快照

先设置以下源对象配置之一：

- 本地：`MIGRATION_SOURCE_STORAGE_DRIVER=local` 和 `MIGRATION_SOURCE_LOCAL_DIR`。
- OSS/S3：`MIGRATION_SOURCE_STORAGE_DRIVER=oss`，以及 `MIGRATION_SOURCE_OSS_ENDPOINT`、`MIGRATION_SOURCE_OSS_BUCKET`、`MIGRATION_SOURCE_OSS_ACCESS_KEY`、`MIGRATION_SOURCE_OSS_SECRET_KEY`。可选 `MIGRATION_SOURCE_OSS_REGION`、`MIGRATION_SOURCE_OSS_KEY_PREFIX`、`MIGRATION_SOURCE_OSS_PATH_STYLE=true`。

```sh
marketplace-migrate snapshot --dir /controlled/archive/new-snapshot --source-fingerprint SOURCE_IDENTITY_HASH
```

目录必须不存在。使用 Marketplace 整库所有 InnoDB 基础表，无全局 10000 行截断。逐表生成 schema SQL 和逐行 JSONL，SQL NULL 与 base64 二进制分别保留；生成列表达式保留在 schema 中，插入时跳过生成列，普通 DEFAULT_GENERATED 列保留。标识符和 CREATE TABLE 严格校验，拒绝多语句、跨库引用、DEFINER、外部表引擎等；某些不在支持子集的复杂 SHOW CREATE TABLE 会显式失败。

对象从所有表的已知 JSON/sidecar/ref 指针载体盘点，包括历史、审核快照、图标等，读取真实字节。每对象上限 256 MiB，总计 10 GiB；超过预算阻断，不截断或跳过。读取本地对象拒绝路径逃逸、符号链接和非常规文件；OSS 适配只调用 GET。每个 blob 用内容地址保存，`object-index.json` 将原 key 映射到 blob/size/SHA256，索引和 blob 均注册进最终 manifest，最终 snapshot ID 包含这些内容。不存在的对象会使导出失败，失败目录保留作受控诊断，不能拿数据库阶段的中间包演练。

DB 与 OSS 没有跨系统原子快照；索引明确记录 `atomic_with_database=false`。工具保全本次读取的对象字节，并在后续解析活动包时核对已有摘要/size，但这不能证明未版本化历史对象在数据库采集时刻的内容。正式采集须另行提供对象版本、冻结窗口或重采集证据；不可宣称强一致。

## 离线计划

平台资源附件使用 `platform/plugins/<最终资源ID>/attachments/versions/<SHA256>`，不创建或借用 Space；Space/私有资源继续使用对应 Space 的附件路径。脚本和后端共享 `internal/artifactscope` 归属判断，平台路径仅允许同一资源 ID 的 system 资源读取；历史 system 行如果原本就有真实 Space，仍兼容其原 Space 附件。下载、历史文件、安装解析与版本保存使用同一归属规则，校验摘要和大小，平台版本对象禁止覆盖、删除。普通更新暂不允许 system 降为 space/private，避免仅改变当前行归属而破坏旧平台历史；这不是迁移所需操作。平台附件支持不代表 Fleet 的文件数量、文本/二进制安装契约已扩大，安装兼容性仍须单独验收。

版本校验区分内部快照序号与业务版本号：`plugin_versions.version` 按现有后端约定保存 `1`、`2` 等序号；`declared_version`、`plugins.current_version` 与 `plugin_version_releases.version_label` 必须使用同一合法三段式业务版本号，例如 `1.0.0`。历史字段原样保全，新快照选取未占用的合法版本号。发布节点必须同时写入发布记录；专家引用的 Skill 和专家团引用的专家必须精确指向本次计划中有正式发布记录的版本，草稿不能冒充发布版本。计划与 Apply 均在写入前阻断不合法标签或缺失发布的引用，Verify 另外回读业务版本号、发布记录及 current/published 指针的一致性，避免只凭快照存在就判定可引用。`released_version_required` 不能仅凭内部序号为 `1` 断定根因，应核对实际发布记录与引用版本 ID。

```sh
marketplace-migrate plan --dir /controlled/archive/new-snapshot --policy /controlled/policy.json
```

policy 可省略，格式如下；frontend-dev 的中文 canonical 需要明确源 ID：

```json
{"FrontendChineseSourceIDs":["SOURCE_UUID"]}
```

policy 读取预算 1 MiB，未知字段或多段 JSON 拒绝。计划使用冻结输入的精确内容与权限，不访问源库或源存储、不写目标。stdout 只输出 ID、映射、状态与安全错误码；正文、配置、文件字节、对象 key 和连接串不输出。任何输入/附件完整性/计划 issue 都是阻断项，退出码 2；已有 MCP 配置中的认证值本身不会阻断迁移。

## 显式隔离演练和恢复

让管理员预先创建全新的 MySQL 8.0.21+ 专用数据库和空对象目录。对象目录 basename 必须同时包含 `rehearsal` 和 snapshot ID 前 12 位；其位置与受控归档不得相互包含。首次目标目录必须为空。不要把现有 test API 的数据库或对象目录作为目标。

```sh
marketplace-migrate rehearse --dir /controlled/archive/new-snapshot \
  --namespace dmwork-test --expected-snapshot SNAPSHOT_SHA256 \
  --target-storage-dir /isolated/rehearsal-SNAPSHOT_FIRST_12 --policy /controlled/policy.json
```

不加 `--apply` 只生成计划，不连接目标、不写数据库或对象。执行使用同样参数加 `--apply`：

```sh
marketplace-migrate rehearse --dir /controlled/archive/new-snapshot \
  --namespace dmwork-test --expected-snapshot SNAPSHOT_SHA256 \
  --target-storage-dir /isolated/rehearsal-SNAPSHOT_FIRST_12 --policy /controlled/policy.json --apply
```

`SKIP_MIGRATION=true` 会阻断。全过程先验证所有快照文件、对象索引、输入、冻结附件摘要及计划，再允许任何目标写入。先逐表恢复，随后原子创建 `marketplace_rehearsal_progress` 及 snapshot/plan/storage identity 行，再升级 schema、复制原始与新活动图对象、Apply、Verify。MySQL 8.0.21+ 的原子 CREATE TABLE AS SELECT 用于消除进度表存在但身份行未插入的中断窗口。

恢复器使用自身来源标记和每表事务回执；成功表会核对真实行内容摘要，未完成表只向空表插入，拒绝无回执的数据。生成列不参与插入；零值 AUTO_INCREMENT 保留。外键校验在独立连接暂时关闭，恢复后逐约束检查实际孤儿行再启用；不会只把开关打开就声称数据有效。独立连接结束后丢弃，避免会话设置影响后续连接。

整轮演练有数据库命名锁。重复相同命令会匹配 progress 身份并跳过已完成的原始恢复，否则 schema 升级和转换后的行会被错误地当成快照原始行。Apply 有独立图事务和确定性回执，已完成节点会验证实际内容；更改 snapshot、policy/plan 或目标存储位置会拒绝续跑。更换输入须新建目标库和新目录，不修改回执绕过检查。

对象写入采用私有临时文件、完整摘要校验、原子不覆盖链接和读回校验；进程中断留下的临时文件不会被当成已完成对象。已有相同字节可复用，已有不同字节拒绝，不覆盖现有对象。原始归档不直接提供给 API；演练从校验后的副本恢复，MCP 配置按用户要求原样保留。

## 只读验收

```sh
marketplace-migrate verify --dir /controlled/archive/new-snapshot \
  --namespace dmwork-test --expected-snapshot SNAPSHOT_SHA256 \
  --target-storage-dir /isolated/rehearsal-SNAPSHOT_FIRST_12 --policy /controlled/policy.json
```

`verify` 重新验证归档、身份、目标对象字节与数据库图，不修补缺失对象、不运行迁移、不写数据库。报告中的 `complete` 仅指数据转换与校验；`install_verified` 固定为 false。还需要独立服务的 system/space/private 权限、API 读写、发布/历史下载、固定版本 Fleet 安装和大包/二进制兼容性验收。

退出码：0 表示该子命令的数据工作通过，2 表示参数错误、阻断或校验失败；不能把 plan 的 0 当作已执行迁移。stdout 不含内部底层错误或真实凭据。

## 本地构建与验证

```sh
go build ./cmd/marketplace-migrate
go test ./cmd/marketplace-migrate ./internal/migration/...
docker build -f Dockerfile.migrate -t marketplace-migrate:local .
```

容器以 UID/GID 10001 运行，挂载受控目录时由管理员授予必要权限，不通过全局 chmod 放开原始归档。Dockerfile 只生成工具镜像，无 API listener 或定时任务，不自动访问 prod/test。

完整恢复/续跑与转换的 MySQL 集成测试使用 `MIGRATION_TEST_MYSQL_DSN`，与现有数据库测试的 `TEST_MYSQL_DSN` 分开。该连接串必须指明 `octo_marketplace_rehearsal_` 开头的测试数据库且不得启用 `multiStatements`。测试只创建并清理本次专属的临时演练库；未设置时明确跳过。GitHub CI 为此注入本地 MySQL 服务的专用测试连接串，不使用集群或生产凭据。

本次不修改 `PLUGIN_VERSION_MANAGEMENT_ENABLED`、`PLUGIN_REFERENCES_ENABLED`、`PLUGIN_PINNED_INSTALLATION_ENABLED`。它们控制后端新版能力的启用，不负责运行迁移；脚本执行、功能启用和真实 API/Fleet 验收分别记录结果。

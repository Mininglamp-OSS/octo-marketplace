# Marketplace 单库原地迁移

迁移命令只连接 `MYSQL_DSN` 指向的当前数据库，在这个库里完成专家团、专家、Skill/MCP 的拆分、去重、版本和引用更新。生产数据复制到 test 是运行前的环境准备，不能通过这个命令导出、恢复或跨库同步。命令不部署服务、不修改三个功能开关、不自动升级业务表结构。

## 运行

旧的 `plugin-backfill`、`marketplace-stock-migration` 命令、专属实现与旧 backfill 镜像入口已删除。资源转换统一使用本文命令；数据库历史 SQL 与旧审计记录继续保留。

```sh
go build -o /tmp/marketplace-migrate ./cmd/marketplace-migrate

# 默认只预览，不写数据库、对象或恢复文件。
/tmp/marketplace-migrate --database CURRENT_DATABASE --policy /secure/policy.json

# 一次执行；中断后用完全相同的参数重试。
/tmp/marketplace-migrate --database CURRENT_DATABASE --policy /secure/policy.json \
  --apply --state-file /secure/marketplace-run.state

# 对同一次执行做只读复核。
/tmp/marketplace-migrate --database CURRENT_DATABASE --policy /secure/policy.json \
  --verify --state-file /secure/marketplace-run.state
```

使用 Marketplace 服务已有的环境变量：`MYSQL_DSN`、`STORAGE_DRIVER`；本地存储用 `LOCAL_STORAGE_DIR`，OSS/S3 用 `OSS_ENDPOINT`、`OSS_BUCKET`、`OSS_ACCESS_KEY`、`OSS_SECRET_KEY`，以及原有 `OSS_REGION`、`OSS_KEY_PREFIX`、`OSS_PATH_STYLE`。必须使用当前环境实际的对象存储配置。凭据通过环境安全注入，不放在命令参数、文档或 Git 中。

`--database` 必须与连接串及服务器返回的当前库名一致；首次执行还将实际数据库名、MySQL server UUID、存储位置和选择规则绑定到恢复文件，重试不能换库或存储。没有生产库名称限制，也没有所谓原库、目标库。连接强制使用 UTC 会话，禁止多语句等不必要的连接选项。

恢复文件是本次计划与必要文件字节的私有断点，不是整库备份；以 0600 权限创建、校验摘要且拒绝覆盖。它可能包含 MCP 配置和附件，必须保存在受控目录，禁止提交 Git。首次执行前保存，重试读取同一计划，避免根据已被迁移的数据重新生成 ID。不要删除文件后重新规划一个已部分执行的库。

## 转换规则

- 按“平台共有、每个 Space、私有 owner”分别处理，禁止跨 Space 合并或扩大权限。系统 Skill、MCP、专家、专家团均保持平台共有，不能为了附件或拆分归入某个 Space。
- 先解析专家团及专家中的依赖，写入时按 Skill/MCP、专家、专家团的顺序执行，引用指向明确的已发布版本。
- `frontend-dev` 选择中文版本。可在 policy 中通过源资源 ID 明确指定；未能明确选择时阻断并报告，不随意取英文版本。
- `fullstack-dev` 的不同内容各自保留；`wechat-article-search` 与 `online-search` 的不同实现各自保留。其他同名资源在同一归属范围内按 `updated_at` 从早到晚选择第一份，相同时按 ID 排序。资源映射用于同步修正引用。
- MCP 的地址、配置、认证及扩展字段保留，不替换成 test 地址、不调用 MCP 服务。
- 旧内嵌 Skill 的市场描述为空时，只从已解析的原始 `SKILL.md` YAML 文件头提取明确的 description，兼容 CRLF 与多行文本；已有非空描述不覆盖。不改写原文件正文，也不在旧记录或历史版本上回填描述。文件头缺失、损坏、描述为空或只是占位正文时，报告 `skill_description_unavailable` 并阻断，禁止发布空描述条目。
- 保留已有历史版本，新建有效版本及发布记录，更新当前版本、发布指针和固定版本引用。内部 `plugin_versions.version` 是后端使用的快照序号（如 `1`）；业务版本 `declared_version`、`current_version` 和发布记录的 `version_label` 使用合法三段式（如 `1.0.0`）。不能只改一个字符串就认定解决 `released_version_required`，还要核对发布记录和引用 ID。
- 平台附件写入 `platform/plugins/<最终资源ID>/attachments/versions/<SHA256>`，Space 附件使用对应 Space 路径。脚本和后端统一检查归属、文件长度与 SHA256；下载、历史版本、安装及保存版本均使用这套规则。

可选 policy 格式：

```json
{"FrontendChineseSourceIDs":["中文版本源资源ID"]}
```

## 执行与验证边界

请使用迁移前完整数据验证。若市场描述为空但完整原文件头仍有 description，新脚本可以据原文补齐该字段；若原文件、文件头或成员关系已丢失，新脚本不能凭空恢复缺失内容；需要从完好的生产数据副本或备份重新准备 test，再核对四类资源的卡片信息、可见性和专家团拆分结果。

复制线上旧库到 test 后，先由配套新版 Marketplace 后端按正常启动流程完成业务表升级，再运行数据迁移。脚本不会代替后端升级结构。读取业务输入使用单个 Repeatable Read 只读事务；写入前检查所需业务表结构。实际执行创建三个迁移进度/映射表，以每个资源的事务提交保存断点，禁止冲突覆盖。对象先以不可变方式写入并回读校验，再写入数据库；失败后使用同一恢复文件重试。历史文件和已有版本不删除。

运行期间应暂停 Marketplace 写入；命令的数据库锁只防止重复运行本命令，不能阻止业务服务编辑。原地迁移的数据库备份及恢复方案由运行环境另行准备；本工具不承担备份职责。

失败不代表整次执行已经回滚：对象存储与数据库没有共同事务，数据库也是逐个资源提交。中断可能留下已写入但暂未引用的不可变对象，以及已完成的前序资源。恢复文件保存本次完整对象清单、摘要和必要字节；数据库进度表保存已提交资源的校验回执。保留该文件，用原命令重试：已有对象必须通过内容校验才可复用，已提交资源必须通过回执校验才会跳过，未完成资源继续执行。不要删除进度表或恢复文件，也不要手动删除历史对象。若遇到源数据变化、对象内容冲突或回执不符，应停止写入并核对变化；无法恢复原计划时，按运行前备份方案恢复数据库及对应存储后重新执行，不能换一个新恢复文件强行继续。

`complete=true` 表示本次计划的数据库、版本、发布、引用及对象校验完成；`install_verified=false` 明确表示尚未完成真实安装验收。仍需在 test 检查平台/Space 可见性、历史下载、专家及专家团安装、MCP 配置保留和重试行为。生产数据复制到 test 时，数据库及其引用的附件都应可读取，并保持 test 的业务连接配置指向 test。

## 本地检查

```sh
go test ./cmd/marketplace-migrate ./internal/migration/...
go test -race -shuffle=on -count=1 ./...
go vet ./...
```

`internal/migration` 的真实 MySQL 集成测试仅在设置 `MIGRATION_TEST_MYSQL_DSN` 时运行，测试连接必须使用 `octo_marketplace_rehearsal_` 前缀的专用测试库。这个限制只属于会创建、删除临时库的测试夹具，不限制实际迁移命令的库名。未设置时跳过，不能据此声称已经在真实 MySQL 或集群完成迁移。

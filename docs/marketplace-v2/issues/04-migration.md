# MP-04 · CLI 冻结计划驱动的历史资产转换与数据安全回滚（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) D5（CLI + 离线授权、两次 preview/冻结 `plan_digest`、apply 只认冻结计划、数据安全回滚、不做 A29/A30 HTTP）、B 模型（迁移生成引用式资源，按目标 ID 引用、读时解析最新，不伪造 pin）、D6（private/受众）、D10（不恢复指针/legacy）。本文是本地 issue 草案，不表示已创建远程 issue。

## 目标（Goal）

把旧 embedded Skill、inline MCP、embedded member 转换为独立资产引用；其中 Skill/Expert 必须成为所属 system/Space 市场中独立可见、可引用的正式资产，不能只做后台拆表后继续隐藏。在一个交付切片内闭合 inventory、两次只读 preview、人工批准冻结计划、apply、映射、幂等、CAS、审核等待和中断恢复。

**生产迁移只经 CLI + 离线授权完成，不新增 A29/A30 HTTP。** 硬依赖：[MP-02](02-dependency-governance.md)，并传递依赖 MP-01；复用版本、正式发布证据、文件快照、依赖策略和统一锁序。可并行协作：与 MP-03、CL-01、CL-02 并行；向 REL-01 提供计划、迁移映射和对比基线。编码与域内验收不依赖 MP-03，不调用 Fleet。

## 背景（Background）

D5 已确认：生产路径为 `marketplace-stock-migration` CLI + 离线授权（命令名/子命令以实现时确认为准）；两次只读 preview——首次 discovery（来源 inventory、候选、关系、缺失选择），第二次冻结（显式标签/映射/Connector server 分组/归属）并写 `plan_digest`；apply 只认人工批准的冻结计划，不接受手改摘要。生产迁移仍需人工批准计划，CLI 不是绕过授权。

B 模型下迁移产物是引用式资源：Expert→Skill、Team→Expert 存目标 ID，`target_version_id` 至多作审计；公共/A34/安装读时解析目标最新发布版。迁移不生成权威 pin、不把当前最新写成不可变依赖、不做自动 latest 猜测；历史 pin 仅作证据保留。

回滚必须数据安全：不删表/不删文件/不覆盖旧快照；不把 `current_version_id/published_version_id` 改回旧值；需要停止新分发时用下架/部署隔离；内容恢复从旧历史生成新版本（forward-fix）；保留 batch/mapping/attempt/audit。

## 承重行为（Load-bearing）

1. **CLI-only、离线授权。** 不注册 `POST /admin/plugins/legacy_import/preview`、`POST /admin/plugins/legacy_import/apply` 或任何替代 HTTP；不把 CLI 包装成 Admin 后门。离线授权制品绑定 `plan_digest`、操作者、来源范围与执行批次，由有权者签发；apply 校验授权与计划一致，二者任一不符即拒绝。授权制品格式、Ed25519/JCS 签名、部署信任配置、主体校验、有效期、撤销和续签恢复固定按 02 §5.1；不得退化为口令或可手改的明文标记。
2. **两次只读 preview。** 第一次 discovery 只读来源并输出 inventory/候选/缺失项；操作者补全后，第二次以同一来源重验并输出冻结的完整计划与 `plan_digest`。两次都不写业务对象、审核、映射、对象存储或 receipt；第一次建议不等于批准。
3. **apply 只认冻结计划。** apply 精确提交第二次 preview 的冻结计划、`plan_digest`、非空来源身份和离线授权；任何映射/范围/标签/关系/server 分组改动都必须重新 preview。禁止手改计划后沿用原摘要；禁止 apply 自行替换标签或补 latest。
4. **B 引用。** 新建/复用的 Expert、Team 关系以目标 ID 表达，`target_version_id` 可空、仅审计；不使用历史 pin 作为公共/安装解析依据。迁移完成后依赖跟随目标最新发布版，代价（不同时间读取/安装可能不同）写入交付说明。
5. **create/reuse/update_source。** 数据库根用 `update_source` 保持原 Plugin ID；ZIP 根用 `create`；子节点 `create` 或明确 `reuse`；不自动按名称/摘要猜复用，不预填假 ID/latest。新节点标签/范围/关系/引用由冻结计划显式给出，父节点从 map 取真实目标 ID。
6. **Connector。** 默认保留原 server 集合映射到一个 Connector；只有冻结计划显式拆分才拆，server key 不重复/不丢；Connector 无业务 Version，审核用 `effective_revision/digest` 观察值；计划只存 server 身份/脱敏 fingerprint 与经授权的配置来源，不存 Secret。
7. **审核。** CLI 生成的新版本走现有发布/审核：默认自动可自动批准；人工模式 run 为 `waiting_review`；CLI run 状态 `completed|waiting_review|blocked`；不直接插 release 绕过审核，不自动重提已拒绝审核。批准后依赖继续浮动；Skill/Expert 发新版不要求重新审核引用方。
8. **数据安全回滚。** 不删表/不删文件/不覆盖旧快照；不回切 `current_version_id/published_version_id`；不恢复 embedded/inline；内容恢复从旧历史生成新版本并走原发布/审核；保留 batch/mapping/attempt/audit；停止分发用下架/部署隔离。
9. **独立可见。** 迁移后的 Skill/Expert 必须在对应市场独立可发现；只有全部节点发布/列表资格就绪才 `completed`；`waiting_review|blocked` 不得报完成。
10. **幂等与恢复。** 同 key 关联原 run/计划；同 key 换摘要/范围/映射/输入冲突；先查 receipt 和 map，再恢复未提交步骤；不能重建已提交目标、不能把 waiting_review 一次回放永久冻结为不可恢复。

## 非目标（Out of scope）

自动生产迁移、A29/A30 HTTP、跨范围公开、猜测 MCP 映射、修改原历史、补 latest pin、共享目标级联删除、实现安装器、支持二进制、自动改写硬编码路径、A09 restore、同名共存改造、Fleet/Daemon 改造。纯本地 Expert/Skill 不属于本次市场迁移。

## 源码路径与所有权

- Marketplace 扩展现有 `cmd/plugin-backfill/`、`internal/backfill/plugin/`，增加本协议专用 CLI 入口/模式；不能把现有历史回填 Runner 当作已实现本协议。复用 `internal/service/plugin/{service,container,review}.go` 的授权、校验、保存与发布能力，不复制业务规则。
- 不接入 Admin HTTP handler；CLI 调用与 Admin 相同的领域服务，不能绕过 Admin/审核权限。Repository 接入 `internal/repository/plugin/{write,history,review}.go`；模型在 `internal/model/plugin.go`，增量 DDL 在 `migrations/sql/`。
- **本 issue 拥有** `plugin_dependency_migration_map` 的 DDL、查询和步骤恢复；拥有迁移 apply 对 MP-02 所建 `plugin_mutation_receipts` 的使用，不能另建同义幂等账本；CL-01 只是调用者，不创建后端表。
- map 记录 run、非空 `source_kind/source_scope/source_identity`、可空 source Plugin/Version、`source_digest`、`plan_digest`、`source_node_key`、可空 target Plugin/Version、`step_state`、`source_commit_json`、`error_reason`；唯一键 `(source_scope,source_identity,plan_digest,source_node_key)`。root reserved 记录先仲裁唯一 run，未创建目标可为空；`source_commit_json` 存 `output_version_id/output_edit_revision/review_id?/frozen_content_digest`。字段、状态及来源身份以 API §5 与 CLI 契约为准，不能用 NULL 来源列代替唯一身份。
- 每个成功目标创建与对应映射必须同事务提交；版本/refs/release/effective 仍由其原业务所有者写入，转换器不能绕过审核直接插入发布证明。**本 issue 的 `owns_api_ids` 为空**：交付的是 CLI，不是 HTTP endpoint。

## 接口边界（CLI）

- 交付一组只读/写入边界清晰的 CLI 操作（命令名与参数以实现时确认为准）：`inventory`（只读来源、输出脱敏清单与发布证据）、`preview --discover`（第一次只读，输出候选/缺失）、`preview --freeze`（第二次只读，返回冻结计划与 `plan_digest`）、`apply`（只接受冻结计划+摘要+离线授权+幂等键）、`verify`（核对新图/文件/发布资格/独立可见）、`resume`（按 run/map 恢复未提交步骤）。不新增 HTTP 路径、Admin 按钮或响应包络。
- `inventory` 与索引核对由 CLI 提供；preview/apply 调用同一转换领域服务，不能成为绕过 Admin 鉴权、批准计划和审计的后门。已有命令的 `-migrate`、普通 `-mode apply` 或旧 phase 不构成本次数据迁移批准，不能在服务启动/DDL 执行时顺带转换。
- 来源身份：数据库 `plugin_id@version_id`；ZIP 原字节 SHA-256（若 CLI 支持 ZIP，必须显式；本期可只支持数据库来源，ZIP 另行授权）。数据库来源要求 current Version 和 `expected_edit_revision`，ZIP 来源无伪造 Plugin/Version/CAS。
- 冻结计划至少包含：来源身份/digest、每个 `source_node_key` 的 create/reuse/update_source、显式目标 ID（reuse）、新标签/分类/受众、完整引用关系（目标 ID，版本仅审计）、Connector server 分组、逐节点依赖顺序、`plan_digest`。apply 重新计算摘要，任何不一致 409，不静默重选目标。
- 错误与结果使用 CLI 结构化输出（脱敏）：`completed|waiting_review|blocked`、逐源/逐节点已提交结果、`run_id`、`plan_digest`、安全 reason；不得输出 Secret/Token/原始 mcp.json。

## 实施步骤

1. **只读 inventory。** 按类型、owner、Space、visibility、embedded 图和来源版本归集，记录文件可取性/完整摘要、发布证据及异常 reason。不得把无展示位当成 embedded，不得把 current 非空当成已发布。
2. **核实发布身份。** 仅采信可审计 approved review、Admin 正式发布等证据；为旧资源记录证据引用和判定。无法证明正式发布的历史保留原文，不伪造 release、历史 pin 或关系索引，不允许直接进入新分发。
3. **第一次 preview（discovery）。** 对 ZIP 校验路径、软链接、文本编码和文件预算；对数据库来源冻结 current Version、edit_revision 与完整 `source_digest`；递归枚举 Team 内嵌 Expert 及这些 Expert 内嵌 Skill，输出每个源节点的目标候选、独立市场展示归属、差异、阻断原因和依赖顺序。`source_node_key` 在相同来源内稳定、唯一，包含唯一 root；同内容也不能因按名称/摘要去重丢失两个不同来源节点或成员关系。摘要覆盖完整文件与关系，不仅比较 `plugin_hash`；旧 `skill/ref.json`/ZIP 外部正文须在受控读取后冻结真实全部字节，不能把历史 stub 当完整 Skill。
4. **形成批准映射（仍只读）。** Skill 候选需内容/附件字节及范围一致且 standalone、具备正式发布版本；Expert 候选还必须包含规范化 Skill 引用与 Connector 身份图。唯一候选也只建议，不自动选中；复用明确 target Plugin（Version 仅审计），新建节点的标签、范围、完整关系及引用 create 输出的 `source_node_key` 按冻结计划固定，父节点在步骤提交后从 map 取得真实目标 ID，不预填假 ID 或 latest。批准前完整覆盖所有枚举节点，不能遗漏、增加图外节点或把嵌套 Skill 藏进成员附件。
   - 精确使用 API §5 的 `MigrationPlanResource/Relation` 语义（CLI 复用同一 schema）：数据库根为 `update_source`、ZIP 根为 `create`，子节点仅 `create/reuse`；`origin_node_key` 标识原节点，MCP 合法拆组须完整 server 覆盖，不能新增图外能力。根仍为原 Plugin，不为“来源切换”另建一个根。新建标签和分类、复用目标 ID/Connector effective 观察值全部进入第二次 preview 冻结计划；`target_version_id` 如出现仅作审计，不作为依赖权威。
5. **处理 MCP。** inventory 只输出脱敏 descriptor fingerprint 与 server key/transport 等非敏感描述；不按 URL、名称或 fingerprint 自动指定 Connector。人工选择已有有效 Connector 或批准从可信配置创建；映射仅固定 Connector ID，不制造业务 Version。
6. **处理多 server。** 默认保持原 server 集合并映射到一个 Connector；拆成多个 Connector 必须逐项获批准，完整 server 归组进入冻结计划及摘要，核对原集合完整覆盖。最终 server key 重复、遗漏、额外未批准能力或配置来源不明均阻断，不能以覆盖/清空修复；计划只保存 server 身份/脱敏 fingerprint 及经授权的配置来源，不保存配置 Secret。
7. **核验范围授权与独立展示。** Skill/Expert 按所属市场来源的 owner 及既定 system/Space 受众形成独立条目，不沿用仅因 embedded 身份而设置的隐藏/private 标记；其独立可见已获产品确认，不另设逐项“是否允许独立展示”的批准步骤。仍需核验实际操作者、来源归属、计划范围及复用目标的受众覆盖和访问资格；Admin 身份不豁免跨 Space/private 依赖限制（同 owner private→private 允许，space/system 不得引用 private）。
8. **第二次 preview（freeze）。** 以同一来源重取并重验完整映射、拓扑、标签、归属/受众和文件，输出冻结的完整可批准计划及 `plan_digest`；任何无法确认项仍为 discovery 且不可 apply。人工对冻结计划作书面批准并生成离线授权；修改映射、分组、范围、关系或标签必须再次 preview，不能手改摘要。
9. **拓扑创建与复用。** 按 Skill/Connector → Expert → Team 执行；新建资产与 map 原子记录，对复用也记录经验证的固定映射。新 Skill/Expert 须 `is_embedded=false` 且接入已有可见 default 投放/合法分类，不仅插入独立行；复用目标也必须满足独立展示资格。目标 Skill/Expert 必须有真实 release；Connector 必须有 effective。Space 新目标沿用原自动/人工审核策略，依赖未批准时 run 为 `waiting_review`，不切来源；审核提交与对应恢复状态原子记录，不因重试重复建版本/审核。
10. **冻结来源切换。** 预读图并按统一锁序重验，首次 CAS 原 current Version、edit_revision 和 `source_digest`；校验目标 release/effective、文件、范围及 wiring 后创建新引用式快照/refs（目标 ID，版本仅审计），同事务写来源审计、map 及 `source_commit_json`。恢复先识别自己的输出，不对它重复最初 CAS 或提审；后续 current 新草稿必须保留，原冻结审核正常批准可完成。若发生其他外部发布/拒绝/取消则核对实际版本证据并按契约阻断或完成，不盲目套旧计划、不重激活旧 published。
11. **按原职责发布。** system 使用批准的 Admin 发布权限；private 由本人按既有流程发布；同 owner private→private 依赖可保留，但不得把 private 历史内嵌依赖自动提升成 system/Space 来源依赖；此类来源列出阻断及所缺原有授权，不能暗中缩小库存范围或报 completed。Space 生成冻结审核版本并按原策略自动批准或等待人工批准；任何待审内容都不能直接覆盖 published。来源最终发布切换受 CAS/审核保护，current 新编辑不得被批准回写覆盖。
12. **幂等与恢复。** 同 key、同批准计划关联原 run；同 key 换摘要/范围/映射/输入冲突。先查 receipt 和 map，再恢复未提交步骤，不能重建已提交目标；非终态 `waiting_review/blocked` 不能被一次 receipt 回放永久冻结为不可恢复，按 CLI 协议从同一 run 继续。审核等待恢复必须关联原冻结 version/review，重读审核结果、所有目标资格及 Connector 观察值；拒绝/取消不能记完成，不能自动重提审核绕过决定。completed 才回放终态结果，保持原 key 不可改输入/过期复用。
13. **部分失败闭合。** 返回每个来源和节点已提交结果、等待项及失败 reason；目标已建但来源 CAS 失败时保留可追踪 map，不报整体 completed。等待审核为 `waiting_review`；已提交部分步骤后失败按契约返回 `blocked` 及安全 run/已提交 ID/reason，不自造成功输出。源被编辑后须重新 preview/批准计划，不能在旧批准下自动套用新编辑；新计划不自动复用旧目标/授权，需重新显式批准复用。
14. **核对与保留。** verify 比较新图、完整文件摘要、成员角色/Leader/server key、发布证据及每个拆出 Skill/Expert 的独立列表资格，确认所有节点/来源切换及发布结果就绪才 `completed`。只回填可确定的历史 refs；原快照、embedded 记录、原文件保持证据，旧格式记录不可再分发。不调用旧 `RebuildGraph/DeleteGraph` 清理历史子树，不随新父资产删除独立目标。对象清理仅限本次确实新建且无任何资源 current/历史/审核/安装计划引用的对象。

## 异常处置

- 缺文件、凭据来源不明、二进制、硬编码旧目录、归属不明、无法证明目标发布资格：逐源阻断并保留定位信息；人工修复后重新 preview、批准，不截断文件、不转文本、不猜历史。
- 计划、日志、错误和报告禁止出现 Header/env 值、Token、可能含 Secret 的 command 参数或原始 `mcp.json`；不读取环境配置文件来生成报告。配置内容必须由受授权的现有业务路径接收，不能嵌入迁移账本。
- map 的 Connector `target_version_id` 为空；审核使用 `effective_revision/digest` 观察值，不能用不存在的 Connector 业务版本替代。审批期间有效配置改变按统一规则报 `dependency_changed`。
- 冻结计划/摘要不一致、离线授权与计划不匹配、来源 CAS 失败、审核拒绝/取消、跨范围依赖：逐源 `blocked`，保留 map 与历史，不重试成新计划，不自动改标签或换目标。

## 逐项验收（Acceptance）

- [ ] `inventory`/两次 `preview` 在真实隔离 MySQL + OSS 上无业务写入；有完整来源列表、发布证据与可定位异常，输出不含 Secret。
- [ ] 第一次 preview 的建议不构成批准；第二次冻结返回完整计划与 `plan_digest`；修改映射/范围/关系/标签/server 分组后必须重新 preview，旧摘要不可 apply。
- [ ] apply 只接受冻结计划+`plan_digest`+离线授权+非空来源身份；手改计划、摘要不符、授权范围不符、来源变更逐项 409/`blocked`，不静默重选目标。
- [ ] **无 A29/A30 HTTP**：不存在 legacy_import preview/apply 路由或 Admin 按钮；服务启动/DDL 不自动转换；无 HTTP 旁路可绕过离线授权。
- [ ] 同名不同内容不复用；同内容不同权限不越权；Expert 的关系差异不会被 `plugin_hash` 相同掩盖；MCP 从不自动映射。
- [ ] 多 server 默认保持集合，批准拆分可验证完整覆盖；重复 server key、越过既定 system/Space 受众或操作者无权的公开均阻断；同 owner private→private 合法，space/system 引用 private 阻断。
- [ ] 以旧 Expert 内嵌 Skill、旧 Team 内嵌 Expert 且该 Expert 再内嵌 Skill 为样本，迁移及现有发布/审核完成后，分别通过真实 A01 确认 Skill/Expert 为独立可见条目，检查对应 default 投放及发布资格；原 Expert/Team 引用正确目标 ID（版本仅审计），不残留 embedded/private 隐藏依赖。范围外用户不可见；待审核或不满足既有列表条件时不伪报迁移完成。Web/Buddy 真实列表展示由 REL-01 验证。
- [ ] 新建目标与映射同事务；依赖未批准返回 `waiting_review`，批准后可恢复；拒绝、取消或 effective 变化不会误完成。
- [ ] 冻结计划后编辑来源必被 CAS 拒绝；目标已建的来源失败不丢 map、不重复创建，也不被其他成功来源掩盖。
- [ ] 同计划重跑和同 key 重试返回同一资源/版本；换计划冲突；逐个提交点中断后恢复不重复创建、不跳过校验。
- [ ] 同一原计划以两个不同幂等 key 并发 apply，仅一套目标/映射提交；ZIP 来源身份非空，不能利用 MySQL NULL 唯一约束缺口重复创建。
- [ ] 两次 preview 均无写入；新建 Skill→新建 Expert→Team 从 map 精确绑定真实目标 ID/Version（版本仅审计），不要求操作者预测新 ID，无 latest 占位；迁移产物按 B 在后续读取/安装解析最新依赖。
- [ ] `waiting_review` 同 key 可在原审核批准后继续同 run，重放不重复提交审核；拒绝/取消及旧冻结版与 current 分离均不误完成。竞争的不同 key 和中断恢复都不重复创建目标/版本/审核。
- [ ] 已发布、未发布、待审核来源分开处理；没有证据的历史不补 latest/release；原快照、文件和 embedded 证据字节不变。
- [ ] 缺文件、二进制、来源不明 Secret、硬编码目录均有阻断结果；清理失败上传不删除共享历史对象。
- [ ] 验证新图引用、Connector 身份/effective、refs、权限、成员角色与文件摘要，无 Fleet 网络或安装副作用；实际安装比较明确交给 REL-01。
- [ ] 只有 DDL 授权或旧 backfill 命令权限时不能执行新生产转换；private/无发布证据/超批准范围来源逐项保留阻断，不提升受众、不漏报或当作未执行的新来源。
- [ ] **数据安全回滚：** 演练只退兼容二进制、保留增量 schema/对象/attempt/map；不删表/不删文件/不覆盖旧快照；不把 `current_version_id/published_version_id` 改回旧值；内容恢复从旧历史生成新版本并走原发布/审核，不恢复 embedded/inline。
- [ ] 回滚后重新 inventory/核对 CAS 与批准范围，确认未决安装与对象完整性后再放行；不以重启、清缓存或恢复旧执行器代替修复。

## 静态源码依据与复用限制

- 已存在 `cmd/plugin-backfill/main.go:21-88` 及 `internal/backfill/plugin/backfill.go:105-175,1005-1068`：旧 phase/Runner 为旧表确定性回填，直接写 plugins/versions/relations 并将历史行标为 published；不是本次批准计划、release 证明、CAS 及逐节点恢复实现，不得借此推定生产执行已授权。
- `internal/backfill/plugin/backfill.go:177-225,480-504`：旧 default 投放只覆盖非 embedded 行，`snapshotSkill` 的 `SKILL.md` 可能是引用外部对象的 stub。`internal/service/plugin/container.go:381-442,493-534` 仍创建 Team 成员及 Skill 的 embedded 行；必须递归读真实完整文件并新建独立目标，不能仅复用旧回填结果或翻转标记。
- `internal/repository/plugin/read.go:187-247`：公共列表要求非删除、`is_embedded=0`，指定 scene 还须 visible placement；只拆关系/建表不足以独立展示。新目标发布/审核沿用现有 default 投放规则，不能增设隐藏资产绕开本产品确认。
- `internal/service/plugin/container.go:220-268` 保留根 owner/Space/visibility 但调用 `RebuildGraph` 替换旧子树，不适合作为保留历史证据的迁移 apply；新转换必须调用已接入 MP-01/MP-02 版本事务的保存/发布能力，不能照搬现有重建/审核覆写 current 行为。
- `internal/backfill/plugin/mapping.go:218-222` 的旧 Connector sanitizer 会清空 secret-shaped 值；本迁移发现未知凭据来源时应阻断并仅报字段位置，不能借 sanitize 把原能力悄悄改成空配置后成功。以上为静态依据，不宣称迁移已实现或通过生产验证。

## 测试与 smoke 交付（Verification）

- 转换器表驱动测试覆盖上述候选、授权、多 server、文件及异常矩阵；CLI 集成测试覆盖两次 preview 只读、冻结摘要、apply 幂等/CAS/离线授权和逐源结果，不用 mock 成功代替数据库恢复证据。
- 隔离 MySQL + OSS 演练：导入 system/Space/private 三类来源，逐步制造提交前/后中断、审核等待/拒绝、编辑竞态和部分失败，重跑后对比 receipt/map/版本/对象摘要及审计；另行演练数据安全回滚（兼容二进制、无指针回切、从历史 forward-fix）。
- 交付批准计划样本、脱敏 inventory、映射与前后能力基线给 REL-01；样本包含共享依赖、Team 和多 server Connector，不运行真实安装来充当本 issue 前置。B 语义下同源两个 Expert 解析同一 Skill 最新版，不再构造“两个固定版本”样本。
- 若 CLI schema/契约变更，更新对应文档与生成物并提供检查证据；具体实现阶段执行，本次文档编写不运行检查。

## 部署前置与退出

- MP-02 及版本/文件/refs/审核模型先就绪，map/receipt 增量 DDL 先部署；CLI 通过部署隔离/最小权限账号与离线授权仅向授权操作者开放，不假定存在未实现的业务开关，不在服务启动或 DDL 中自动执行。**不部署 A29/A30 HTTP，不保留其路由。**
- 首轮仅隔离环境或明确选定试点；真实生产 apply 必须另有批准计划、备份、范围授权、操作者和中止步骤，不能由服务启动或数据库 migration 自动执行。
- 转换代码可独立验收；生产部署门禁、全链路安装比对、市场旧调用链退役与兼容回滚由 [REL-01](08-cutover.md) 放行。保留 schema、映射和历史证据，不回退 embedded/inline，不误删独立非市场共享实现。

## 本轮评审修订验收

- [ ] 实现 02 §5.1 的 authorization 文件和统一验证器；CLI apply/resume 均验证可信主体、签名、公钥启停/撤销、环境、范围、来源、批次、plan_digest 及有效期；手写 approved_by 或附件自带公钥不能授权。
- [ ] 篡改计划/签名、错误主体/环境/范围、过期/撤销均非零退出且无新增业务写入；授权 ID 并发使用只关联同一原 run，不能新建第二个 run。
- [ ] 过期后保留 run/map；续签同计划同批次授权恢复原 run 并记更替审计，不重复目标/版本/审核。换计划必须重新 preview/批准。轮换后旧钥按部署启停/撤销状态处理。
- [ ] Admin 无资产审核步骤，system 迁移仍调用原直接发布领域事务；离线授权仅是迁移计划执行许可。Space 保留自动/人工策略。原 review 取消/拒绝后 run 保持 blocked，不自动换绑页面重提的新 review；需修订计划/授权处理。
- [ ] #128 的锁前文件冻结、readiness 修复保留；指针回滚移除，新增授权/run 关联存储采用增量迁移。

# MP-02：浮动引用、最新发布解析、审核观察与依赖治理（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型、D1（删除并集）、D4（A01 四字段）、D6（private/下架）、D7（Connector effective）、D8（CAS/relations）、审核策略与 C01/C03/C07。本 issue 拥有**唯一的最新发布解析服务**与依赖治理；不得再写第二套“取最新”实现。

## 目标（Goal）

硬依赖：MP-01。合并前需其完整快照、release、Connector effective、CAS 和统一读取可用。本 issue 将原 R1～R4 收成一套端到端领域能力：浮动引用写入、最新解析、权限矩阵、审核观察、删除保护、容器、A01 公共过滤与 mine 诊断、候选与影响。不能先开放引用写入再补权限或删除保护。

范围：octo-marketplace 所有 User/Admin/Bot 写入口、容器、提交/批准、市场版本图、候选及依赖影响。非目标见下。

## 背景（Background）

B 模型下，Expert→Skill、Team→Expert 不再以 `target_version_id` 作为权威 pin：每次公共读取/A34/安装/审核都解析目标资源的当前正式发布版（`plugins.published_version_id`，且 `status=1`、未删除、`listing_state='published'`）。`target_version_id` 仅保留为上次解析/审计值，可空。

- Expert→Skill：目标 Skill 当前正式发布版。
- Team→Expert：成员 Expert 当前正式发布版；该 Expert 的 Skill 也解析最新；Connector 按 ID 读 effective。
- Team 的 `member_key/role/is_leader/sort_order` 仍由 Team 边决定，不随成员 Expert 发新版改变；成员内容/依赖跟随最新。
- 目标没有正式发布版，或当前正式发布版被下架/删除/缺失 → 依赖不可用，**不回退旧版**。

因此本 issue 的 `refs` 是“引用身份 + 审计快照”，不是解析权威；`content_digest` 排除浮动版本。A01、A34、安装、审核观察必须复用同一个解析服务，并在一次数据库一致性快照内解析完整图，保证单次响应或首次安装计划内部不混版；不同时间的列表查询与新安装可解析到不同版本。审核默认自动（现有 `plugin_review_policies`，Space 可关）；自动模式内部重试一次，人工模式窗口变化才 409，批准后依赖继续浮动。

同名/多版本共存本期明确拒绝：同名不同内容安装冲突失败，不生成 `mk1` 别名、不改写 `SKILL.md` frontmatter，不改 Fleet/Daemon。

## 承重行为（Load-bearing）

1. **唯一解析服务。** 新建 `LatestPublishedResolver`（名称实现时确认），输入根 `plugin_id` + `view`，输出 `root/graph/assets 引用清单` 所需的节点身份：版本节点 `plugin_id@version_id`、Connector 节点 `plugin_id@effective_revision`。A01 公共过滤、A03 图、A34、安装计划、审核观察全部调用它；任何调用点不得自行按 `current`、`target_version_id` 或“最近发布时间”拼图。
2. **一次请求一个一致性快照，不回退。** 同一请求在同一只读事务/快照内选定根、全部目标最新发布版和 Connector effective，并复制 effective 内容/修订/摘要；退出事务后只读不可变文件。任一节点缺失/无发布/下架/删除/无权/refs 不一致 → 整图不可用；不静默用旧版本、不裁剪、不分页补 latest。“最新”是模型，不是失败回退。
3. **引用写入。** `plugin_relations` 以目标 `plugin_id`（及 Connector ID）为权威；`target_version_id` 可空、仅审计；Expert/Team 的 Skill/Expert 边省略该值也合法。Connector 边必须完全省略版本字段（null/空字符串也拒绝），禁止客户端提交 `observed_*`。完整 Write 的 `relations` 必填数组；缺失/null 400，显式 `[]` 才清空；`relation_id` 只能更新本来源既有边。
4. **Team 语义。** 成员集合、`member_key`、角色、Leader、顺序由 Team 边数据决定；成员 Expert 内容跟随其最新发布版；成员唯一性按 `member_key` + 目标 ID 校验；并按本次解析后的成员名称校验唯一（Fleet Definition `experts.name` 唯一），冲突 400 `team_member_name_conflict`，不按历史固定名判定、不自动改名。每 Expert 最多 20 Skill、20 Connector；Team 1～30、唯一 `member_key`/目标、恰一个 Leader；非 Leader 的 `trim(role)=leader` 拒绝。
5. **审核观察。** 提交时解析并记录所有浮动依赖的本次版本（Expert、Skill、Connector `effective_revision/digest`）；批准时重新解析。自动模式：解析/批准间变化则重试一次，正常不把 409 暴露给用户；人工模式：变化 409 `dependency_changed`，由有权取消者取消旧申请，再由有权提交者重新提交并生成新的 review_id；审核人查看新申请后批准。批准后依赖继续浮动；Skill/Expert 发新版**不需要**重新审核引用它的 Expert/Team，只有 Expert/Team 自身内容/依赖集合变化才需要新版本/审核。审核不能冻结批准后的依赖内容。
6. **A01 公共过滤（D4/B）。** 公共列表默认 `view=published`：Expert/Team 的解析图任一节点不可用 → 来源不进入公共列表，**不修改来源 `listing_state`**。过滤在 SQL 的 COUNT/排序/分页之前完成；分类 `plugin_count`、公共 tags 同口径；一次一致性快照。前端/客户端不做二次过滤或逐卡 A34。
7. **A01 mine 诊断（D4）。** `mode=mine` 保留原 owner/Space 内全部非删除资产与 total、全部 listing 状态；仅 Expert/Team 必返 `dependency_availability={status,evaluated_view,evaluated_version_id,reasons}`：
   - `evaluated_view/evaluated_version_id` 指来源自己的 published/editing（有 published 诊断 published，未发布才诊断 current/editing）。
   - `reasons[].code` 固定 `dependency_delisted|dependency_deleted|dependency_unavailable`。
   - `target.version_id` 是本次解析到的最新目标版本；Team 的 `via` 是根与目标之间的最新成员 Expert（含版本）。
   - 目标及整条 via 逐项有权才给具体身份；否则合并一条匿名 `dependency_unavailable`，不带 ID/名称/类型/版本/数量。
   - 公共行和其他类型省略该字段；正常 `available` 且 `reasons=[]`，异常至少一条。应有版本/refs 缺失与 DB 故障按完整性/服务错误，不伪造 available。
8. **D1 删除保护。** 只按并集判定：live incoming（active 来源、来源/关系未删除、不过滤 listing、含草稿）∪ published refs（来源已发布，按 `target_plugin_id`）∪ Team→最新发布 Expert→Skill/Connector 间接引用（published Team 的成员 Expert ID 的发布依赖）。**没有“曾发布即禁止删除”总禁令。** 只改删除，不改已发布资源收窄 visibility 的现有行为；引用式来源删除只删自身及自身关系，不删共享目标。
9. **D6 依赖矩阵。** system → 已发布 system；space → 已发布 system 或同 Space 的 space；**同 owner private → private 允许**；space/system 不得引用 private；跨 Space 禁止；Admin 可见不绕过。最新解析依赖下架/删除 → 来源公共隐藏且 A34/安装失败；owner 只能看 mine 诊断，不能安装坏图。
10. **D7 Connector。** 审核冻结全部 Connector 的 `effective_revision/digest`；批准期间变化 409 `dependency_changed`；只改 editing 不误判；批准后新安装读新 effective，已安装实例不受影响。
11. **D8 写入。** 所有写入口（Tenant/Admin/Bot/容器/导入）统一 `edit_revision` CAS：更新必带 `expected_edit_revision`，新建省略；缺失/过期 409 `edit_conflict`；`relations` 必填数组。四端同批改。
12. **refs 完整性。** 同一快照内校验 `relations_json` 与 `plugin_version_relation_refs` 双向一致（`source_plugin_id`、数量、relation_id/type/target ID/审计版本）；合法 `[]` + 零 refs 是“无依赖”；缺 Version、非数组、缺/多行、错来源/目标均 500 `relation_index_inconsistent`。目标 Plugin 缺失但快照/refs 一致是“正常不可用”，不得 INNER JOIN 吞边。

## 非目标（Out of scope）

新增生命周期操作、客户端选择器/编辑器、A34 HTTP 与安装执行（MP-03）、旧包自动转换（MP-04）、A09 restore、A29/A30 HTTP、同名/多版本共存、`mk1` 别名、frontmatter 改写、Fleet/Daemon 改造。CL-01/CL-02 可依据本 issue 接口先编码，真实 API 验收在本 issue 合并后并行。

## 源码与数据所有权

- `internal/model/plugin.go`；`internal/repository/plugin/{write,read,history,tags,review,listing,graph_review}.go`。
- `internal/service/plugin/{service,validation,admin,container,listing,review,review_policy,scenes}.go`，新集中 `DependencyPolicy`、`LatestPublishedResolver` 与图读模型。
- `internal/api/handler/plugin/{handler,admin,review,listing}.go` 及现有 placement/visibility mutation 入口；SQL 迁移和 OpenAPI 生成物。现有容器入口是 `AdminImportContainer/AdminReuploadContainer`→`ImportContainer/ReuploadContainer`；不能只修改 wrapper 漏掉实际 builder 与 `RebuildGraph`。
- 唯一创建 `plugin_relations.target_version_id`（可空）、`plugin_version_relation_refs` 及目标索引；review Connector observations 字段；Admin 导入 `plugin_mutation_receipts`（供 MP-04 复用，同表不得重复创建）。原表 owner 不变，不新增分发状态列。复用 MP-01 的 codec/完整文件读取/冻结内容及唯一 release/effective 事务；MP-03 唯一拥有 A34 HTTP 与完整内容组装，本 issue 只交付无 Runtime/Workspace/Secret 依赖的解析器与引用清单。
- A01 唯一 owner 保留 MP-01；本 issue 的 `extends_api_ids` 增加 A01，负责公共列表/分类/tags 共享 metadata predicate、mine Expert/Team 诊断计算及 reason 授权。复用 MP-01 DTO/list 底座，不新建列表接口或可用性状态表，不改 8 issue 硬依赖。**A34 的 HTTP/DTO 仍归 MP-03，本 issue 只提供解析语义，不能实现第二个 A34。**

## 实现步骤

1. 扩展 Host 关系 DTO、Repository 所有 SQL 列/scan 和统一 snake_case codec；Expert→Skill/Team→Expert 以目标 ID 为权威，`target_version_id` 可空且仅审计；Connector 字段必须完全省略（null/空字符串也拒绝），禁止客户端 `observed_*`。`source_plugin_id` 如有必须等于来源；`relation_id` 只能更新本来源既有边，跨来源/重复 ID 拒绝。固定关系所属/release、唯一 type+target 纳入写入/对比，`target_version_id` 变化不单独产生业务版本；不能漏 `syncRelations`/`graphEdgeColumns`/review/fixture 等直接入口；不通过 Lib 公共 Relation schema 扩大字段。
2. 先完成关系 ID 分配/reconcile，再以同一 canonical 最终关系列表同事务写快照与 refs（PK `source_version_id+relation_id`），核对投影，不另行以未赋 ID 的请求数组写索引。refs 无独立业务修改入口；只允许从可证明快照确定性重建；**不给旧缺版本数据补 latest、不改历史快照**。发布内容摘要包含关系身份/角色/Leader/顺序，不含浮动目标版本；Connector 观察值只在 review。
3. 实现结构 validator 和锁内 `DependencyPolicy`：standalone/release/现有状态与访问检查/来源受众/跨 Space；Team 递归通过 `LatestPublishedResolver` 验证成员 Expert 最新版及其 Skill/Connector，不读 `target_version_id`/current 替代。每 Expert 最多 20 Skill、20 Connector；Team 1～30、唯一 `member_key`/目标、恰一个 Leader；data 仅允许 `member_key/role/is_leader` 且三项必填，其他关系 data 只能 `{}`，`sort_order` 非负。`member_key` 按权威正则/长度，role 非空且最多 500 字，非 Leader 的 `trim(role)=leader` 拒绝。既有发布、审核、下架、删除检查保持，只加入浮动引用与完整依赖检查。
4. 各写入口、Admin、容器、自动/人工审批和审核卡片动作接同一 `DependencyPolicy`：system 仅依赖已发布 system，space 仅已发布 system/同 Space 的 space，**同 owner private→private 允许，space/system 来源不得引用 private**；递归禁止跨 Space。历史 Version 受众取目标当前有效范围 ∩ 版本 `declared_visibility`；历史受众不明不按当前公开推断。保留原角色/owner 提交、`isReviewer`、Space 自动审核开关与管理权限，Admin 可见不能绕过矩阵。采用 review→全部 Plugin 按 ID（含来源/间接目标）→Version 按 ID→effective/placement/refs 锁序；Delete/Delist 取消 pending 也同序。预读全锁集后锁内重读版本/关系/有效状态，**重新解析最新图**，变化重算，死锁最多 3 次后 409；无 OSS/Fleet 长事务。
5. 容器 schema2 按 [API 契约 §4](../02-api-contracts.md#4-引用式容器与导入幂等) 完整字段表，只收来源定义、市场 id/version 与 wiring，ZIP 为最终内容/关系。`file/container_schema_version="2"` 必填，version/category_id 可选，重传 `expected_edit_revision` 必填；唯一 `expert.json` 或 `squad.json`，Expert skills/connectors 显式数组、Team members 1～30。summary/name/tags/instruction 等允许字段、未知/旧团字段处置按权威 schema，严格解码，不沿用 `json.Unmarshal` 静默忽略；Team 自身 instruction 必须参与新 AGENTS 渲染。创建省略 version 默认 1.0.0，重传省略沿用 current 候选；有变化且原动作直接生效时若复用正式标签则 409 `version_label_conflict`，要求显式更高标签、不自动 bump。保留路径安全、元数据保留；删除新建 embedded Skill/member 和 inline MCP builder。重传全量关系仅改来源，不遍历旧图删除独立目标；按 type+target 保留 relation_id，目标写最新发布不写死 pin。保留独立 Skill 自身上传。A24 对 Expert/Team 拒绝，不接受页面内容/关系/元数据覆盖、不另设 metadata PATCH；Tenant A12 仍完整 Write+relations。
6. 导入/重传按 actor+operation+target（新建固定 new）+key 唯一抢占，receipt 与结果同数据事务提交；输入摘要覆盖原 ZIP、schema/version/category/CAS，不按文件名。抢占失败回读已提交 receipt，同 key 同输入回放原 Detail+`resolved`，不重新解析或再次建 Version；不同输入 409 `idempotency_input_mismatch`。7 天提供完整结果、之后保留 tombstone 并拒绝复用。ZIP/对象先校验上传再入事务，失败清理沿用 MP-01 跨资源引用保留规则。`resolved[]` 只读回显首次导入解析到的目标 ID/版本（同 key 回放不重算），不是权威 pin。
7. A14/A13 提交审核只接收指定 `version_id`（Connector 使用 `expected_edit_revision`），加载 MP-01 不可变快照，不再接收游离 Manifest/Package/relations/parse_task。提交时用 `LatestPublishedResolver` 解析完整最新图，**递归记录全部依赖的本次版本**：Expert、Skill、Connector `effective_revision/digest`；批准时在同一锁序内重新解析，任一有效观察变化：自动模式重试一次，人工模式 409 `dependency_changed`；单纯 editing 改变不误判。审批、拒绝、取消 CAS pending，自动审批和卡片动作同约束；批准只切 published/有效受众/listing 或 Connector effective，不回写 current/live、不重新 snapshot。重复批准同请求返回原结果不重激活旧发布。
   - A15 冻结详情严格用 API §2.3 的 `frozen_version/frozen_connector`、`readme_content/frozen_relations`、`source_edit_revision/frozen_content_digest`、`connector_observations` 及 `baseline_version_id/dependency_diff`；本 issue 创建 `baseline_version_id/dependency_diff_json`，按提交时解析结果计算冻结差异，列表不逐行加载正文。Connector 自身有效基线（首次可 null）也观察，防旧审核覆盖后来有效配置；解析失败不吞空，拒绝/取消仍沿原权限可用。
8. 保留现有 live incoming 删除保护并补并集：active source、`r.status=1` 且来源/关系均未删除，无 listing 过滤，草稿 live 引用也保护；再从**未删除来源的 `published_version_id`** 进入 refs（按 `target_plugin_id`，不按 audit version），展开 published Team→成员 Expert→该 Expert 发布依赖的Team→最新发布 Expert→Skill/Connector 间接引用。**不采用“曾发布即禁止删除”**：没有 live 引用、没有 published 来源引用、也没有Team→最新发布 Expert→Skill/Connector 间接引用的资产可以删除，即使它自己曾发布。不能以 published 替代 live 保护；Team 间接路径读取成员当前正式发布版，历史审计 pin 不参与判定。删除/缩权接现有访问检查；新引用式 Expert/Team 删除仅删除自身及自身关系，不删除共享 Skill/Expert；既有下架及 pending 取消行为保持，不给共享独立目标级联下架/改范围。旧 embedded 历史仅显式迁移，不全局清理。
9. 实现 [API 契约 §2.4](../02-api-contracts.md#24-graph--candidate--dependents) 完整 Graph：版本节点 `plugin_id@version_id`、Connector 节点 `plugin_id@effective_revision`；边带 source/target 资源 ID，目标版本为**本次解析身份**，Connector 带 `resolved_effective_revision`。根与每个 Connector 的有效行必须在同一一致性读内取得；后续组装不能按 ID 再取新 effective 行造成节点/配置混版。第二跳不读 Expert `current`；缺依赖/无权/索引异常整体失败，不沿用 `GetGraphClosure` 静默裁剪或按 `plugin_id` 合并版本。A03 只返文件清单不含正文；Team 计数和列表派生信息从本次解析快照取，resolver 不命名改写、不调用 Fleet。
   - 新图计数统一按 API §2.4：含根 1500 节点、2000 边，版本节点不能按 `plugin_id` 合并。调整现有 `repo.go` 的 630 子节点限制，使合法 Team→最新 Expert→Skill/Connector 完整图不被旧上限误截；读取即计数，超限整体 413，不裁剪。MP-03 同用该解析底座，Fleet 限制不改。
10. 完成 A03/A10/A11、A14～A19、A25/A27，接入 A12/A13/A24 依赖规则及既有 Tenant `POST /plugins/delete`、Admin DELETE、placement/visibility 入口。Dependents 标准 data 数组+pagination+`meta.has_hidden_dependents`，覆盖实际删除保护并集，隐藏路径/ID 不入 data；候选独立于场景列表、对无权目标不返回，有权不合格才给安全 `can_select=false` 原因。A25 Detail+`resolved[]` 逐条返回本次解析到的直接目标版本，字段与 Connector 省略规则以权威 DTO 为准，不能返回猜测解析结果。保留非默认投放可见性，default 不能隐藏。
11. 沿用 Admin 现有写入、发布/审核与上下架动作，不新增重新上架按钮或接口；放宽来源受众使用快照 `declared_visibility`，批准前线上 visibility 不扩大，批准时重验完整最新图及 inbound 影响。
12. 按 [技术方案 §6.1](../01-technical-design.md#61-已确认公共列表的依赖可用性与-sql-分页) 实现共享只读 metadata predicate：从来源发布根出发，**每次按 `LatestPublishedResolver` 解析目标最新发布版**；Team 沿最新成员 Expert 再判 Skill/Connector；直接或间接目标下架、删除、缺失时公共来源不展示，不读 `current`/`target_version_id`/旧版本替代。将条件接入 `read.go::buildListQuery/List` 共享 from/where，COUNT 与排序/分页前过滤；`history.go::ListPlacementCategories` 的 `plugin_count`、`tags.go::ListTags` 公共集合/统计复用同条件。不得分页后丢卡、逐卡 A34 或文件校验；不因读列表修改来源 `listing_state`、审核或投放。
13. `mode=mine` 保留原 owner/Space/非删除管理范围、全部既有 listing 状态及 total，Admin 管理列表也保留异常来源。仅 mine Expert/Team 必返 [§6.2 DTO](../01-technical-design.md#62-已确认我的发布诊断-dto-与原因安全边界)：已有 published 就诊断 published，即使行展示 editing；仅未曾发布才诊断 current/editing。available 的 reasons 必为空，unavailable 至少一条；正常无依赖图 available，应有版本/refs 缺失与 DB 故障返回既有完整性/服务错误，不虚构空图/available。
14. reason 仅使用 `dependency_delisted/dependency_deleted/dependency_unavailable`；目标和整条 via 路径逐项授权后才给具体 code/target/via，固定节点给 version_id、Connector 不给。直接依赖无 via，间接 via 只含按根至目标排序的中间节点。无权、缺失不可确认或已删元数据无权读时，所有不可披露原因只聚合一条匿名 `{code:'dependency_unavailable'}`，不带 ID/名称/类型/版本/via，不泄漏数量及下架/删除差别，不借来源快照名/缓存绕过权限。服务端不返回整句或 HTML，不要求客户端扇出补原因。
15. 来源只改草稿不影响公共发布图或线上诊断；移除/替换坏依赖的新版本正式发布后、或目标恢复合法上架后，下一次列表查询**重新解析最新图**并恢复合格来源。沿用现有刷新，不新增持久化可用性、推送或轮询；列表 metadata 合格不替代 A34 文件读取或安装最终校验。
16. 同一 DB 一致性快照内、在依赖失效隐藏及分页前校验根/Team 最新成员 Expert 的 `relations_json` 与 refs：`refs.source_plugin_id` 必须等于 `Version.plugin_id`，JSON 数组长度与该 `source_version_id` 的 refs 数量对应，再将 `JSON_TABLE` 投影的 relation_id/type/target ID/审计版本与 refs 双向比较，不能只比数量。合法 `[]` 且零 refs 是无依赖；缺 Version、非数组、缺/多 refs 或等数量错来源/目标均 500 `relation_index_inconsistent`。目标 Plugin 缺失而快照与 refs 一致是正常不可用，不得 INNER JOIN 吞边。共享完整性检查适用于公共 COUNT/list/facets 和 mine 诊断，不读取文件、不新增索引状态表。

## 接口与错误验收口径

- 输入输出、包络及错误仅来自 [API 契约 §2～§4、§8](../02-api-contracts.md)；MP-01 的 A01 底座是唯一接入点，MP-02 只提供共享解析/依赖判定/完整性检查/诊断，不建立第二套 wire 或列表。
- **B 下不再要求 Expert/Team 关系必带版本**：省略 `target_version_id` 合法；显式携带时必须属于该目标资源，否则 400 `dependency_version_mismatch`；不再返回 `dependency_version_required`。Connector 携版本（含 null/空）400 `connector_version_forbidden`、embedded 400 `dependency_embedded`、非法 wiring 400 `team_wiring_invalid`、成员 `member_key`/目标冲突 400 `team_member_name_conflict`；有权但 scope 不兼容 400 `dependency_scope_mismatch`，无权/不存在 404 `dependency_unavailable`/`version_not_found`，不暴露目标存在性。未 release/下架/观察变化/被引用/CAS 冲突 409；坏 refs 为 500 `relation_index_inconsistent`。均使用固定顶层 code，文件完整性由 MP-01 底座及 MP-03 消费，不吞成正常诊断。
- A03 的 `view=editing` 与 `version_id` 互斥，默认 published，Connector 默认 effective；精确历史根须有权且曾 release，不按 latest 补。A10 新建必 source_type/source_visibility，existing source 需验证写权限及拟定受众；A11 仅 owner/有权治理者，A27 既有 Admin 鉴权。A14 返回 201 ReviewDetail，A16 返回 PublishResult，A17 返回空 data；A13 沿用 200 而非 202，Space 自动/人工状态由结果字段区分。
- **本 issue 不新增 A29/A30、不新增 A09**：生产迁移走 MP-04 CLI，恢复能力不做。

## 验收（Acceptance）

- [ ] 全写入口非法目标 ID/错归属/未正式发布/private 越权/跨 Space/embedded 一致拒绝，错误不暴露无权目标存在性；**省略 `target_version_id` 合法，显式错误版本 400**。
- [ ] system→已发布 system、space→已发布 system/同 Space 的 space 成功；**同 owner private→private 成功**；space/system→private、跨 Space 拒绝，Admin 也不绕过。市场发布拒绝本地路径/Fleet skill_id 充当依赖，不为本地/工作空间组合创建 private 市场 Skill；非默认场景可见性与 default 不可隐藏约束保持。
- [ ] 既有 Space 自动审核开启时仍自动批准，关闭时仍由既有审核者审批；普通成员不能修改审核策略，owner 提交和原审核权限不变。自动/人工批准都拒绝非法依赖，不产生新的免校验通道；自动模式并发依赖变化内部重试一次成功，人工模式变化 409 `dependency_changed`。
- [ ] Team 唯一 Leader/`member_key`/目标，数量 1～30；Expert 各最多 20 Skill/Connector；非法 wiring 明确失败。
- [ ] **B 跟随：** Skill S 发布 v2 后，引用它的 Expert E（其自身未发新版）在下一次读取/A01 解析到 S v2；成员 Expert 发新版后 Team 下一次读取展示新内容，但 Team 的 `member_key/role/is_leader/sort_order` 不变。
- [ ] **B 不回退：** 最新版 S v2 下架/删除时 E 依赖不可用、公共隐藏；不得改读 S v1；S 发布 v3 恢复后下一次查询恢复。
- [ ] E 发布 e1→S，保存 current e2 删 S 但未发布：删除/缩权 S 仍拒绝，影响指向 E/e1；仅在草稿 live 中新增的引用也继续阻止删除，不引入草稿豁免。
- [ ] Team 发布 t1 引用 Expert e，Expert 后来发布 e2 去掉旧 Skill：t1 的边仍指向同一 Expert ID 并解析 e2；t1→e 的已发布/间接依赖仍保护相关目标；**不再把 t1 固定到 e1 作为判定依据**。
- [ ] **无“曾发布即禁止删除”：** 一个只有历史发布、当前无 live 引用、无 published 来源引用、无Team→最新发布 Expert→Skill/Connector 间接引用的 Skill/Expert 可以删除；有任一保护引用时拒绝，错误/影响指向真实来源。
- [ ] 同一 Skill 被多个成员引用时，在同一请求内解析到同一个正式版本并去重为一个节点，保留各来源边；不同 Skill ID 即使同名也不能合并。详情/列表成员数不随未发布草稿漂移。
- [ ] Connector 审核期间有效修订变更使人工批准失败、自动重试收敛；纯 editing 变更不误判；审批后新 effective 不创建 Expert 新版本。
- [ ] 两容器共享 Expert/Skill，重传或删除一个来源不删除共享目标；旧 ZIP 返回 `legacy_container_format`，不自动猜转换。
- [ ] 两并发下架/批准、删除/加引用、visibility 扩大/依赖缩权均只有满足锁内不变量的一方成功；锁内重新解析，不存在验证提交间隙。
- [ ] 无权限的 dependents 不暴露隐藏路径/ID，只返 `has_hidden_dependents`；401/403 与枚举规避符合契约。
- [ ] 分别使 Skill/Connector 下架、删除或缺失：相关 Expert 从公共 Web/Buddy A01 结果消失；使成员 Expert 下架、删除或缺失时 Team 同样消失。删除/缺失使用合法可构造的历史异常数据验证，不放松现有删除保护以制造场景；来源 `listing_state`、审核与操作不变。
- [ ] 公共 COUNT、跨页结果、分类 `plugin_count` 及 tags 都使用同一 SQL 前置谓词，无异常来源占位/分页后裁剪；无 A34/OSS/前端逐依赖调用。无依赖合法 Expert 返回 available，必需版本/refs 缺失和 DB 故障走原错误，不假成功。
- [ ] 同一坏依赖来源仍在 mine 与 Admin 原有非删除管理范围；mine total 不减，全部 listing 状态/审核标记/操作保留，未引入本人已删除行。mine Expert/Team 必返四字段诊断，公共和其他类型不带该字段。
- [ ] 已发布 e1 异常、current e2 已修复但未发布：mine 仍诊断 e1/published，公共仍隐藏；e2 合法发布后下一次查询恢复。仅未曾发布的 current 草稿异常为 editing/current，提示草稿异常而非已从市场隐藏。
- [ ] 依赖合法恢复上架后下一次查询重算；仅未批准编辑不改变有效状态；恢复不写来源 `listing_state`、不新增状态/接口/推送/轮询，已有 A34/安装检查不被列表结果绕过。
- [ ] 有权直接原因带 target 且无 via；间接 Team→Expert→Skill/Connector 原因保留本次解析版本并只以成员 Expert 作 via。目标或任一中间节点无权时只返一条匿名 unavailable；多个不可见异常不暴露数量，已删元数据无权及缺失不可确认不泄漏 ID/名称/类型/版本或删除/下架细节，旧快照/缓存名也不可补出。
- [ ] 全量关系 round-trip 保留准确目标 ID、`relation_id` 与可选审计版本；Connector 省略版本；Connector 传 null/`""`、客户端 `observed_*`、未知 data、错误 `source_plugin_id`、跨来源/重复 `relation_id` 均拒绝。同 type+target 重复目标不允许用不同 `relation_id` 绕过。
- [ ] Team 重复目标/`member_key`、本次解析后成员名称重复、错误 `member_key` 正则/长度、空或超长 role、非 Leader `role=leader`、负 `sort_order`、双 Leader/无 Leader 逐项 400；成员 Expert 改名后按本次解析名展示与判重，不因历史固定名漂移报错。
- [ ] private 历史 Version 在目标后来发布为 system/space 后仍不能被新受众读取、选择或引用；Team 递归相同；目标当前缩权或历史受众不可证明时不自动按新公开范围放行，无 ID 枚举泄漏。
- [ ] A25 同 key 同 ZIP/字段/CAS 重试回放同插件/版本/关系及 `resolved[]`；同 key 换 ZIP/version/category/CAS 409，不同 key 并发受 CAS 保护。提交前失败无 receipt/半成资源，提交后回包丢失仍可回放，7 天后 key tombstone 拒绝复用；不删除任意历史对象。
- [ ] Admin A24 Expert/Team 创建/更新均拒绝，不能通过页面 Write 或 metadata PATCH 覆盖 ZIP；A25 创建省 version 得 1.0.0，已正式 1.0.0 后内容/依赖变更但重传省 version 或复用 1.0.0 则 409，显式更高标签成功，未变全量重传不自动升版。tenant A12 引用式 Write 仍可保存草稿。
- [ ] 同一请求读 Connector 有效行后并发生效新修订，A03/解析器返回的 `node_key/effective_revision/digest` 及内容始终同一修订；纯 editing 不改变节点身份。此底座由 MP-03 复用于 A34，不新增 Connector 历史 Version 或长期缓存授权。
- [ ] refs 的少行、多行、等数量错 `source_plugin_id`/target/审计版本、非数组快照或缺必需 Version 在公共过滤前及 mine 均 500 `relation_index_inconsistent`；合法 `[]`+零 refs available；refs 完整但目标 Plugin 不存在时公共隐藏、mine 仅安全匿名 unavailable。COUNT/list/分类/tags 同一一致性快照，无 INNER JOIN 吞坏边。
- [ ] 已发布来源保存新 editing 后再批准旧冻结快照，current 内容/文件/关系/`edit_revision` 不被回写或回退；重复批准旧请求不覆盖更晚发布版，拒绝/取消/下架取消 pending 与批准竞争至多一个决定。review→全部 Plugin 排序锁序覆盖原 DeleteGraph/AdminDelete/Delist 入口，无反向锁序旁路。
- [ ] 同名/多版本共存不在本 issue 通过任何别名、改名或 frontmatter 重写实现；同名不同内容由安装阶段（MP-03）明确冲突失败。

## 验证与交付（Verification）

实施阶段运行 API：创建独立 Skill/Connector→Expert→Team→提交审核→Skill/Connector 发新版或变更 effective→观察自动跟随/人工 409→保存新草稿移除旧依赖→尝试删除旧依赖并观察拒绝。另覆盖草稿 live 引用保护、published 间接引用、依赖下架隐藏与无旧版回退、A01 四字段/匿名、既有上下架检查及多版本图 smoke；不开发新治理动作。本次文档修改不运行验证。

隔离 MySQL 验证 refs 同事务/重建、CAS 与锁竞争；检查对象及 Secret 不进入错误。相关包/全量测试与 OpenAPI check/diff 只在完整改动后执行。提交所有 SQL/DTO/handler 调用点迁移，不留下旧 builder 或权限旁路。MP-03 与 MP-04 从本 issue 稳定边界并行，不彼此重建 policy 或解析器。

### 依赖更新审计（本轮用户确认）

依赖发布变更保留审计证据，读取结果展示本次解析版本；本期不因依赖更新主动发送 owner 通知，不新增通知发送任务。沿用原审核通知等其他事件，不扩大本决定范围。

## 本轮评审修订验收

- [ ] 人工提交依赖 v1，目标更新 v2，批准返回 409 且原 pending/观察值不变；GET 刷新不改变结果。按原权限取消后重提同一来源版本得到新 review_id/新观察值，重新审阅可批准；取消与批准竞争不产生双重发布。
- [ ] Connector 重提用当前 expected_edit_revision 并重新展示内容；旧冻结记录不可改写。无权取消/提交者不能借恢复流程越权；Admin 直接生效不进入此流程。
- [ ] 专家历史 v1 引用 A，当前发布 v2 和 live 草稿均已移除 A，其他保护并集为空时允许删除 A；保留历史证据。当前发布或 live 草稿仍引用 A 则拒绝，不扫描全部历史扩张保护。
- [ ] 单次解析一致；两个独立请求跨发布可观察不同最新版，无跨请求快照承诺。依赖更新仅审计展示，无主动通知。

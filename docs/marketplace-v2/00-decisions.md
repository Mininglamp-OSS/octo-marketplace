# 规则裁定与最终决策（2026-10-26 · B 模型）

状态：**最终决策已锁定**。用户在本次对话中再次明确确认 D1 的“曾发布但无保护引用可删除”及 B 模型的“Team→Expert 也跟随当前正式发布版”。本文替代此前关于固定版本 pin、A09 恢复、A29/A30 HTTP 迁移、private 一律禁止、曾发布禁止删除、同名共存等的讨论。凡旧段落与本文冲突，以本文为准。

## 0. 决策清单

| ID | 决策 |
|---|---|
| D1 | 保留原有删除能力；删除保护 = live incoming（含草稿）∪ published refs ∪ Team→Expert→Skill 间接引用；没有保护引用的曾发布资源也可删。只改删除，不改“已发布资源收窄 visibility”的现有行为。 |
| D2 | 不做 A09 restore-to-draft；从 MP-01/API/plan/CL-02 删除 A09。 |
| D3 | 不允许同名/多版本共存；同名冲突失败；普通安装保留市场原名/原字节；不改 Fleet/Daemon。 |
| D4 | A01 `dependency_availability` 四字段；`evaluated_view/evaluated_version_id` 指 Expert/Team 自己的 published/editing；`target.version_id` 用最新解析值；无权依赖匿名。 |
| D5 | 生产迁移用 CLI + 离线授权；不做 A29/A30 HTTP；CLI 两次 preview/冻结摘要；回滚必须数据安全。 |
| D6 | 同 owner private→private 允许；space/system 不得引用 private；最新依赖下架/删除 → Expert/Team 公共隐藏且 A34/安装失败；owner 只能看诊断，不能安装坏图。 |
| D7 | Admin 不用审核，写入直接生效并同步 effective；Tenant 沿用既有审核策略；effective 模型；审核观察 revision/digest；变化 409 `dependency_changed`；安装/A34 读 effective。 |
| D8 | `edit_revision` + `expected_edit_revision`；更新必带、新建省略；缺失/过期 409；relations 缺失 400；Admin/Web/CLI/Bot 都改。 |
| D9 | 保留 A32 URL；A31/A32 同一引擎；同 key 同输入重放原 plan，上游正常发新版不阻断；显式安装输入变化 409，使用新 key 新安装；仅首次成功计数。 |
| D10 | 新改造完成后旧市场执行器不可达；回滚接受兼容二进制 + 新版本恢复；不恢复指针/legacy。 |
| B | **Expert→Skill、Team→Expert 都解析目标当前正式发布版**；Connector 按 ID + effective；Team 的 `member_key/role/is_leader/sort_order` 仍由 Team 边决定。 |
| 审核 | 沿用现有 Space 策略：默认自动，Space 可关；自动模式内部重试；人工模式窗口变化才 409；批准后依赖继续浮动。 |

## 1. B 引用模型

### 1.1 定义

- **最新目标版本** = 目标资源的 `plugins.published_version_id`，且 `status=1`、`deleted_at IS NULL`、`listing_state='published'`。
- 目标没有正式发布版、或当前正式发布版被下架/删除 → 依赖不可用；**不回退旧版本**。
- **Expert→Skill**：每次读取/安装解析目标 Skill 的当前正式发布版。
- **Team→Expert**：每次读取/安装解析成员 Expert 的当前正式发布版；该 Expert 的 Skill 也解析最新。
- **Expert→Connector**：按 ID 读 `plugin_connector_effective` 的当前有效修订（D7），Connector 无版本 pin。
- **Team 边**：`member_key/role/is_leader/sort_order` 由 Team 快照/边决定，不随成员 Expert 发新版改变；成员 Expert 只更新内容和它引用的 Skill。
- `target_version_id` 可保留为**上次解析/审计值**，但不是公共读取、A34、安装、审核的权威依据；客户端不能靠它固定依赖。

### 1.2 解析与一致性

- A01、A34、安装、审核观察必须复用同一个“最新发布解析”服务，不能各写一套。
- 一次请求在同一个数据库一致性快照内解析完整最新图，保证单次响应或首次安装计划内部不混版；不同时间的列表查询与新安装可解析到不同版本。
- Team 递归：Team → 最新成员 Expert → 该 Expert 的最新 Skill/Connector；任一节点不可用，整图失败/隐藏。
- 图/文件预算仍按 API §2.4/§6.1：1500 节点、2000 边、16MiB 等；按解析后的资源+版本计数。

### 1.3 审核

- 默认自动审核：沿用现有 `plugin_review_policies`（没有 Space 配置时 `IsAutoApproveEnabled=true`）；Space owner/admin 可关闭。
- 提交时解析并记录所有跟随依赖的当前版本（Expert、Skill、Connector effective）；批准时重新解析。
- 自动模式：提交→解析→批准；若解析/批准间有并发变化，重新解析并重试一次；正常不把 409 暴露给用户。
- 人工模式（Space 显式关闭自动审核）：批准时变化返回 409 `dependency_changed`；由有权取消者取消旧申请，再由有权提交者重新提交并生成新的 review_id；审核人查看新申请后批准。
- 批准后依赖继续跟随最新；**Skill/Expert 发新版不需要重新审核引用它的 Expert/Team**。只有 Expert/Team 自身内容/依赖集合变化才需要新版本/审核。

### 1.4 代价（必须写进 API/客户端）

- 同一个 Expert/Team 版本，不同时间读取/安装可能拿到不同依赖内容；A34 `bundle_digest` 动态变化。
- 审核不能冻结批准后的依赖内容，只能保护审核窗口。
- Skill/Expert 发新版可能影响引用方；用户已确认本期仅记录审计并展示当前解析版本，不因依赖更新主动发送通知。
- 安装可复现性只保证“同 key 重试首次冻结 plan”；新 key 新安装按最新解析。

## 2. 裁定原则

1. 保留独立市场资产、完整审核、可验证安装、显式历史迁移；但依赖版本按 B 浮动。
2. 不新增生命周期治理、不新增审核角色、不新增 Fleet 协议；沿用现有上下架/删除/投放/审核策略。
3. 技术选择在本文作出；字段、算法、错误包络不再转交产品决定。
4. 没有“失败后补 latest、切 legacy、删边、跳文件、空配置”的隐藏分支；B 的“最新”是唯一显式规则，不是失败回退。
5. 生产迁移仍需人工批准计划；CLI 不是绕过授权。

## 3. 冲突与裁定

| ID | 主题 | 最终裁定 |
|---|---|---|
| C01 | private 目标 | **同 owner private→private 允许；space/system 来源不得引用 private；跨 Space 禁止。** Admin 可见不绕过。私有资产作为依赖时不扩大受众；A34/安装只对同 owner 可见。 |
| C02 | 展示位 | 保留现有非默认场景可见性；default 不能隐藏；候选接口独立于场景列表。 |
| C03 | 生命周期 | 保留现有删除/下架/访问/投放动作。删除保护 = live incoming（active 来源、未删除、不过滤 listing、含草稿）∪ published refs（来源已发布，按 target_plugin_id）∪ Team→Expert→Skill 间接引用。**没有保护引用的曾发布资源可删除**；下架依赖不获得新增安装豁免。 |
| C04 | Connector 版本 | 不新增业务 Version；采用 `plugin_connector_effective`（有效修订/摘要），编辑与 effective 分离；安装/A34 读 effective。**Admin 系统 Connector 的既有直接生效写入保留（同步更新 effective，属既有平台操作）；Tenant Connector 编辑必须审核；Expert/Team 审核观察 effective。** |
| C05 | Connector 审核漂移 | 提审冻结全部 Connector 的 effective_revision/digest；批准时变化 409 `dependency_changed`；只改 editing 不误判。 |
| C06 | Admin 依赖 | Admin Expert/Team 只上传/重传 schema2 ZIP；A24 拒绝这两类 JSON 内容写；A25 强制 Idempotency-Key、CAS、resolved[]。 |
| C07 | 同名/多版本 | **本期不允许共存；同名不同内容安装冲突失败**；不改 `SKILL.md` frontmatter、不生成 `mk1` 别名、不改 Fleet/Daemon。 |
| C08 | Admin 发布矩阵 | 保留现有 AdminCreate/AdminUpdate 行为：创建即发布+default；更新保留 listing/owner/Space/visibility，只接版本/effective 底座；不新增矩阵、不新增重新上架动作。 |

## 4. 原缺口与最终处理

| ID | 主题 | 最终处理 |
|---|---|---|
| A01 | 保存/发布双指针 | 保留 current/published/release；读默认 published，编辑显式 editing；A01 增加 `dependency_availability`（B 语义）。 |
| A02 | 删除保护 | 按 C03 并集；不新增总禁令。 |
| A03 | 图版本身份 | 图节点按 `plugin_id@version_id`（Connector `plugin_id@effective_revision`）；B 下节点版本是“本次解析版本”，边仍带 source/target ID；A03 轻量图，A34 完整内容。 |
| A04 | 安装引擎 | 一个 InstallCoordinator；A31/A32 只做 adapter；删除市场 legacy create/bind/delete 补偿；独立 `/experts`、`/squads` 保留共享代码。 |
| A05 | 安装重试 | 首次冻结无凭据 plan/请求 + HMAC；同 key 只重放原 plan；同 key 显式输入变化返回 409；仅上游正常发新版仍重放原 plan，想使用最新内容则新 key 新安装；状态机/UNKNOWN；仅首次成功计数。 |
| A06 | 迁移 | CLI 生产迁移（D5）：两次 preview/冻结计划、显式标签/映射、可恢复 run、数据安全回滚；不做 A29/A30 HTTP。 |
| A07 | 重新上架 | 撤回；不新增按钮/接口。 |
| A08 | 历史 | 旧快照/字节/记录保留；不伪造 pin/release；历史读取仍按 release/受众。 |
| A09 | restore-to-draft | **删除，不做。** 历史读取 A04–A08 保留。 |

## 5. 生产迁移（CLI）

- 生产路径：`marketplace-stock-migration` CLI + 离线授权；不新增 A29/A30 HTTP。
- 两次只读 preview：
  1. discovery：来源 inventory、候选、关系、缺失选择；
  2. 冻结：显式标签/映射/Connector server 分组/归属，写 `plan_digest`；apply 只认冻结计划，不接受手改摘要。
- 来源身份：数据库 `plugin_id@version_id`；ZIP 原字节 SHA-256（若 CLI 支持 ZIP，必须显式；本期可只支持数据库来源，ZIP 另行授权）。
- create/reuse/update_source：root 用 `update_source` 保持 ID；子节点 create/reuse；不做自动 latest 猜测。
- 标签：preview 可建议 `patch+1`，但必须写入冻结计划并由人批准；apply 不自行换标签；冲突重新 preview。
- Connector：默认保留原 server 集合；只有冻结计划显式拆分才拆，server key 不重复/不丢。
- 审核：CLI 生成的新版本走现有发布/审核（默认自动可自动批准；人工则 waiting_review）；CLI run 状态 `completed|waiting_review|blocked`；不直接插 release 绕过审核。
- 回滚（数据安全）：
  - 不删表/不删文件/不覆盖旧快照；
  - 不把 `current_version_id/published_version_id` 改回旧值；
  - 需要停止新分发时用下架/部署隔离，不用指针回切；
  - 内容恢复从旧历史生成新版本（forward-fix）；
  - 保留 batch/mapping/attempt/audit。
- 独立可见：迁移后的 Skill/Expert 必须在对应市场独立可发现；只有全部节点发布/列表资格就绪才 completed。

## 6. 同名/多版本

- 本期 **拒绝共存**：Expert/Team 同一依赖名不同内容时安装失败；不生成稳定别名、不改写 `SKILL.md`、不改目录名。
- 普通无冲突安装使用市场原名/原字节。
- 若未来要做，需另立决策并补真实 Fleet/Daemon 加载证据；不得以 `mk1` 等未批准算法上线。

## 7. A34 完整内容读取（B 语义）

- `GET /plugins/{plugin_id}/distribution`，只 path ID + 既有认证/Space；无 workspace/runtime/custom_env/幂等键/根版本选择。
- 每次按 B 解析最新完整图：Expert/Team 自身 + 最新成员 Expert + 最新 Skill + 有效 Connector。
- 返回 `DistributionBundle{schema_version,root,graph,assets,bundle_digest}`；每个 node_key 恰一 asset；文件原字节/hash；Connector `files:[]` + 有效公开配置/占位符。
- 同一短只读一致性事务解析根/refs/effective，退出事务后读不可变文件并校验；`no-store`；零 Fleet/零写入。
- 16MiB 完整 JSON、图/文件预算；缺节点/文件/权限/完整性整体失败；不裁剪/分页/补 latest（“最新”是模型，不是失败回退）。
- 不生成 `installation_name`、不改写 frontmatter、不产安装副本；读取成功不代表 Fleet 安装/共存成功。
- CL-02/CL-03 完整内容只消费 A34；A03 只做轻量图展示。

## 8. A01 公共过滤与 mine 诊断（B 语义）

- 公共 A01：Expert/Team 的解析图（最新成员 Expert + 最新 Skill + Connector effective）任一不可用 → 来源不进入公共列表；不修改来源 `listing_state`。
- SQL 在 COUNT/排序/分页前过滤；分类 `plugin_count`、公共 tags 同口径；一次一致性快照。
- mine/Admin 保留原管理范围全部非删除资产与 total；不新增回收站。
- 仅 mine 的 Expert/Team 必返：
  `dependency_availability={status,evaluated_view,evaluated_version_id,reasons}`；
  `evaluated_view/version` 指来源自己的 published/editing（有 published 诊断 published，未发布诊断 current/editing）；
  `reasons[].code` 固定 `dependency_delisted|dependency_deleted|dependency_unavailable`；
  `target.version_id` 是本次解析到的最新版本；Team 的 `via` 是根与目标之间的最新成员 Expert（含版本）。
- 目标及整条 via 逐项有权才给具体身份；否则合并一条匿名 `dependency_unavailable`，不带 ID/名称/类型/版本/数量。
- `view`：mine 仅 editing，公共仅 published，Connector 可 effective；非法组合 400 `details.field=view`。
- 只读，不改 listing/审核/投放；依赖恢复或来源新版本发布后下一次查询重算；不新增推送/轮询/状态列。

## 9. 发布/审核/安装/数据

- 发布审核职责、owner 提交、Space 自动审核默认、人工审核入口沿用；自动/人工都执行同一最新图校验。
- 保存/发布：Skill/Expert/Team 内容、关系集合、标签/受众变化写新版本；`edit_revision` CAS；relations 显式数组。
- Connector：编辑/审核/effective 分离；不新增业务 Version。
- 安装：A31/A32 一个引擎；同 key 原 plan；新 key 最新；Secret 只在内存；仅首次成功计数。
- 数据：`plugin_version_releases`、`plugins.published_version_id`、`plugin_relations`、`plugin_version_relation_refs`（target_version_id 可空/审计）、`plugin_connector_effective`、`plugin_install_attempts`、`plugin_mutation_receipts`、migration batch/map。
- 历史：published 快照不可变；B 下依赖最新不是“改写历史”，而是读取时解析；A34/安装不承诺同一版本永久同一依赖内容。

## 10. 不变核心与非目标

不变：独立资产、完整审核、跨 Space 隔离、不保存运行时 Secret、Marketplace 展开/Fleet 原子安装、历史字节保留、生产迁移另行授权。

非目标：同名共存、A09 恢复、A29/A30 HTTP、新生命周期状态、新审核角色、Fleet/Daemon 改造、自动执行生产迁移。

## 11. 验收新增重点

- **B 解析：** Skill/Expert 发新版后，引用它的 Expert/Team 在下一次读取/安装自动使用新版；不需要发新版本；Team 的角色/Leader 不变。
- **不回退：** 最新版下架/删除时依赖不可用，不静默用旧版。
- **审核：** 自动模式无人工步骤；人工模式审核窗口依赖变化 409；批准后浮动。
- **D4：** mine 的 published/editing 诊断、匿名原因、view 非法 400。
- **删除：** live 草稿引用、published refs、Team 间接引用仍保护；无“曾发布总禁令”。
- **A34：** 单 GET 完整最新图；动态 bundle_digest；no-store；零 Fleet/零写入。
- **安装：** 同 key 原 plan；同 key 显式输入变化 409；上游正常发新版仍重放原 plan；新 key 最新；仅首次计数。
- **迁移：** 两次 preview/冻结计划；数据安全回滚；不指针回切；审核/独立可见。

## 评审修订裁定（2026-10-26）

- **人工审核恢复：** 依赖观察变化返回 409，旧申请与观察值保持不变。沿用原取消/提交权限取消旧申请并重新提交，生成新 review_id、新观察值；版本化资源可重提同一不可变来源版本，不自动追随来源新草稿。Connector 按当前编辑修订重新提交并重新审阅，不把旧冻结内容替换为当前内容。无新增刷新基线接口，不自动取消或代提。
- **删除范围：** published refs 仅从未删除来源的当前 published_version_id 进入；Team 间接展开成员 Expert 当前正式发布版。全部历史发布版不能作为保护超集；历史保留用于审计。live 草稿引用仍保护。
- **一致性与重试：** 每次请求内部一致，不保证先后两次列表/新安装版本相同。原 key 重放原计划，正常上游发布不触发冲突；显式输入变化和节点下架/删除/权限失效分别校验。
- **Admin 与通知：** 用户已明确 Admin 不用审核；沿用直接生效动作和权限，仍做 CAS、依赖及范围校验。依赖更新仅审计展示，不主动通知。生产迁移计划的离线执行授权不等同于新增 Admin 资产审核。
- **迁移执行授权：** 采用 API §5 的签名授权制品，绑定计划、操作者、范围和批次；信任配置、有效期、撤销及同 run 恢复均须验证。此为工程契约补齐，不表示用户已授权实际生产迁移。

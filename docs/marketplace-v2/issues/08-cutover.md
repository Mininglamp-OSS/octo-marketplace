# REL-01 · 四端真实证据、部署门禁、旧执行器退役与数据安全回滚（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型、D1（删除并集）、D2（无 A09）、D3（无共存）、D4（A01 四字段）、D5（CLI 迁移）、D6（private/坏图失败）、D7（Connector effective）、D8（CAS）、D9（同 key 原 plan/新 key 最新）、D10（旧执行器不可达、数据安全回滚）。本文是本地 issue 草案，不表示已创建远程 issue；不自动执行生产切换。

## 目标（Goal）

以可复现的真实环境证据放行 B 引用模型：冻结旧写入、使旧市场执行器不可达、删除运行 fallback，并交付能实际执行的中止/数据安全回滚步骤。四端（Admin/Web/CLI/Buddy）与真实 MySQL/OSS/Fleet/Daemon 证据必须齐备；不能用“跑全部测试”替代场景结果。

- **硬依赖：** [MP-03](03-installation-fleet.md)、[MP-04](04-migration.md)、[CL-01](05-admin.md)、[CL-02](06-web-cli.md)、[CL-03](07-buddy.md) 全部满足各自验收；MP-01/MP-02 为传递依赖。
- **可并行协作：** 前置实现期间准备隔离环境、固定测试数据和证据模板；正式放行必须等五个直接依赖齐备，不能跳过某端换取完成。

## 背景（Background）

2026-10-26 决策已锁定 B 模型：Expert→Skill、Team→Expert 每次读取/安装解析最新正式发布版，Connector 读 effective，`target_version_id` 仅审计；A09 restore、A29/A30 HTTP、同名/多版本共存均已明确不做。本 issue 的放行对象因此不是“固定 pin 的 v2”，而是“浮动最新解析 + 一个安装引擎 + CLI 迁移 + 数据安全回滚”的完整切换。

D10 要求：新改造完成后旧市场执行器不可达；回滚只接受兼容二进制 + 从历史生成新版本的恢复，不恢复 `current/published` 指针、不恢复 embedded/inline。部署层门禁必须以真实入口拒绝/网络隔离/停发送进程实现，不能只隐藏客户端按钮，也不能假设存在未实现的业务开关。

## 承重行为（Load-bearing）

1. **真实四端证据。** Admin/Web/CLI/Buddy 都必须在真实身份/Space/数据层上验证 B 读取、A01 四字段、A34 单 GET 最新内容、同 key 原 plan/新 key 最新；不以 mock 或单测替代。
2. **旧执行器不可达。** 部署门禁覆盖所有市场写入口、审核回调、CLI apply 通道与 A31/A32；旧市场 runtime fallback、legacy create/bind/delete 补偿及 `mk1`/frontmatter 改写不得留作灰度兜底。
3. **数据安全回滚。** 不删表/不删文件/不覆盖旧快照；不回切 `current_version_id/published_version_id`；不恢复 embedded/inline；内容回退从历史生成新版本并走原发布/审核；保留 schema/对象/attempt/map/审计。
4. **CLI 迁移。** MP-04 的两次 preview/冻结 `plan_digest`/离线授权/apply 只认冻结计划；不存在 A29/A30 HTTP 放通步骤；`waiting_review` 不是可放流结果。
5. **无共存门槛。** 同名不同内容安装冲突失败是正确结果；不把共存列为通过或失败，不为此改 Fleet/Daemon。

## 非目标（Out of scope）

新造业务 API、重做迁移器、修改 Daemon 解析市场资产、支持二进制、强停已运行实例、以旧执行器容灾、自动生产迁移、恢复指针/legacy、A09 restore、A29/A30 HTTP、同名/多版本共存。

## 源码路径与所有权

- Marketplace：`internal/service/plugin/{service,admin,container,review,listing,install,capability_*}.go`、`internal/repository/plugin/{write,read,history,review,listing}.go`、`internal/api/handler/plugin/`、`internal/service/expert/`、`cmd/plugin-backfill/`（CLI 迁移）与 `migrations/sql/`。
- Fleet：消费现有 F01 Capability POST，使用真实服务与数据库提供联调证据；不拥有或要求 Fleet 源码、API、schema/migration、cleanup、过期重占及保留期限改造，不新增查询 action。
- Admin：`src/pages/ExpertMarket/`、`src/api/expert.ts`；Web：`packages/dmworkmcp/src/api/{pluginWire,expertService,expertWire}.ts`、发布/安装组件与审核页；CLI：`internal/registry/specs/marketplace.json` 及 CL-02 的生成物；Buddy：以 CL-03 列明的市场读取、本地分发与招募安装落点为准。
- Web 依赖诊断覆盖 `packages/dmworkmcp/src/pages/{AllAssetsList,ExpertMarketListPage}.tsx`、`bridge/useExpertCatalog.ts`、实际 Expert 领域类型 `mock/expertMock.ts`，以及 dmworkskillmarket 的 API/domain/MineRow/MineTable 全部链路。中英文 reason 文案使用 `packages/dmworkskillmarket/src/i18n/{zh-CN,en-US}.json` 及 `packages/dmworkmcp/src/i18n/{zh-CN,en-US}.json` 的现有 namespace，不误写 dmworkbase 通用 locale；准确源码地图归 CL-02。
- 本 issue 拥有跨仓 smoke、证据索引、切换 runbook、旧路由调用链退役核对和兼容二进制回滚演练；不重复拥有版本、安装 attempt 或迁移 map 的 DDL。
- Daemon 使用真实、未修改的可支持版本，是运行验收对象而非本 issue 新增市场解析逻辑的位置。

## 环境与证据准备

1. 准备独立真实 Marketplace MySQL、真实 OSS 测试 bucket、未修改 Fleet 及其 PostgreSQL、可用 Workspace/Runtime/未修改 Daemon，以及 Admin/Web/CLI/Buddy 四端；接入真实 Octo 身份/Space 服务，记录版本/提交、schema 版本、网关限制及测试资源 ID，不记录凭据。确认目标 Fleet 的实际安全重试窗口，Marketplace 的保守截止时间不得超过该窗口；不能只引用设计中的 24h。
2. 建立两个 Space、普通成员、来源 owner、有既有审核权限的 Space owner/admin、平台 Admin 和 CLI Bot profile；角色可由同一测试账号兼任，但必须记录每次实际身份。准备 system/space/private 发布及草稿资产，分别保留 Space 自动审核开启/关闭的样本；不通过关闭鉴权模拟权限。至少准备同 owner private 来源→private 目标（应允许）与 system/space→private（应拒绝）样本。
3. 每个场景记录输入的无 Secret 摘要、操作者/范围、请求/attempt/run ID、预期与实际结果、数据库/对象/Daemon 证据定位和执行时间；错误回包和截图脱敏。安装请求不得抓存 Token、custom_env 或完整含凭据 POST；用请求关联 ID、受控故障点记录及数据库结果证明同次提交。环境不可用就是未满足验收，不能以 mock 回声或仅页面截图证明安装。
4. 准备受控故障注入：OSS 缺失/上传失败、数据库并发 CAS、Fleet 提交后丢响应、发送中断和审核竞态；注入在隔离代理或测试设施完成，不要求改 Fleet。注入点必须可撤除，不进入生产请求 fallback。超过安全窗口用隔离 Marketplace attempt 截止时间场景验证，不要求等待 24h 或改变 Fleet 数据保留。
5. 样本分组：常规安装使用已支持的无命名冲突最新图、多 server Connector、共享支持文件及待审核来源。**B 专用样本**：Skill S v1→v2、Expert E / Team T 引用 S；验证 E/T 未发新版时 A01/A34 下一次读取跟随 S v2，S v2 下架后 E/T 隐藏且安装/A34 失败、不回退 v1；Team 成员 Expert 发新版后角色/Leader/顺序不变。另备无依赖正常 Expert、成员无依赖但仍合法 1～30 成员/唯一 Leader 的 Team、多个直接依赖、published v1 坏依赖/current v2 已修复、从未发布坏依赖草稿、下架/删除/不可确认缺失目标和目标可读但 via 不可读/跨 Space 不可读授权组合。删除使用既有合法动作或隔离故障数据，不绕过生产删除保护。**不准备同名/多版本共存样本作为门槛。**
6. 切换前封存旧基线：在相互隔离的旧数据库/对象副本与旧 Workspace 记录实际能力；迁移后用新的测试 Workspace 比较，避免旧同名实例造成 Fleet 复用/冲突而污染结果。保留原历史字节和证据，不把可下载历史等同于可合法重新发布。旧 pending review 必须按 MP-01 形成可证明的新冻结快照，或明确 fence 并由有权者重新提交；不能留到放流后靠 current 补齐。

## 真实数据与服务验收

5. **发布隔离。** 发布 v1→保存 v2→提交 v2→保存 v3→批准 v2；核对外部列表/搜索/分页/详情都是 v2，编辑仍是完整 v3，文件、关系、标签、名称和成员计数一致，批准不把冻结内容写回 current。
6. **Admin ZIP。** Expert/Team 仅上传/重传原 ZIP，清单使用目标 ID（版本可省略/仅审计）且是最终关系，无选择器或页面关系覆盖，过期 `edit_revision` 返回冲突；A24 拒绝 JSON 内容写。现有 Admin 写入、发布审核与上下架访问检查不变，不新增发布矩阵、上架按钮或接口；非默认场景既有投放可见性保留，default 不能隐藏。
7. **依赖矩阵与职责。** 目标 ID 归属/release/effective 证明正确；system 仅引用已发布 system，space 仅已发布 system/同 Space 的 space，**同 owner private→private 允许，space/system→private 拒绝**，跨 Space 拒绝，Admin 不绕过。拒绝本地路径/Fleet `skill_id` 充当市场关系，本地组合不产生市场资产。原 owner 提交、审核角色及 Space 自动审核开关行为不变，自动/人工批准都校验最新图，未授权数据不泄露。
8. **保护与并发。** 保留既有 active source、`r.status=1` 且来源/关系均未删除的 live incoming 保护，无 listing 过滤，草稿 live 边同样阻止删除；补 published refs 及Team→最新发布 Expert→Skill/Connector 间接引用保护；**验证没有“曾发布即禁止删除”总禁令**：仅历史发布、无任何保护引用的资产可删除。引用式来源删除仅删除自身及自身关系，不删共享目标；旧 embedded 历史仅显式迁移。保存/批准/删除竞争满足统一锁序和 CAS。
    **A01 公共可用性与 COUNT/facets：**通过真实 Web 与 Buddy 公共列表抓包并对照 DB，逐一使直接 Skill/Connector、Team 成员 Expert 及其依赖下架、删除或缺失，来源 Expert/Team 不再出现在公共列表。证明在 SQL 分页/COUNT 之前使用同一最新解析谓词，翻页无事后过滤空洞、total 正确，分类 `plugin_count` 与公共 tags 一致；`current`/旧版本不得替代最新解析。缺应有版本/refs、DB 故障走既有错误，不伪造 available；列表判定不调用 A34 或读文件逐卡校验。
    **B 跟随与不回退：**发布 S v2 后 E/T 下一次公共读取/A34 自动用 v2，E/T 无需发新版；S v2 下架/删除后 E/T 隐藏且 A34/安装失败，不得回退 S v1；S v3 合法发布后下一次查询恢复。Team 的角色/Leader/顺序不随成员 Expert 发新版改变。
    **恢复与投影：**来源 `listing_state` 和既有审核状态/动作保持原值，不新建治理状态/接口。先只修草稿，公共仍隐藏且 mine 继续诊断旧 published 版；再发布移除/替换依赖的新来源，下一次列表查询恢复；依赖合法恢复上架也在下一次查询重算，不新增推送/轮询。从未发布根按 current 草稿评估；无依赖图 available/`reasons=[]`。metadata available 与 A34 文件成功、安装成功独立验收。
9. **Connector。** 编辑未批准不影响有效配置；审核冻结 `effective_revision/digest`，审批期间变化报 `dependency_changed`（人工）或内部重试（自动）；批准后新安装读取新有效态，旧 attempt 仍用原计划，重复 server key 全图失败。
10. **文件与 OSS。** 通过受授权下载重取历史 ZIP/SKILL.md/支持文件并核对摘要；模拟上传失败后确认历史/审核/其他资源/安装计划引用对象都在。缺文件、二进制、预算超限等准备失败均证明 Fleet mutation=0。
    **A34 单请求最新完整内容：**对 Expert 和 Team 分别仅以既有认证/Space 上下文发送一次 `GET /market/api/v1/plugins/{plugin_id}/distribution`，不需 workspace/runtime/custom_env/幂等 key。核对 200 `{data:DistributionBundle}` 的 `schema_version/root/graph/assets/bundle_digest`，每 graph `node_key` 恰好一项 asset；依赖目标按本次最新解析版本映射，全部 `AGENTS.md`、`SKILL.md` 及支持文件的 path/mime_type/content/content_hash 与原始 UTF-8 字节/hash 一致，不归一化换行。按 API §6.1 规定的数组排序和 RFC 8785 JCS、排除 `bundle_digest` 自身重新计算摘要。核对有效 Connector 公开配置/占位符不混入未批准编辑或凭据。不得仅有 ID/链接/storage key，无 `installation_name`、不重写 frontmatter/原市场名、不生成安装副本；核对原市场快照未变。抓取 Web 完整展示、CLI 完整查询、Buddy 完整准备各只有一次市场内容 GET，不先 A03 或再扇出 detail/versions/skill_md/download/files；A03 仅独立图展示。读取前后核对 Fleet 零调用、零安装记录/资源创建、发布状态未变与 `Cache-Control: no-store`。
    **B 动态性：**发布 Skill/成员 Expert 新版或产生新 Connector effective 后再次 A34，确认返回新内容且 `bundle_digest` 变化；同一 Expert/Team 版本不承诺永久同一依赖内容，这是 B 的明确代价。读取期间根发布/Connector 生效变化时，单次响应仍来自同一一致性快照，不混用节点修订与实际配置。
    **A34 失败关闭：**逐次注入缺节点/文件、错 hash、权限拒绝及 16MiB 响应 JSON UTF-8/既定文件图预算越界；16MiB 按含 data 包络、metadata 和 JSON 转义的实际出站字节计，另验证单文本 1MiB、每 Skill 支持文件 50/全图 500 及成员/直接依赖数量上限。核对整体失败及 API §8 固定顶层 code/details.reason：超限 413 `PAYLOAD_TOO_LARGE`/`budget_exceeded`、文件完整性 500 `INTERNAL_ERROR`/`artifact_integrity`、索引故障 500 `INTERNAL_ERROR`/`relation_index_inconsistent`、缺失/无权目标使用既有安全 404；不得为 reason 新增顶层 code。无分页、截断、裁剪后 200、latest 或客户端历史文件/缓存 fallback；失败同样无 Fleet 调用及业务写入。单请求读取成功不代表 Fleet 安装或 Daemon 共存通过。
11. **原子安装。** 用两个 HTTP 入口安装同类 Expert/Team，确认都到同一 `InstallCoordinator` 和一次 Fleet 原子事务；不存在先创建再绑定再补偿链路，失败不删除共享 Skill，不强停旧运行实例。**不执行同名/多版本共存条件验收**：本期同名不同内容必须明确冲突失败，且不生成 `mk1`/不改写 frontmatter；若未来要做，另立决策并补真实 Fleet/Daemon 加载证据，不由本 issue 扩大。
12. **网络安全重试。** 使用真实现有 F01，在 Fleet 事务提交后丢 POST 响应，Marketplace 保留未知；现有窗口内只用原 key、原 plan、原完整规范化请求及重新提供的相同运行时 Secret 重试，返回相同资源 ID 且不增加计数。同 key 期间发布 Skill/Expert 新版或产生新 Connector effective，不得改变原 plan；新 key 新安装才解析最新。Secret 不落本地或 Marketplace，不新增远端结果查询；已有本地已知成功可回放。
13. **幂等边界。** 同用户/Workspace/key 并发只建一次；换 Space/参数/custom_env 冲突，Connector/root 升级不能改变原计划。冻结 plan 节点被删除/下架返回 `dependency_changed`，不静默换最新。超窗由 Marketplace 客户端阻止 POST，无需改变 Fleet 过期重占/cleanup/保留期限；无法重建原参数、输入不同或结果未知时保留未知并人工核对，不自动换 key、删记录或 fallback。
14. **CLI 迁移实装比较。** 使用 MP-04 CLI 的两次 preview/冻结 `plan_digest`/apply 转换 system/Space 与多 server/Team 样本；按“环境与证据准备”的隔离旧基线与新 Workspace 对比指令、成员角色/Leader、server key、完整技能文件与独立可发现性，不在新线上保留旧执行器做对照。**不存在 A29/A30 HTTP 调用；apply 只认冻结计划+离线授权。** private 来源只验既有本人发布及迁移阻断/范围保持：同 owner private→private 可保留，但不能为通过验收公开旧 private 内容，也不能把 private 目标作为 system/Space 依赖；无法满足合法已发布依赖与原受众的来源保留历史、明确 `blocked`。
15. **迁移异常与回滚。** 验证 source CAS 失败、部分目标已建、中断重跑、`waiting_review`/拒绝、缺历史证据/文件/二进制/硬编码路径等逐源结果；保留 map 和原历史，不报假 complete。**数据安全回滚演练：**只退兼容二进制，保留增量 schema/对象/attempt/map；不删表/不删文件/不覆盖旧快照；不把 `current_version_id/published_version_id` 改回旧值；内容恢复从旧历史生成新版本并走原发布/审核，不恢复 embedded/inline、不调用 A09 restore。

## 四端与真实 Daemon smoke

16. **Admin：** 上传 `schema_version=2` ZIP，预览目标 ID/本次解析版本，完成 import、重传、冲突修正；无选择器、无页面关系覆盖。错误定位包/关系，不自动猜 ID 或静默删边；发布审核及上下架、tenant 权限沿用既有动作。目标后来发新版时下一次预览显示新版本，不误报来源失败。
17. **Web：** 作者 editing 读取/保存/发布、消费者 published 读取，完成原 Space 审批与最新图展示；B 跟随样本只验证 A34 读取/显示最新内容，实际 Workspace 安装使用无命名冲突样本。缺失/无权节点整体失败，不伪装残缺可安装；**无恢复草稿入口**。
    **Web“我的发布”真实 UI：**分别在全部、专家、专家团 tab 记录 A01 `mode=mine` 响应、行数/total 和截图，确认 owner/Space 范围内原本可管理的非删除草稿/待审/发布/下架资产全部保留，异常不整行消失，不纳入自己已删除资源，不新增回收站。全部 tab 必须覆盖 `getMySkills` → Skill domain → MineRow 路径；专家/专家团覆盖 expertWire → toMineRow。诊断 unavailable 独立显示“依赖异常”，原 listing_state/display_status 标记、审核理由及操作入口仍在；Admin 列表管理入口保留。hover 和键盘 focus 均能打开 Tooltip，多个可见直接/间接原因按固定 code 中英文映射，间接 via 不含根/目标，Connector 不携 version_id；不显示服务端 HTML/整句。
    **诊断根与隐私：**A01 仅 mine 的 Expert/Team 必返 `dependency_availability`，公共与其他类型省略。`published_version_id` 存在时即使展示 editing 内容也诊断当前发布版及准确 `evaluated_version_id`；从未发布显示 `evaluated_view=editing` 和“草稿依赖异常”，不得称已从市场隐藏。仅目标和全部 via 逐项有权读取才可返回具体 code/target/via；跨 Space、via 无权、不可确认缺失或无权读已删元数据时只聚合一条匿名 `dependency_unavailable`，不泄露 ID/名称/类型/版本/路径、隐藏数量或下架 vs 删除细节。混合可见原因和匿名原因也须验收。抓包证明 source flags/reason 来自服务端，无前端过滤或依赖/A34 扇出取 tip；available 无异常标记，unavailable 至少一条原因。
18. **CLI：** 从真实 spec 生成的命令完成发布、最新内容查询、图查询和旧安装 URL 调用；新意图新 key、网络重试复用 key，缺幂等头拒绝，版本/关系/CAS 参数与服务契约一致，输出不含 Secret。**不存在 restore/A29/A30 命令或 HTTP 调用**；生产迁移只走 MP-04 CLI。
19. **Buddy：** 按 A34 正式契约验收完整 Expert/Team 单 GET 及本地 Skill/Connector 入口的权限/历史受众、全文件与有效配置、失败无补取；依赖发新版后下一次读取跟随最新。用用户身份完成 Workspace 招募及窗口内原 key/完整原请求重试；pending 仅无 Secret 防重复/人工核对元数据，重启无法重建参数不自动重发。Buddy 不发布/审批、不绕 Bot 限制、不转旧入口或伪造成功。
20. **Daemon：** 常规安装检查实际目录、SKILL.md/frontmatter、正文/metadata/支持文件摘要、成员 bindings、MCP transport/server key 与技能发现，不仅凭 Fleet 响应判通过。**同名不同版本共存不在本期验收，且不预设稳定 `mk-` 名、不改写 frontmatter/目录名**；若未来另行批准，必须用未修改 Daemon 单独验证，不由本 issue 扩大。A34 始终返回未变的原市场内容，其验收独立于此。

## 接口与退役核对

- 汇总 Marketplace OpenAPI 3.1、swag 生成物及 `make openapi-check` / `make openapi-diff` 输出；逐条核对版本/发布、CAS、显式关系、A34 最新 DTO/只读错误/no-store 及两安装入口必填头与错误，拒绝非预期 breaking diff。**A09、A29、A30 不在计划内、不应出现在新生成物或路由中**；旧契约若仍列出，以 00/02 的 B 口径为准，不能为消除 diff 恢复旧 fallback。若 diff 因已批准项非零退出，保留原输出并明确范围，不能伪造“diff 全绿”。
- 核对 Fleet 现有 F01 Capability POST wire 未变，以真实服务/数据库证明原 key、完整原请求在现有窗口内只提交一次；无 Fleet 新 API、action、schema/migration 或部署改造验收。核对四端 spec/类型/生成物来自同一 [API 契约](../02-api-contracts.md)，不另造接口兼容层。
- 对照路由→Handler→Service 调用清单，证明 A31/A32 两个市场安装入口都调用 `InstallCoordinator`；删除其旧 provision/rollback adapter、路由调用和市场独占 dead code。`service/expert` 仍被独立非市场业务使用的方法可保留；不要求整个目录或非市场 provision 代码归零。
- 调用链清单显式区分仍注册的 `POST /experts/{expert_id}/install`、`POST /squads/{squad_id}/install`：源码 `internal/api/handler/expert/handler.go::Register/InstallExpert/InstallSquad` 及 `router.go` 仍直连 `service/expert`。核验两者的独立身份域与实际调用者；本期四端的市场安装调用必须迁至 A31/A32 且不能回退至这些入口。独立非市场模型/调用者保持原协议，不新增 Plugin 映射、不迁移这些业务、不将其保留共享代码视为市场退役阻断；证据应证明它们没有被 A31/A32 或市场 fallback 调用。
- 删除市场分发运行时 embedded/inline 解包、current/latest 补目标版本、缺文件跳过、空 MCP 代替、legacy 执行 fallback 和 `mk1`/frontmatter 改写；历史取证/迁移 reader 可保留，但不得被市场分发路由调用。旧市场 URL 保留不代表旧市场执行器保留；独立非市场包解析不因同名 helper 被误删。

## 切换操作顺序与阻断

1. **建立部署门禁并备份：**本 issue 不假设已有“新链路/分发/发布/apply”业务开关，也不新增市场状态或治理 API。先在实际现有 gateway/部署设施形成可执行 runbook：列出网关与服务直连入口、路由/后台任务、操作者、阻断/恢复命令和配置制品版本；以入口拒绝、网络隔离及停止发送进程实现门禁，禁止仅隐藏客户端按钮。覆盖 A12/A13、审核批准/回调、Admin 写入/导入/重传、MP-04 CLI 执行通道、A31/A32 及经调用链核对仍可写市场来源或触发市场安装的旁路；不把独立非市场 Expert/Squad 业务扩成本期迁移。没有能阻断全部市场入口和后台发送的真实部署权限/设施就不部署放流，不凭空记录某开关已关闭。备份 Marketplace 库、对象及引用清单，演练恢复到隔离库/桶；Fleet 不安排改造部署或 migration。
2. **维护窗口冻结并部署：**先阻断外部新增市场写入、发布和分发，停止旧市场后台写入/发送者，等待在途请求结束；所有可能已发送而未确认结果的安装逐项保留 UNKNOWN 证据，不以进程停止推断未提交。避免新旧市场写入二进制共用同一库长时间混跑；受控先部署 Marketplace 增量 schema/不可覆盖对象能力，再部署新服务与四端静态资源/兼容 CLI/Buddy 包。新服务尚未公开时仅开放授权迁移、审核和验收操作者的隔离访问。后端始终拒绝新 embedded/inline/缺目标 ID 市场写入，旧客户端不能绕过；关闭 A31/A32 的旧执行器调用，不以迁移失败触发 legacy，也不部署 A09/A29/A30。
3. **执行批准迁移与审核：**按批准范围运行 MP-04 CLI 的 inventory/两次 preview/冻结 apply（Skill/Connector→Expert→Team）；记录每个来源发布身份、完整文件、目标 ID 依赖、范围授权与可构建计划。保持普通写入/分发门禁，受控允许既有有权审核者处理迁移产生的审核及回调；不能一边冻结全部批准入口一边要求 `waiting_review` 完成。审核等待/拒绝不是可放流结果，也不能为了缩短窗口绕过既有审核。**CLI 不是绕过授权：离线授权与冻结计划必须一致。**
   - 已确认旧内嵌 Skill/Expert 独立市场可见：递归核对 Team→Expert→Skill 拆分结果，在既定 system/Space 范围内分别通过 A01 和真实 Web/Buddy 市场看到独立条目，原依赖方使用目标 ID 引用（版本仅审计）。不扩大受众，不因该产品确认自动执行生产迁移；保留现有审核、下架/删除及列表过滤。
4. **逐源隔离异常与校验读取：**无法转换的记录保留历史，由既有新模型资格/完整性检查阻止全部分发入口；不临时添加持久化“可分发”状态或假造 release。输出来源 ID、安全 reason 与修复责任，放行清单同时列出合格和排除来源。对真实公共列表、mine、历史读取及异常来源直接请求验证排除行为；若遗留坏数据导致合格来源的列表也不能按契约读取，先处理该阻断，不能靠前端过滤或删历史换取可用。
5. **隔离试点与兼容回滚演练：**优先在完整隔离部署/测试 Workspace 中开放合格范围完成上述 smoke、兼容二进制回滚和恢复；生产如按用户/Space 灰度，必须有实际部署路由隔离和权限约束证据，不能假设有未实现的按 plugin 业务开关。观察错误/冲突、UNKNOWN 及安全重试。发现跨范围泄露、完整性失败、重复资源或市场旧执行器被调用，立即恢复市场入口阻断并处置。
6. **扩大与完成：**仅在全部必需验收有证据、兼容回滚包已验证及负责人批准后，按记录的部署门禁恢复命令开放范围；同名共存不计为必需通过或失败。旧市场运行 fallback 必须在首轮新链路试点前已不可达，最后只完成市场代码退役核对，不把它留作灰度兜底。可分发集合内 embedded/inline/缺目标 ID/Connector 携版本/refs 不一致均为零；历史证据允许旧格式，不为全库零计数改写历史。
   - 逐步放通顺序必须落到实际路由规则：先为授权人员开放 MP-04 CLI 只读 inventory/preview 和 Admin/历史取证读取；再受控开放 CLI apply 及迁移所需的既有发布/审核批准/回调；新图就绪后对试点身份开放 A01/A02/A03/A34 及历史读取；最后才开放 A31/A32 与普通新写入。每一步保存配置差异与拒绝/允许请求证据，不能把分发门禁打开作为只读验收前提。**不存在 A29 preview/A30 apply HTTP 放通步骤。**
   - A31/A32 门禁关闭时客户端停止自动 POST，现存 attempt/pending 及 UNKNOWN 原地保留，已知本地成功可展示已有结果，不伪造远端查询。确需窗口内原请求重试时，只在实际设施能够限制为原用户/Space/Workspace/原 key 意图且原完整参数可重建的受控通道执行，原鉴权/资格和截止时间检查不得绕过；若无法做到受控放通，就保持关闭和未知，不能以管理员代装、开放所有新意图或新增恢复 GET 解决。故障时先重封市场写入/批准/apply 与 A31/A32、再停后台发送，保留只读证据通道。

## 可操作中止与回滚

- 立即执行已演练的部署门禁命令，阻断新增写入/发布/CLI apply 及新增分发，暂停后台发送；保留授权只读取证通道。记录开始时间和在途请求，保存 Marketplace 数据库、不可变对象、attempt/map 及现场证据；不删表、不删版本、不清空队列来伪造恢复，不改变 Fleet 记录保留。门禁不是未定义的业务开关，不能只清空 Fleet URL 或停前端来替代全入口阻断。
- 对 DISPATCHING/UNKNOWN 保留原 attempt/key/plan；已知成功回放本地结果，未决人工依据真实 Fleet 服务/数据库证据核对，不引入客户端远端 receipt 查询。仅在现有窗口内且能重建含相同 Secret 的完整原请求时安全重试；超窗或无法重建则保持未知，禁止自动换 key 重装、删记录或补偿删除，不推断失败。
- 回滚服务只能选择已演练、理解 current/published、release、refs、effective、attempt 与迁移映射的新模型兼容二进制；客户端只能回滚到不发送旧容器且正确传 CAS/幂等头的兼容版本。保留增量 schema 和全部对象。
- **数据安全回滚：**不删表/不删文件/不覆盖旧快照；不把 `current_version_id/published_version_id` 改回旧值；需要停止新分发时用下架/部署隔离，不用指针回切；内容回退只能从字节与关系证据完整的历史生成**新版本**（forward-fix），给新合法标签并按原职责发布/审核；原目标引用仍满足当前 release/权限/历史受众时可保留，否则重新 preview 并显式批准合法映射。旧 embedded/inline 历史只能经 MP-04 CLI 转换，**不调用 A09 restore、不由 restore 猜目标版本**；缺历史证据/文件或无法满足范围时保持阻断。
- 恢复前重新 inventory/核对 CAS 与批准范围，确认所有未决安装和对象完整性后重开合格范围；原故障 smoke 必须通过，不能以重启、清缓存或启用旧执行器代替修复。

## 逐项验收与部署前置（Acceptance）

- [ ] 五个直接依赖均完成；真实 MySQL/OSS/Fleet/Daemon 与 Admin/Web/CLI/Buddy 四端 smoke 有脱敏证据、资源 ID 和可复现步骤，不以“跑全部测试”替代场景结果。
- [ ] 草稿/发布隔离、权限/受众、发布保护、Connector 漂移、文件保留、并发 CAS 和预算失败关闭逐项通过。
- [ ] **B 解析与不回退：**Skill/成员 Expert 发新版后引用方下一次 A01/A34/安装跟随最新且无需发新版；Team 角色/Leader/顺序不变；最新版下架/删除时隐藏/失败且不回退旧版；恢复后下一次查询恢复。
- [ ] **D1 保护并集：**live 草稿引用、published refs、Team→最新发布 Expert→Skill/Connector 间接引用保护；无“曾发布即禁止删除”总禁令；引用式来源删除不删共享目标。
- [ ] **D6/D7：**同 owner private→private 允许，space/system→private 拒绝；Connector 审核观察与 409 生效、安装/A34 读 effective。
- [ ] 公共 Web/Buddy 的 A01 分页/COUNT、分类 `plugin_count` 及公共 tags 使用同一最新解析谓词；直接/间接坏依赖隐藏、旧版本不被 current 替代，来源状态不变；草稿修复不恢复、正式发布修复及依赖合法恢复在下一次查询恢复。
- [ ] Web 全部/专家/专家团真实 MineTable 保留全部原可管理非删除资产及 total、原状态和操作，独立标记/多原因 Tooltip 的 hover 及键盘 focus 均有证据；published/editing 诊断根、无依赖 available、跨 Space 与 via 权限匿名聚合、不泄露隐藏数量、无前端过滤/扇出逐项通过。
- [ ] Web/CLI/Buddy 完整内容场景各一次 A34 GET 拿全正确最新目标、全部原始文件与有效 Connector；无 Fleet 调用/业务写入，原市场名称/内容未变；依赖发新版后 `bundle_digest` 变化；错误全失败、无裁剪或客户端 fallback。A34 正式确认不等于本轮已运行通过。
- [ ] 两入口同原子引擎、POST 响应丢失后原 key/完整原请求安全重试及幂等计数均有真实 Fleet 服务/数据库证据；同 key 不因依赖发新版改变原 plan，新 key 才解析最新；超窗客户端阻止重发，未知保留与人工核对不依赖 Fleet 改造。
- [ ] **无共存：**同名不同内容安装冲突失败，不生成 `mk1`、不改写 frontmatter/目录名；不把共存列为通过/失败门槛。
- [ ] MP-04 CLI 的两次 preview/冻结计划/映射/恢复与真实安装前后能力比较闭合；apply 只认冻结计划+离线授权，**无 A29/A30 HTTP**；异常有逐源阻断，未改历史、未自动生产迁移。
- [ ] 旧 Expert 内嵌 Skill 及 Team 内嵌 Expert→内嵌 Skill 递归拆分后，在对应 system/Space 市场以独立条目可见且可合法引用；Web/Buddy 列表、目标 ID 引用及范围外隔离有真实证据。
- [ ] OpenAPI check/diff、Fleet wire、四端生成物已核对；**A09/A29/A30 不在生成物/路由中**；旧市场写入、A31/A32 的旧执行调用及市场运行 fallback 已退役，旧市场 URL 仍按新引擎工作；独立非市场 Expert/Squad 共享代码保留有实际 caller 及调用链隔离证据。
- [ ] **D10 部署门禁：**部署层全入口/后台发送门禁、在途未知结果保留、兼容二进制回滚、完整历史生成新引用版本和恢复放流已在隔离环境演练并留证；旧市场执行器在新链路试点前不可达；记录实际执行命令/操作者/制品，不要求关闭未实现的业务开关。迁移审核所需受控入口可达，旧 pending review 已转换或 fence，无新旧写入混跑旁路。
- [ ] **数据安全回滚：**不删表/不删文件/不覆盖旧快照、不恢复 current/published 指针、不恢复 legacy/embedded、不调用 A09；内容回退从历史生成新版本并走原审核，回滚后重新 inventory/CAS/完整性核对。
- [ ] 生产负责人具备批准范围、备份、兼容包、操作者、监控/告警与中止权限后方可人工放行；任一必需环境或证据缺失即保持关闭。

## 验证与交付（Verification）

本 issue 在实现阶段运行上列检查并归档脱敏证据；本次设计文档编写不运行 build、lint、tests、formatters 或 smoke，也不声称实现已通过。证据索引至少包含环境版本、操作者、请求/run/attempt ID、预期/实际、数据库/对象/Daemon 定位与门禁命令输出；任一必需环境或证据缺失即保持关闭。

## 本轮评审修订验收

- [ ] 四端验证原 key v1/新 key v2、显式输入变化冲突、节点下架/删除/无权且 UNKNOWN 保留；不是只验证文档字段。
- [ ] 验证人工审核冲突→有权取消→同来源版本新审核→重新批准；Connector 当前 CAS 重提；Admin 直接生效无审核步骤。
- [ ] 验证历史引用已移除且当前/live 无引用时可删除，仍有当前或草稿引用则阻断；依赖更新仅审计展示。
- [ ] 演练迁移授权篡改/过期/撤销/轮换/同 run 续签恢复；迁移计划许可不变成 Admin 资产审核，失败不丢 map 或重建目标。
- [ ] 实施前复核 #126/#127/#128 head 与合并状态，记录选定基线和安全修复复用清单；保留 #128 readiness/回执手册，提供真实环境证据。文档覆盖 80 项不等于实现通过 80 项。

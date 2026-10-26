# 跨服务 API 权威契约

状态：**2026-10-26 最终决策已锁定，引用模型为 B。** Expert→Skill、Team→Expert 每次读取/安装解析目标“当前正式发布版”；`target_version_id` 仅可为可选审计值，不是公共读取、A34、安装、审核的权威依据；Connector 按 ID 读 `plugin_connector_effective`，无版本。A09 restore-to-draft、A29/A30 HTTP 迁移、同名/多版本共存本期均不做；生产迁移改 CLI + 离线授权（§5）。A34 一次返回按 B 解析的最新专家/团队及全部原始内容，客户端不得按依赖补请求；`bundle_digest` 随最新解析结果动态变化。Admin ZIP、Fleet 既有重试、system/Space 范围、原审核和市场动作保持。不得遇错降级、不得回退旧版、不得把同 key 重试静默改成最新内容。模型见[技术方案](01-technical-design.md)与[最终决策](00-decisions.md)。

## 1. 通用规则

### 1.1 路径、身份和包络

- 下表 Marketplace 路径为相对服务前缀；用户实际 URL 为 `/market/api/v1` + 路径，服务现有注册为 `/api/v1` + 路径。保留既有网关，不新增路径探测。
- Fleet 对外文档路径为 `/v1` + 路径；网关为 `/fleet/api/v1` + 路径；内部 client 按配置选择唯一 base path。swag Router 写相对路径，不重复 `/v1`。
- Tenant：有效用户/Bot token（`Token`或Bearer，不能两者矛盾）、`X-Space-Id`；owner/Space从权威身份得出，不信body。User/Bot的现有发布、提交与管理权限不变，owner提交及既有审核者校验继续有效；安装及同键重试仅允许用户身份。
- Admin：本人session、既有superAdmin/marketAdmin鉴权及跨Space管理读不变；不能绕过已确认的system/Space依赖矩阵。不新增生命周期管理权限或运营按钮。
- 安装/Fleet：只允许用户身份；单个 `X-Workspace-Id` UUID，不接受 slug/query 兜底。Marketplace 转发原用户 token/Space，不能使用平台管理员 token 代装。
- 单体成功 `{data:{...}}`，列表 `{data:[...],pagination:{page,page_size,total}}`，空成功 `{data:{}}`；日期 RFC3339 UTC；page 默认1、page_size默认20/最大100。新字段 snake_case，布尔 is_/has_/can_。
- JSON 请求拒绝未知字段、多个根值、重复键和错误类型；所有写入缺少必需字段明确400，`relations` 缺失或 null 为 400 `relations_required`（只有显式 `[]` 才表示清空）。HTTP header 大小写不敏感，约定写法不影响现有客户端。
- JSON 错误统一 `{error:{code,message,details,hint}}`，code 只用既有12项。文件下载200为ZIP字节流，是显式包络豁免；失败仍JSON。所有内容/历史文件读取鉴权，不能返回 bucket/object key 或 storage_uri。
- **B 解析（唯一权威）：** Expert→Skill、Team→Expert 每次读取/安装按目标 `published_version_id` 解析“当前正式发布版”，且 `status=1`、`deleted_at IS NULL`、`listing_state='published'`。目标没有正式发布版、或最新发布版被下架/删除，即依赖不可用，**不回退旧版**。Connector 按 ID 读 `plugin_connector_effective` 的当前有效修订。Team 的 `member_key/role/is_leader/sort_order` 由 Team 边决定，不随成员 Expert 发新版改变；成员 Expert 只更新自身内容及其引用的 Skill。
- **一致性：** A01、A34、安装准备、审核观察复用同一个“最新发布解析”服务，不能各写一套；一次请求在同一个数据库一致性快照内解析完整最新图，保证单次响应或首次安装计划内部不混版；不同时间的列表查询与新安装可解析到不同版本。
- **B 的明确代价（必须写进客户端行为）：** 同一个 Expert/Team 版本，不同时间读取/安装可能得到不同依赖内容；A34 `bundle_digest` 动态变化；审核不能冻结批准后的依赖内容，只能保护提交到批准窗口；Skill/Expert 发新版可能影响引用方；本期仅审计和展示当前解析版本，不因依赖更新主动发送通知；安装可复现性只保证“同 key 重试首次冻结 plan”，新 key 新安装按最新解析。
- **target_version_id：** 仅 Expert→Skill、Team→Expert 可给，且只是上次解析/审计值；提供时必须属于目标且有 release，但不参与公共/安装/审核解析。省略合法；Connector 必须完全省略。source/target `plugin_id` 才是关系权威身份。
- 市场与运行态分开：市场关系使用目标的`plugin_id`（可选审计`version_id`），不接受本地路径或Fleet skill_id。纯本地组合不调用Marketplace，工作空间本地Skill直接推Fleet；不创建private市场中间层。system仅引用已发布system；space仅引用已发布system/同Space的space；**同 owner private→private 允许，space/system 来源不得引用 private，跨 Space 禁止**；Team递归同样校验。private 目标不扩大受众，A01 公共列表、A34 与安装只对同 owner 可见。访问与上下架资格沿用现有检查，不新增豁免。

### 1.2 本方案的 breaking changes

| 调整 | 原因 | 必须同步的调用者 |
|---|---|---|
| Expert→Skill、Team→Expert 的 `target_version_id` 改为可选审计值，不再参与解析；Connector 仍禁止该字段 | B：读时最新，字段不能固定依赖 | Admin/Web/CLI及Bot工具，Buddy只读 |
| 删除 A09 restore-to-draft（端点、DTO、客户端动作） | D2：不做历史还原为草稿 | 全部 Admin/Web/CLI/Bot 客户端 |
| 撤回 A29/A30 HTTP，生产迁移改为 CLI 两次 preview/冻结计划（§5） | D5：CLI + 离线授权，不做 HTTP 迁移 | Admin/CLI/运维；CL-02 不接 A29/A30 |
| 更新必须 `expected_edit_revision`；完整 Write 的 relations 必须显式数组 | D8：防并发覆盖；Skill parse-task import 使用其独立请求、服务端生成空关系 | Admin/Web/CLI及Bot工具 |
| 市场默认 published，编辑显式 `view=editing`；mine 仅 editing，公共仅 published | 草稿隔离 | 所有客户端 |
| A32 保留 URL 但接入同一安装引擎，必须 Idempotency-Key、用户身份、Workspace UUID | D9：同一原子安装边界，不保留 legacy 行为 | Web/CLI；Buddy已有幂等入口 |
| detail_graph 改为“请求时最新解析”的版本 nodes/edges | B：节点版本是本次解析结果，不是 pin | 当前未发现四端消费；未来调用者按新schema，不据此新增本地版本注册表 |
| A34 单次返回最新解析的完整专家/团队内容，`bundle_digest` 动态 | B：一次解析、消除客户端扇出与混版 | Buddy；Web完整内容界面；CLI完整内容查询 |
| Connector 无业务版本；审批观察 effective 修订/摘要 | D7：无版本但不能审核漂移 | Admin/Web/CLI审核与展示 |
| Admin Expert/Team 停止通用 JSON 写入；A24 拒绝这两类，A25 ZIP-only | C06：ZIP 是内容/关系权威 | Admin原ZIP导入/重传及所有实际JSON调用者 |
| 同名/多版本不共存；同名不同内容安装冲突失败，不生成别名、不改写 frontmatter | D3：本期拒绝共存，不改 Fleet/Daemon | 安装客户端及迁移校验 |

已确认范围按统一契约同步调用者，不保留旧字段推断、旧数据自动升级或错误fallback。既有权限、审核和市场动作不重新设计，Fleet协议不变。**没有固定 pin，也没有“失败后补 latest、切 legacy、删边、跳文件”的隐藏分支；“最新”是唯一显式规则。** 同名/多版本共存明确不做，不阻止完整内容查询按资源＋本次解析版本身份返回数据，也不构成安装隔离承诺。

## 2. 公共 DTO（规范性字段定义）

下列 `{}` 表示合法开放领域JSON对象而非未定义实现。所有 ID 是非空字符串，服务生成的 version_id/attempt_id 为 UUID；既有 plugin_id 按原契约兼容。所有版本化字段在 Connector 响应省略，不能填假值。

### 2.1 RelationWire / RelationSnapshot

核心wire约束：expert_skill、expert_team_expert 写入目标 `plugin_id`；`target_version_id` 可选，只保留为上次解析/审计值，公共读取、A34、安装、审核一律按 B 重新解析目标当前正式发布版，客户端不能靠它固定依赖。expert_connector 仅目标 ID。以下接口变化只服务独立引用与 B 解析，不新增市场治理动作。

| 字段 | 类型与必需性 | 规则 |
|---|---|---|
| relation_id | string，新建可省略，更新已有边应回传 | 来源关系身份；容器重传按类型+目标匹配保留 |
| source_plugin_id | string，写可省略，读必返 | 写时如有必须等于当前来源 |
| target_plugin_id | string，必填 | 关系权威身份，不允许名称替代 |
| target_version_id | string，可选；仅 expert_skill/expert_team_expert 可给 | 提供时必须属于目标且有 release；只作审计，**不用于公共/安装/审核解析**；省略合法，不再有 `dependency_version_required`；Connector 必须完全省略，null/空字符串 400 `connector_version_forbidden` |
| relation_type | enum，必填 | expert_skill / expert_connector / expert_team_expert |
| sort_order | integer，默认0 | >=0 |
| data | object，默认{} | team仅member_key/role/is_leader且三项必填；member_key正则/长度、role非空≤500字符、唯一Leader及非Leader禁保留role=leader见技术§5.1；其余关系只能{} |

RelationSnapshot 与上述同字段，source_plugin_id 可由所属版本推导；不允许客户端写 observed_*。浮动关系的 `target_version_id` **不进入 content_digest**，也不参与“是否重复保存/是否新版本”的判定；目标发新版不改变关系、不要求来源发新版、不触发引用方重新审核。Review 独立返回 `connector_observations:[{plugin_id,effective_revision,effective_digest}]`，这些是服务端写入的观察值，不是关系 pin。

来源仍使用既有 private 本人草稿/发布时：**同 owner private→private 允许；space/system 来源不得引用 private；跨 Space 禁止。** private 资产作为依赖时不扩大受众，A34/安装只对同 owner 可见。纯本地组合不因此纳入市场。

### 2.2 PluginProjection / Detail / Version

本节投影用于读取来源自身版本、隔离未发布编辑，并表达 B 下的解析结果；沿用现有owner/Admin鉴权，不另设取证模式或新增内容治理权限。历史受众检查不得因资源后来公开而泄露旧私有内容。

PluginProjection 保留既有身份、plugin_name/type、category/tags/publisher/icon、owner_id/space_id/visibility、listing_state/display_status、评分与计数；新增：

- `view`: published/editing/effective，必返，Connector市场为effective。
- `selected_version_id`、`version_label`：**来源自身** view 对应的实际版本；published视图必返，为来源当前正式发布版（根版本），不随依赖更新改变；Connector省略。
- `published_version_id`：Tenant版本化资源必返、允许null；不能用current填充。Admin运营DTO省略，与原具体方案一致。
- `current_version_id`、`edit_revision`、`has_unpublished_changes`：仅授权编辑者/管理者的editing或mine响应返回；current可null，edit_revision从1开始。版本化资源的has_unpublished_changes为current非空且与published不同；两者均空为false。Connector省略current_version_id，以完整editing内容及拟定受众与effective内容比较has_unpublished_changes，不比较两个独立计数器是否相等。公开详情不得泄漏草稿ID或编辑修订。
- `effective_revision`、`effective_digest`：Connector的有效内容必返；没有有效内容时，仅授权editing可见并以null表示。
- `manifest_json`、`plugin_json`：详情返回；列表保留既有轻量manifest投影，不返回Package。字段内容与view选根一致，不能名称是current、Package是published。
- `dependency_availability`：仅A01 `mode=mine`中的Expert/Team必返，供作者诊断；其他类型及公共市场列表省略。独立于listing_state/display_status，不改变发布、审核或上下架状态，结构及安全规则见§2.2.1。

Detail=`{plugin:PluginProjection,relations:RelationWire[],relation_result?:{created:string[],updated:string[],deleted:string[]}}`。Connector relations为空。Detail 的 relations 只表达来源自己的关系集合（目标 ID 与可选审计版本）；目标的当前解析版本由 A03/A34/A10 表达，不在 Detail 里伪造 pin。

Version=`{plugin_id,version_id,revision_no,version_label,declared_visibility,is_released,is_published,manifest_json,plugin_json,relations,files,content_digest,created_at,created_by}`；`files:[{path,mime_type,content_size,content_hash}]`不含内部key。VersionSummary明确省略manifest_json、plugin_json、relations、files，只保留身份/序号/标签/受众/状态/摘要/创建信息，不能每行返回完整关系/文件图。is_published为当前发布，is_released为曾正式发布。历史读取受众取当前有效范围与该版本declared_visibility交集，后来公开不追溯公开旧私有历史；不明历史仅owner/Admin按既有权限管理读取。A09 restore 已删除，因此 Version/VersionSummary 不再有 `restored_from_version_id`。

A34完整内容读取已确认，沿用既有鉴权、可用性和历史受众检查，不增加安全状态或专用取证入口。A02/A03供详情/轻量图展示，A05～A08供独立历史/文件工具；不得用“先查A03，再按目标ID/版本逐个补正文/文件”替代一次完整专家内容查询。

#### 2.2.1 A01：公共市场过滤与“我的发布”依赖诊断（B 语义）

继续使用`GET /market/api/v1/plugins`，不新增接口或依赖检查query开关。公共列表以来源**当前正式发布版**的 relations 为关系集合（不用 current/草稿替代），逐条按 B 解析目标最新发布版；只展示自身符合原上架/权限条件、且完整最新图均可用的资源：任一 Skill、Connector 或成员 Expert 的最新发布版下架、删除或缺失，依赖方不出现在列表；Team 递归为 Team→最新成员 Expert→该 Expert 的最新 Skill/Connector，不能只查直接关系。**不得用新草稿/current 替代目标的正式发布版，也不得在最新版不可用时回退旧版。**

该条件在SQL分页、排序及COUNT之前生效；`pagination.total`、搜索/分类筛选、`GET /plugin_categories`的plugin_count与公共`GET /plugin_tags`复用同一元数据可用性判定。只查refs、版本和资源状态，不逐条调用A34或读取内容文件。目标缺失不能因INNER JOIN丢边而被当成“无依赖”。

分类接口仍返回既有active配置行，不因plugin_count=0而新增隐藏规则；前端是否隐藏空分类保持原行为。完整性检查与公共COUNT/列表/分类/tags使用同一数据库一致性快照：将relations_json的relation_id/type/target_plugin_id经JSON_TABLE投影，与refs双向比对（含source_plugin_id归属），并核对数组类型及数量。合法空数组与零refs可用；版本缺失、非数组、漏/多refs或等数量错target均500 relation_index_inconsistent，不得先被依赖不可用过滤吞掉；refs一致但目标Plugin缺失才按正常不可用处理。`target_version_id` 只是可空审计列，不参与图解析，缺失或与本次最新解析版本不同都不算不一致。

`mode=mine`保留原owner/Space及自身未删除条件，包含原有草稿、待审、已发布、下架等资源；**不得因依赖异常删除行或减少total**。mine缺省及唯一允许的`view`为`editing`；显式`mode=mine&view=published`返回400 `VALIDATION_ERROR`（`details.field=view`），不能忽略参数、隐藏从未发布的草稿或回退current冒充published。公共模式缺省及允许的`view`为`published`；仅`plugin_type=connector`可显式使用`effective`，响应按有效态归一。公共模式显式`editing`同样400；非法组合统一400且 `details.field=view`。Web“我的发布”的全部/专家/专家团列表显式请求`mode=mine&view=editing`，保留编辑投影及原操作入口，不增加回收站或已删除来源展示。mine标签统计与A21 Admin管理列表也不得套用公共市场的依赖失效隐藏条件。

每个mine Expert/Team返回`DependencyAvailability={status:"available"|"unavailable",evaluated_view:"published"|"editing",evaluated_version_id:string,reasons:DependencyReason[]}`：

| 字段 | 规则 |
|---|---|
| status | 只表示本次解析图（来源评估版本及全部浮动依赖的最新发布版）的元数据可用性；available时reasons为空，unavailable时至少一条原因。不代表根自身已上架、A34文件完整或Fleet安装成功 |
| evaluated_view | 指**来源自己**的视图：来源存在published_version_id时必须为published，即使本行展示editing草稿；只有从未发布的来源才以current草稿评估并返回editing。reasons 始终基于该评估版本自身的 relations，不用展示行的草稿关系替代 |
| evaluated_version_id | 与上述评估根严格一致的UUID，必填；不能用草稿修复状态覆盖线上版本诊断。应有版本/refs缺失或数据库失败按既有完整性/服务错误处理，不伪造available |
| reasons | `DependencyReason={code,target?:DependencyTarget,via?:DependencyTarget[]}`；code仅 dependency_delisted／dependency_deleted／dependency_unavailable；`DependencyTarget={plugin_id,plugin_type,plugin_name,version_id?}`，类型为skill/connector/expert，Connector省略version_id |
| target.version_id | 本次解析到的最新目标发布版本；目标从未发布、已删除或无权确认时省略；不是关系里的审计 target_version_id |
| target/via | 仅当目标及整条中间路径均可按既有授权读取时返回。直接原因省略via；Team的间接原因via给根与目标之间的**最新成员 Expert**（含本次解析版本），不包含根和目标本身 |

无法查看目标、目标缺失无法确认、或已删元数据不可授权读取时，合并为一条`{"code":"dependency_unavailable"}`；不返回隐藏ID、名称、类型、版本、路径或隐藏数量，不通过旧缓存/快照名称绕过现有权限，也不暴露它究竟下架还是删除。可见的多个原因可以分别返回；普通下架/删除依赖是正常诊断数据，mine仍成功返回该行；真正查询/完整性故障不吞成正常原因或空数组。

B 下典型返回（含“审计旧版本已下架但最新版正常”不报错）：

| 场景 | 返回 |
|---|---|
| E 已发布 v1，其依赖 S 的最新发布版被下架 | `status=unavailable, evaluated_view=published, evaluated_version_id=E-v1, reason=dependency_delisted target=S 最新发布版`；Web 显示“线上发布版异常”，不是“已恢复” |
| E 从未发布，current v1 的依赖 S 最新发布版被下架 | `status=unavailable, evaluated_view=editing, evaluated_version_id=current-v1, reason=dependency_delisted target=S 最新发布版`；Web 显示“草稿依赖异常（尚未发布）” |
| E 已发布 v1，关系里的审计 S-v1 已下架，但 S 最新发布版 S-v2 正常 | `status=available, reasons=[]`（审计值不参与解析，B 下不报错） |
| 你无权看 S（或整条 via 中任一项无权） | `status=unavailable, evaluated_view=published/editing, reasons=[{"code":"dependency_unavailable"}]`，无 target/via/数量 |

以下是同一mine列表行新增字段的两个片段，不是新增API包络。可授权的下架依赖（`target.version_id` 为最新解析版本）：

```json
{
  "dependency_availability": {
    "status": "unavailable",
    "evaluated_view": "published",
    "evaluated_version_id": "00000000-0000-4000-8000-000000000101",
    "reasons": [
      {
        "code": "dependency_delisted",
        "target": {
          "plugin_id": "demo-skill",
          "plugin_type": "skill",
          "plugin_name": "部署检查",
          "version_id": "00000000-0000-4000-8000-000000000102"
        }
      }
    ]
  }
}
```

无权或无法安全确认具体目标时，只返回匿名原因：

```json
{
  "dependency_availability": {
    "status": "unavailable",
    "evaluated_view": "published",
    "evaluated_version_id": "00000000-0000-4000-8000-000000000101",
    "reasons": [{"code": "dependency_unavailable"}]
  }
}
```

Web以固定code映射中英文i18n，不由服务端返回任意HTML或整句文案。unavailable显示独立“依赖异常”标记，hover/键盘focus的Tooltip说明原因；保留原发布/审核标记和操作。published提示可用“Skill「部署检查」的最新发布版已下架，当前发布版本暂不在市场展示”；间接原因说明成员专家路径；匿名提示“存在已失效或不可访问的依赖，请检查依赖配置”。editing提示必须写“草稿依赖异常”，不声称该草稿已从市场隐藏。全部tab经getMySkills/MineRow/MineTable及专家/专家团tab均须展示，不仅修改市场卡片。

不改变来源listing_state，不持久化新治理状态；依赖最新发布版恢复合法上架，或来源发布**移除/替换该依赖的新版本**后，下一次列表查询重算。只保存修复草稿不能清除已发布版本异常；来源发布新版本后，公共市场按新版本的 relations 重新解析，旧版本仍按自身 relations 诊断。沿用既有刷新/缓存失效，不新增推送/轮询。前端不能过滤mine异常行或为标记/Tooltip逐卡请求依赖、A34或完整文件；缺少必需诊断字段不能默认为available。

### 2.3 Write / Publish / Review

发布审核职责、owner提交、User/Bot鉴权、Space自动审核开关及管理权限均不变。自动/人工批准执行同一“最新解析图”校验及发布隔离；**审核只保护提交到批准窗口，批准后依赖继续跟随最新，不冻结未来内容**。**ZIP-only仅适用于Admin Expert/Team的内容、内容元数据与依赖写入（A25）；Tenant Expert/Team仍使用A12完整Write+relations。** A24拒绝Admin Expert/Team创建/更新，评分及placement保持原专用入口，不新增metadata PATCH旁路或保存/发布矩阵。**Connector 例外：Admin 系统 Connector 的既有直接生效写入保留（同步更新 effective，属既有平台操作）；Tenant Connector 编辑必须审核；Expert/Team 审核观察 effective。**

Write 保留现有 `{plugin:{plugin_id?,plugin_name,plugin_type,visibility,version?,manifest_json,plugin_json,category_id?,tags,publisher?,icon?},relations:[RelationWire],expected_edit_revision?:integer}`。

- plugin_name/type/visibility/Manifest/Package/relations显式必需；其他已有可选元数据按原语义保留。新建省略plugin_id和expected_edit_revision；更新二者必填（Admin path ID可作为plugin_id，但body如有必须相等）。缺失/过期 `expected_edit_revision` 为 409 `edit_conflict`；`relations` 缺失/null 为 400 `relations_required`。
- `version` 是候选SemVer标签，更新省略沿用current标签；正式发布时与release验证，不靠保存序号判断。Connector仅保留原合法展示标签兼容，无版本选择语义。
- `relations:[]`明确清空；不是PATCH语义。返回Detail。
- owner/Space不可写；Tenant写入visibility是编辑拟定受众，保存到declared_visibility，不立即改变线上visibility。发布时按候选图校验outbound并检查incoming后原子切换；Admin更新的归属/范围保持原值。
- relations 存目标 `plugin_id` 与可选审计 `target_version_id`；保存只记录目标身份，不把依赖版本写死。依赖最新发布版在校验、A34、安装时解析。

同一content_digest不能直接判为重复保存：候选version_label、declared_visibility或完整RelationSnapshot身份变化也生成新不可变Version；只有上述全部冻结输入一致才可复用current。content_digest只由服务端统一codec生成，客户端视为opaque并原样传递，不自行用JSON.stringify或Go默认编码推导；它不等于公开可重算的A34 bundle_digest。**浮动关系的审计 target_version_id 不进入 content_digest；目标发新版不改变来源版本摘要，也不要求来源发新版本。**

**A12的既有Skill包导入：**`POST /plugins/import`保留JSON `{parse_task_id,plugin_id?,plugin_name?,name?,description?,category_id?,tags?,visibility?,icon?,version?,changelog?,expected_edit_revision?}`及200 `{data:Detail}`。新建必parse_task_id和明确plugin_name，省略plugin_id/CAS；更新必parse_task_id、plugin_id、expected_edit_revision，其他元数据省略沿用原值，category_id可显式null清空，tags显式[]清空。name仅为既有别名，同时给plugin_name时不允许值矛盾。parse task须属于当前UID/Space、成功且未消费；完整SKILL.md、原ZIP及支持文件由服务端保存，不能只用readme重建Package。该请求不接relations，Skill最终关系为[]；成功生成editing快照及selected/current/version_label/edit_revision，不自动发布。CAS或持久化失败释放task消费权且保留旧版本/文件；前后并发和消费必须与内容提交一致。A14不再接parse_task_id，审核必须在保存后提交准确version_id。MP-01负责该既有端点及OpenAPI扫描/生成，MP-02不另造上传协议。

版本化Publish=`{plugin_id,version_id,version,changelog?}`，version必须等于指定快照version_label，version_id必填，不猜current；**version_id 选择来源自己的待发布快照，不是依赖 pin**。发布事务内按 B 解析完整最新图；任一依赖最新发布版不可用返回 409 `dependency_unavailable`，不回退旧版、不删边、不静默替换。Connector Publish=`{plugin_id,expected_edit_revision,changelog?}`，不传version_id或业务version。提交审核同样使用确定版本或Connector编辑修订，防并发保存后发布错内容。

PublishResult保留`{plugin_id,listing_state,display_status,review_id?}`，Tenant版本资源另返published_version_id；Admin不返该指针。A13沿用既有成功HTTP 200，以listing_state/display_status/review_id区分待人工审核与已发布，不新增202分支。Space按现有策略自动批准或进入人工审核：**默认自动审核**（无 Space 配置时 `IsAutoApproveEnabled=true`），自动路径提交→解析最新→批准，若解析/批准间并发变化则重新解析并重试一次，正常不把 409 暴露给用户；Space 显式关闭自动审核后进入人工审核。private既有直接发布不变，同 owner private→private 依赖允许，但不扩大受众。

Review提交=`{plugin_id,version_id,changelog?}`；Connector为`{plugin_id,expected_edit_revision,changelog?}`，不另传Manifest/Package/relations/parse_task_id。approve/cancel空body，reject=`{reason}`；实际批准复用唯一发布事务。A15保留现有review_id/plugin_id/space_id/target_scope/status/kind/version/changelog及申请者、审核者、自动批准/决策来源和时间字段；以下是明确的详情增量，不在队列列表逐行加载重内容：

- `source_version_id`：版本化资源必返；`source_edit_revision`与`frozen_content_digest`必返；Connector省略source_version_id和业务版本字段。
- `frozen_version:Version`：版本化源的完整冻结版本（**只冻结来源自身内容**），Connector省略；`frozen_connector:{manifest_json,plugin_json,declared_visibility}`仅Connector必返。两者清除内部对象key，正文来自冻结内容，不向current详情扇出补齐。
- 保留详情`readme_content`及`frozen_relations:RelationSnapshot[]`，与冻结Version完全一致；合法无关系返[]，快照缺失/解析失败500 relation_index_inconsistent，不能沿用旧decode失败吞空。详情失败不改变审核状态，仍可按已有权限拒绝/取消。
- `resolved_dependency_observations:[{plugin_id,plugin_type:"skill"|"expert",version_id}]`：提交时解析图中全部 Skill/Expert 目标（不含来源根，Team 含成员 Expert 及其 Skill）的**当前最新发布版本**，按 plugin_id 去重；仅观察值，不是 pin，不冻结批准后的内容。Connector 仍由 `connector_observations` 单独表达（D7）；两者合起来覆盖全部跟随依赖。
- `connector_observations:[{plugin_id,effective_revision,effective_digest}]`记录解析图全部 Connector 当前 effective；Connector自身提审也记录其提交时effective基线，首次无有效行可为null/null。仅该首次根允许null。
- 批准时重新解析：人工审核窗口内任一 `resolved_dependency_observations.version_id` 或 `connector_observations.effective_*` 变化返回409 `dependency_changed`，由有权取消者取消旧申请，再由有权提交者重新提交并生成新的 review_id；审核人查看新申请后批准；自动审核内部重试一次，不把日常 409 暴露给用户；重试后仍变化则不发布并返回409 `dependency_changed`，不静默批准。**只改 editing 不误判；批准后依赖继续浮动，Skill/Expert 发新版不要求重新审核引用它的 Expert/Team。**
- `baseline_version_id:string|null`及`dependency_diff:[{change:"added"|"removed"|"changed",before?:RelationSnapshot,after?:RelationSnapshot,before_version_label?,after_version_label?}]`在提交时按当时published根冻结，Connector的baseline_version_id为null、关系diff为空。added仅after、removed仅before、changed二者皆有且确有 target ID／type／sort_order／data 等变化；按relation_type+target_plugin_id对应边。**审计 target_version_id 与本次最新解析版本不参与 diff**；目标可在审计字段附提交时真实version_label（仅审计），Connector不附业务标签。基线、diff和观察值由服务端存入审核快照，不随current或后来published漂移；列表省略这些重字段。沿用审核读取授权和历史受众规则，无权依赖不泄漏身份、缺图不伪造完整预览。

**人工审核冲突恢复（不新增端点）：** 409 `CONFLICT` / `details.reason=dependency_changed` 不修改旧 pending 申请或冻结观察值，GET 刷新也不更新基线。有权取消者使用既有 cancel 取消旧申请；取消与批准竞争按 pending CAS 仲裁，若已批准则读取原结果，不继续代提。有权提交者随后使用既有 submit 生成新的 review_id 和新观察值。Skill/Expert/Team 可重新提交同一不可变 source_version_id，仍须通过当前权限、标签、发布基线及依赖资格检查；不自动改用 current 新草稿。Connector 提交必须携当前 expected_edit_revision，重新冻结当前编辑内容，若编辑已变化须展示并明确重新提交，不冒充原冻结内容。旧记录永久保留；新记录进入原自动/人工策略；批准空 body 不变。客户端不得自动取消、重提或连续重试 approve。迁移 run 的原 review 被取消后保持 blocked，不自动换绑新 review；另行修订迁移计划和授权处理，不通过普通页面重提绕过迁移恢复约束。

### 2.4 Graph / Candidate / Dependents

Graph=`{root_node_key,nodes,edges}`。**依赖节点都是本次请求按 B 解析到的最新正式发布版；节点版本不是关系 pin，下一次请求可能不同。** A03 显式选择历史根时，根节点是所选根自身版本，依赖仍按最新解析；A34 不提供根版本选择。

- VersionNode=`{node_key,plugin_id,version_id,plugin_type,version_label,plugin_name,manifest_json,files}`；ConnectorNode=`{node_key,plugin_id,plugin_type:"connector",plugin_name,manifest_json,effective_revision,effective_digest}`，其manifest_json来自本次选定完整effective行，不读editing。
- node_key为版本化plugin_id@version_id或Connector plugin_id@effective_revision；VersionNode的files为`[{path,mime_type,content_size,content_hash}]`，ConnectorNode不承载文件清单，其ContentAsset.files必须[]。节点ID唯一；每条边的source_version_id等于source节点version_id，target ID及本次解析版本/有效修订等于target节点，root身份与根节点逐字段一致。所有节点从根可达，不能塞入无边的图外资产；同一依赖插件在一次解析中只出现一个最新版本，不按名称合并、也不并列旧版本。
- 图统一最多1500个节点（含根，按解析后的资源＋版本/有效修订去重）、2000条边；A03与A34使用同一计数规则。Marketplace原detail_graph的630子节点限制和旧install的500目标限制须在对应新解析图路径更新，不能误把Connector新增节点当成已兼容；Fleet现有30成员/600 Skill及其他限制不变。读取边/节点时增量计数，超限413 budget_exceeded，不能全量构建后裁剪。
- edge=`{relation_id,source_node_key,target_node_key,source_version_id,target_plugin_id,target_version_id?,resolved_effective_revision?,relation_type,sort_order,data}`；`target_version_id` 是本次解析到的最新版本（节点身份），不是关系审计值；Connector省略target_version_id并返回本次选定的resolved_effective_revision。所有端点必须在nodes，缺目标不能裁剪后200。
- A03的Graph是轻量版本图，files仅文件清单，不含正文；node_key不是读权限凭据。完整专家/团队查询直接取A34，其graph给本次解析身份及映射，assets已含全部实际内容，客户端不再补取Version/文件。独立历史工具仍可单独调用A05～A08，历史版本自身内容不可变，但其依赖仍按最新解析，不恢复旧依赖内容。

Candidate保留Plugin身份/发布者/scope，新增`published_version_id`、`version_label`（Skill/Expert **当前最新发布版**）、`effective_revision`（Connector **当前有效修订**）、`can_select`、`reason?:string`、`is_already_bound`。目标无权限根本不返回；有权限但不满足来源受众/状态时可返回安全的不可选原因。候选返回最新发布身份，但选择关系时**不要求也不承诺固定版本**；后续读取/安装仍按 B 解析。

Dependents为标准列表，每行`{source_plugin_id,source_version_id,relation_type,target_plugin_id,target_version_id?,is_indirect,via?:[{plugin_id,version_id}]}`，另有顶层`meta:{has_hidden_dependents:boolean}`。集合是删除保护的live边＋published直接/间接路径并集：live使用该来源current_version_id，published使用真实published根；中间 Expert 及最终目标都按 B 解析最新发布版（不是固定中间版本）。source_version_id指路径根版本，relation_type是最终指向被查询目标的边类型，target_version_id是本次解析到的最新目标版本（观察值），不是 pin。相同根版本、最终边、目标 plugin_id、完整 via 解析身份只返一次，不同路径保留；按source_plugin_id/source_version_id/is_indirect/via/target_plugin_id/relation_type/target_version_id稳定排序后分页。目标是Connector时省略target_version_id；直接行省略via。仅完整授权路径进入data及total，隐藏路径仅置has_hidden_dependents，不返回其ID/数量，不能对隐藏路径分页计数。

## 3. Marketplace 接口清单

表内ID用于issues/manifest关联。未列出的分类、评分、解析任务、Space审核策略等既有能力保持原协议，除内容读取必须遵循统一投影。operationId保持现有者不因本文重新命名；新增者在表中给出。

### 3.1 读取和历史

读取沿用现有权限，并补准确的正式/编辑版本选择与 B 最新解析。A09 restore-to-draft 已删除（D2），不提供“历史还原为草稿”动作；历史读取 A04～A08 保留，历史版本自身快照不可变，但其依赖读取仍按最新解析，不伪造 release/pin。

| ID | 方法/路径 | 请求 | 返回/权限 | 所属issue |
|---|---|---|---|---|
| A01 | GET /plugins | scene_code必填；plugin_type公共必填、mine可省；保留category_id/tag/q/mode/sort/page/page_size，新增view；mine缺省且仅editing；公共缺省published，Connector可显式effective；不合法组合400 `details.field=view` | 公共按最新图在COUNT/排序/分页前过滤；mine保留原非删除行及Expert/Team四字段诊断；Admin列表不套用公共过滤 | MP-01底座；MP-02判定扩展 |
| A02 | GET /plugins/detail | plugin_id、include_relations默认true、view默认published | Detail；editing只owner/有权管理者，Connector默认effective；relations 为目标ID＋可选审计版本，不伪造依赖pin | MP-01 |
| A03 | GET /plugins/detail_graph | plugin_id、view默认published；可给version_id选择根自身已发布版本（历史展示），与view=editing互斥 | 轻量Graph；依赖节点为本次最新解析，根按version_id或published，不含正文；历史根仅有权且曾发布 | MP-02 |
| A04 | GET /plugins/versions | 保留plugin_id/page/page_size；scope=released默认；scope=all只owner/管理者 | VersionSummary列表；Connector400 unsupported_type | MP-01 |
| A05 | GET /plugins/{plugin_id}/versions/{version_id} | path IDs | Version，所属检查；普通人只能正式版本；operationId=plugin.version.get | MP-01 |
| A06 | GET /plugins/skill_md | 保留plugin_id；可选version_id，不给时取published | `{content,version_id}`；仅Skill，不能取current兜底 | MP-01 |
| A07 | GET /plugins/download | 保留plugin_id；可选version_id，不给取published | Skill ZIP，文件缺失明确失败，不静默改取最新包 | MP-01 |
| A08 | GET /plugins/{plugin_id}/versions/{version_id}/files/content | query path必填，相对路径 | `{path,content,mime_type,content_hash}`，仅UTF-8文本；operationId=plugin.file.get | MP-01 |
| A10 | GET /plugins/dependency_candidates | source_plugin_id可选；新建必传source_type/source_visibility；target_type/q/page；existing来源可给拟定visibility但要验证权限 | Candidate列表（最新发布身份），非场景列表；operationId=plugin.dependency.list | MP-02 |
| A11 | GET /plugins/{plugin_id}/dependents | page/page_size | Dependents；只资源owner/有权治理者；operationId=plugin.dependent.list | MP-02 |

历史文本附件可由A08独立读取；不新增任意对象URL接口。Admin读Skill用A23 skill_md/download，Expert指令从A22 Version.plugin_json中的原始AGENTS或合法受控内容接口取得；不能读取storage引用自行下载。A03默认published；Connector的缺省市场投影规范化为effective，显式editing仅有权管理者，不能给Connector伪造version_id。完整专家/团队内容只在§6的A34一次取得，不用A03+A05～A08拼装。

### 3.2 写入、审核、生命周期

以下发布/审核/下架/删除均复用既有动作、权限与入口，只接 B 最新解析与引用完整性校验。不新增下架类别、治理状态或运营流程。删除保留既有 live incoming 保护，并补 published refs 及间接引用；不能因改用最新解析而放开原本阻止删除的草稿/live 引用。

| ID | 方法/路径 | 请求与语义 | 返回 | 所属issue |
|---|---|---|---|---|
| A12 | POST /plugins/upsert、POST /plugins/import | 前者完整Write+relations（CAS 必带）；后者Skill parse_task包导入，独立请求见§2.3；更新均CAS，只推进editing | Detail，包含实际selected/current版本与修订；Connector无版本 | MP-01底座；MP-02依赖接入 |
| A13 | POST /plugins/publish | Publish，显式version_id选择来源自身快照；锁内按最新解析校验依赖 | PublishResult；依赖最新版不可用409 dependency_unavailable | MP-01发布；MP-02规则 |
| A14 | POST /plugins/review_requests | Review提交，锁定来源自身version_id/Connector编辑修订；记录提交时最新解析版本 | 201 ReviewDetail | MP-02 |
| A15 | GET /plugins/review_requests 及 GET /plugins/review_requests/{review_id} | 保留既有过滤/分页/本人或Space审核者权限 | 列表/ReviewDetail（含resolved_dependency_observations/connector_observations） | MP-02 |
| A16 | POST /plugins/review_requests/{review_id}/approve | 空body；批准时重新解析最新图；人工窗口变化409 dependency_changed，自动模式内部重试 | PublishResult，重复同请求返回已批准结果不重新发布 | MP-02 |
| A17 | POST /plugins/review_requests/{review_id}/reject 或 /cancel | reject必需reason；cancel空body | `{}` | MP-02 |
| A18 | POST /plugins/delist | 保留原`{plugin_id,reason}`、鉴权与下架语义，不另设下架类别或依赖豁免；已发布资源收窄 visibility 的现有行为不变 | 原listing状态结果 | MP-02 |
| A19 | POST /plugins/delete | 保留原`{plugin_id}`和软删除；删除保护 = live incoming（含草稿、不过滤 listing）∪ published refs（按 target_plugin_id，含 Team→Expert→Skill 间接路径）；无保护引用的曾发布资源可删除，不新增“曾发布即禁止删除” | 原删除结果，不删除共享独立依赖 | MP-02 |

现有场景投放可见性接口原样保留；非默认场景可设is_visible，default场景现有禁止隐藏的约束不变。不将场景可见性扩成资产级隐藏状态，也不新增平台治理API。A09 不恢复；历史读取与既有附件保留。

### 3.3 Admin 保留接口及必要增量

Expert/Team沿用A25 ZIP上传/重传，清单是关系集合的权威（目标 ID；审计版本可选）。其余Admin写入保持原鉴权、归属、listing及内容生效策略，仅适配 B 最新解析、并发校验和完整关系，不增加独立发布/重新上架入口。

| ID | 方法/路径 | 请求与语义 | 返回 | 所属issue |
|---|---|---|---|---|
| A21 | GET /admin/plugins、GET /admin/plugins/{plugin_id} | 保留分页过滤；默认editing/current；仅Connector详情允许显式view=effective供依赖预览，沿原Admin鉴权；不套用公共依赖隐藏 | 原列表/Detail，不暴露published指针；Admin保留原范围行（mine 见 A01） | MP-01 |
| A22 | GET /admin/plugins/{plugin_id}/versions/{version_id} | 按 version_id 精确读取/归属检查，Admin鉴权；Connector禁止 | Version；operationId=admin_plugin.version.get | MP-01 |
| A23 | GET /admin/plugins/{plugin_id}/skill_md、/download | 可选version_id，缺省current，必须标实际version | 文本结果/ZIP；仅Skill，原缺省保持 | MP-01 |
| A24 | POST /admin/plugins、PATCH /admin/plugins/{plugin_id} | 仅Skill/Connector完整Write；PATCH必CAS。Expert/Team全部内容及内容元数据只A25，A24拒绝这两类（400 zip_only_required） | Detail；评分/placement走原专用入口 | MP-01底座；MP-02校验 |
| A25 | POST /admin/plugins/import、POST /admin/plugins/container_reupload/{plugin_id} | schema2容器，见§4；强制Idempotency-Key；重传必expected_edit_revision；返回最新解析观察值 | Detail + resolved数组；Connector不含version_id | MP-02 |
| A26 | POST /admin/plugins/skill_import、POST /admin/plugins/skill_reupload/{plugin_id} | 保留JSON parse_task_id、name/category/tags/version等；重传新增expected_edit_revision | Detail；Skill自身上传不因禁止bundled而删除 | MP-01 |
| A27 | GET /admin/plugins/{plugin_id}/dependents | 与A11同包络，Admin可跨Space；operationId=admin_plugin.dependent.list | Dependents | MP-02 |

**A29/A30 本期不实现。** `POST /admin/plugins/legacy_import/preview` 与 `POST /admin/plugins/legacy_import/apply` 不注册路由、不出现在 OpenAPI、不分配客户端、不进入本发布验收；编号保留仅为避免复用。生产迁移一律走 §5 的 CLI + 离线授权。Admin DELETE、rating、分类/场景/投放保留原入口与语义；只给现有删除保护补版本化引用。先前新建的治理及 Admin 上架接口从当前目录撤回，不作为前置任务，不重排其他编号。

## 4. 引用式容器与导入幂等

沿用具体方案schema2字段，不另造容器格式。multipart：file与container_schema_version="2"必填；version/category_id可选，重传expected_edit_revision为必填十进制整数。创建省version默认1.0.0，重传省略沿用current候选标签；内容/关系有变化且原动作直接发布时，复用已release标签409 version_label_conflict，要求明确更高标签，不自动bump。ZIP仅一个根expert.json或squad.json，不允许额外文件（尤其skills ZIP、mcp.json、expert/context.json）。两类公共字段如下：

schema2压缩multipart请求继续使用部署MaxArchiveBytes()（MAX_UPLOAD_MB）上限；解压仅一个清单且≤1MiB，最终AGENTS.md同样≤1MiB。保留路径穿越、重复路径、软链接和解压预算检查；不因ZIP只有引用而跳过归档安全。§5 的 CLI preview 处理旧多文件包时沿原归档上限和完整文件校验，不误套schema2单文件限制。

| 字段 | schema2规则 |
|---|---|
| schema_version / kind | 必须分别为字符串"2"及agent/squad，与文件名和multipart一致 |
| name | 必填，trim后非空且UTF-8≤128字节 |
| summary / description | 至少一个trim后非空，优先summary，否则既有description别名；最终摘要UTF-8≤512字节，不截断 |
| tags | 可选字符串数组，trim、去空及去重后≤20项；缺省[]，不是页面覆盖字段 |
| instruction | 两类均必填非空UTF-8文本，≤1MiB；生成该来源唯一AGENTS.md，Team不能沿用旧leader/strategies渲染器丢掉instruction |
| skills / connectors | 仅agent，显式数组各≤20；skills每项仅`{id,version_id?}`、connectors每项仅`{id}`；`version_id`为可选审计值（提供时须属于目标且有release，不参与解析），Connector永不带版本 |
| members | 仅squad，1～30，每项仅`{id,version_id?,member_key,role,is_leader,sort_order?}`；`version_id`同为可选审计值；sort_order缺省数组下标，其他wiring遵守§2.1及技术§5.1 |

未知字段、重复JSON键、内联能力及旧Team的leader/strategies/dependencies/permission均拒绝；旧结构返回400 legacy_container_format引导显式迁移，不静默丢弃。明确是schema2但字段错误使用400 VALIDATION_ERROR并指清单路径，不因严格解码重写原始指令字节。

```json
{"schema_version":"2","kind":"agent","name":"示例专家","summary":"引用市场Skill的专家","instruction":"示例指令","skills":[{"id":"skill-id"}],"connectors":[{"id":"connector-id"}]}
```

```json
{"schema_version":"2","kind":"squad","name":"示例团队","summary":"引用市场专家的团队","instruction":"团队指令","members":[{"id":"expert-id","member_key":"lead","role":"负责人","is_leader":true,"sort_order":0}]}
```

Expert的skills/connectors显式数组可为空；Team members为1～30。ZIP用id是原具体方案的领域格式，HTTP Relation仍target_plugin_id；只有容器parser负责转换，不要求改通用Lib。`version_id` 若给出只作审计，**不进入 content_digest、不作为读取/安装依据**；与最新解析版本不一致时不报错、不改变解析，只有不属于目标或未发布才 400 `dependency_version_mismatch`。重传按relation_type+target_plugin_id保留relation_id，关系集合/data/角色/Leader/顺序变化才计入新内容摘要；目标发新版不计入。不能接受UI的relations/manifest覆盖字段。

A25返回`{data:{plugin,relations,relation_result?,resolved:ResolvedDependency[]}}`，Detail基础字段保持不变。`ResolvedDependency={source_plugin_id,relation_id,relation_type,target_plugin_id,target_version_id?}`，每条最终直接关系恰好一行，按sort_order/relation_id稳定排列，无依赖为[]；`target_version_id` 是**本次导入解析到的最新目标发布版本（观察值）**，Connector完全省略；不写回关系权威、不作为后续读取/安装依据。不内含目标名称/配置或审核观察值，不把resolved误作完整图；Admin名称/正文预览使用A21有效Connector或A22 Version。

导入key唯一域为actor＋operation＋target_plugin_id（新建固定new）＋key，摘要覆盖原ZIP及schema/version/category/CAS。先核验当前Admin权限、查已提交receipt，再执行首次CAS/依赖校验；同key同输入回放原Detail+resolved（resolved按首次记录原样回放，不因最新依赖变化重算），不因自己的提交已推进修订而重做旧CAS。不同输入409，不能再创建或补当前内容。receipt与结果同事务，7天完整回放后保留tombstone拒绝复用；对象先校验上传，失败只清理本次新建且无引用对象。

## 5. 生产迁移：CLI + 离线授权（不做 A29/A30 HTTP）

旧市场资产内嵌的 Skill 及 Expert（含专家团成员及其内嵌 Skill）拆成独立、市场可见资产，原依赖方改为目标 `plugin_id` 引用（B 下读取/安装解析最新，审计版本可选）。按所属市场来源的 owner 及既定 system/Space 受众、现有发布审核和列表规则展示，不能仅去掉 embedded 关系却继续隐藏目标或创建 private 依赖。**生产执行必须由持有离线授权的操作者批准冻结计划；CLI 不是绕过授权。** 不新增生命周期规则或 HTTP API。

**传输方式（D5）：** 本期生产迁移使用 `marketplace-stock-migration` CLI + 离线授权，**不实现 A29/A30 HTTP**。CLI 与领域服务复用同一转换/校验实现及同一计划 schema；A29/A30 编号保留但不注册路由、不分配客户端。任何自动 apply、猜测映射或绕过批准的路径都不得作为实现捷径。

**来源身份：** 数据库来源为 `plugin_id@version_id`，并核对 `expected_edit_revision` CAS；ZIP 来源为原始字节 SHA-256（CLI 支持 ZIP 时必须显式，本期可只支持数据库来源，ZIP 另行授权）。不得凭名称/内容相似自动选目标，也不得把旧 pin 当权威。

**两次只读 preview（冻结摘要）：**

1. `preview --discover`（discovery）：完整枚举来源 inventory、建议候选、关系及缺失选择；只读，不写业务对象、审核、receipt、映射或 OSS；`can_apply=false`，不把建议或唯一候选当批准。
2. `preview --freeze --proposed-plan <file>`（frozen）：操作者补全显式标签、映射、Connector server 分组、归属/受众后再次 preview；preview 可建议 `patch+1` 标签，但必须写入冻结计划并由人批准，apply 不自行换标签，标签冲突重新 preview。CLI 重取同一来源并核验完整映射、拓扑、标签、归属/受众及文件，输出冻结 `plan.json` 与 `plan_digest`；任何无法确认项仍 discovery、不可 apply。修改任意映射/分组/范围/关系/标签必须重新 freeze，**不允许手改冻结计划后沿用摘要**。

两次 preview 均只读数据库与对象存储；`MigrationPreview` 与 `MigrationPlan` 是 CLI 输出/输入文件格式，不是 HTTP 端点。

`MigrationPreview={phase:"discovery"|"frozen",input_digest,source_kind,source_identity,source_scope,source_plugin_id?,source_version_id?,expected_edit_revision?,resources:DiscoveryResource[],relations:MigrationPlanRelation[],exceptions:[{source_node_key,reason,can_retry}],can_apply,plan?:MigrationPlan,plan_digest?:string}`。只有frozen可同时给plan/plan_digest及can_apply=true。DiscoveryResource为`{source_node_key,plugin_type,source_content_digest,plugin_name,proposed_owner_id,proposed_space_id,proposed_visibility,suggested_targets:[{plugin_id,version_id?,version_label?}],mcp_server_keys?}`；候选均经授权，Connector不自动给候选或版本，最多输出server身份/脱敏descriptor fingerprint，不含配置秘密。

`MigrationPlan={schema_version:"1.0",input_digest,source_kind,source_identity,source_scope,source_plugin_id?,source_version_id?,expected_edit_revision?,resources:MigrationPlanResource[],relations:MigrationPlanRelation[]}`。冻结plan使用以下完整字段，后续执行不能再凭当前名称/内容猜映射：

source_kind仅`plugin_version|zip`；数据库来源必须source_plugin_id/source_version_id/expected_edit_revision，ZIP必须省略。root的source_node_key和origin_node_key均为root；proposed_plan使用同一MigrationPlan字段形状，不含plan_digest/approval_reason，缺选择不得冻结。冻结前把mcp_server_keys按UTF-8字节序排序，资源及关系使用下述稳定排序。

| 类型/字段 | 必需性与执行规则 |
|---|---|
| MigrationPlanResource | `{source_node_key,origin_node_key,plugin_type,source_content_digest,action,owner_id,space_id,visibility,category_id?,version_label?,target_plugin_id?,target_version_id?,mcp_server_keys?,target_effective_revision?,target_effective_digest?}`；space_id允许null，其余省略规则如下 |
| source_node_key / origin_node_key | root恰好一个；其余为来源inventory的稳定节点。非Connector二者相同且每源节点恰好覆盖一次。合法MCP拆组可多条独立source_node_key指向同一origin_node_key，各组server key须无重复且完整覆盖原集合；除此不漏节点、不加图外能力 |
| action=create | 创建独立目标，省略target IDs。Skill/Expert/Team必明确新version_label；Connector省略版本标签/ID。源内容、名称、指令和文件由冻结origin输入确定，category_id显式给合法分类或null；不能用只有fingerprint的计划还原配置 |
| action=reuse | 必target_plugin_id；**不要求也不写死依赖版本**（B 读取/安装解析目标最新发布版），省略新version_label/category_id，不修改目标内容/归属。`target_version_id` 若给出仅为审计观察，apply 校验目标身份/资格而非该版本内容。Connector省略target_version_id，记录preview时的target_effective_revision/digest作为批准观察值，apply变化则plan_stale，不把它们写成关系pin |
| action=update_source | 仅数据库来源的root，target_plugin_id必须等于source_plugin_id，省略target_version_id，明确新version_label。生成同一Plugin的新引用式快照，不创建第二个根，不覆盖历史；ZIP根用create。禁止子节点以该动作改写共享目标 |
| owner/Space/visibility | 保持所属市场来源及已确认system/Space受众，不能靠人工plan扩大受众或改变owner；ZIP根采用已鉴权Admin允许的新建范围。每次由服务端核验，Admin可见不豁免依赖矩阵，private历史不能因本次确认被自动公开 |
| MigrationPlanRelation | `{source_node_key,target_node_key,relation_type,sort_order,data}`；两端必须在resources，类型/wiring依统一关系契约。父边引用create/update_source输出时从提交map取得真实Plugin ID（审计version_id可选）；reuse按target_plugin_id引用，Connector只ID。完整保留来源成员/Skill关系及原角色/Leader/顺序，禁止自动补latest、猜测映射或丢边 |
| mcp_server_keys | 仅Connector必填非空，明确来自origin的server集合；默认整组，不按URL/名称猜映射。批准拆组必须完整覆盖、无重复/额外能力，所有配置从同一已授权输入取得，秘密/来源不明先阻断 |

复用Expert时，其实际关系必须与计划中对应子节点的目标ID及Connector身份一致；**依赖版本按 B 最新，不要求冻结**。对应子节点仍为create且真实ID未知时，不能确认复用该Expert，可由操作者选择精确复用其既有子目标或新建Expert。不能为了匹配计划覆写共享Expert，也不把同内容不同目标身份偷偷合并。

摘要统一由服务端/CLI计算，客户端按opaque值回传：ZIP的input_digest与source_identity均为原ZIP字节SHA-256；数据库的source_identity=plugin_id@source_version_id，input_digest覆盖源图完整快照、文件原字节摘要及原归属。source_content_digest标识该源节点的原始内容，不冒充尚未分配目标ID的新Version摘要。source_scope=SHA-256(JCS({owner_id,space_id,visibility}))，取root并由服务端核验。plan_digest=SHA-256(JCS(MigrationPlan))小写hex；resources按source_node_key，relations按source_node_key/relation_type/sort_order/target_node_key排序，字符串用UTF-8字节序、序号用整数。摘要绑定每个新建标签、映射、server分组及关系；正式Version的content_digest在实际ID绑定后由唯一版本codec生成。

**apply（只认冻结计划）：** `apply --plan <plan.json> --plan-digest <digest> --authorization <authorization.json>` 必须同时满足：计划为frozen、摘要与重算一致、来源/归属/权限/输入未变、操作者持有离线授权。CLI只执行冻结计划，不自行换标签、不重选目标、不猜映射；摘要或输入变化返回 `plan_stale`，要求重新 preview/批准。数据库root首次切换还须CAS原source_version_id/expected_edit_revision；ZIP不伪造这两项。actor来自认证/运行记录，审计不能信任手工`approved_by`。

apply按 Skill/Connector → Expert → Team 的拓扑执行；root用`update_source`保持原Plugin ID，子节点create/reuse。新版本走现有发布/审核：Space默认自动审核可自动批准；人工模式为 `waiting_review`，不直接插入 release 绕过审核。`MigrationRun={run_id,status:"completed"|"waiting_review"|"blocked",plan_digest,mappings:[{source_node_key,target_plugin_id?,target_version_id?,step_state}],exceptions:[{source_node_key,reason}]}`；step_state仅`reserved|created|waiting_review|published|source_switched|blocked`，未提交target省略，Connector永无target_version_id。全部节点/来源切换、发布和**独立列表资格**核验就绪才`completed`；等待审核为`waiting_review`；已部分提交后失败为`blocked`，保留run/mapping并给出安全原因，不假报整体完成。迁移后的Skill/Expert必须在对应市场独立可发现；只有全部节点发布/列表资格就绪才completed。

**可恢复与幂等：** run以`(source_scope,source_identity,plan_digest,source_node_key=root)`唯一保留；同一冻结计划重跑恢复原run，不重复创建Version/提审，也不重做最初CAS。map的`source_commit_json={output_version_id,output_edit_revision,review_id?,frozen_content_digest}`在来源切换时同事务记录，标识自己的已提交输出。后续current新编辑不覆盖、不自动按旧计划重新套用；原冻结审核正常批准且输出已正式发布时可完成，不能因current已有新草稿误判失败；审核拒绝/取消保持blocked。新计划或输入改变必须重新preview+批准，不借旧map跨范围复用；恢复重验原review/release/effective及全部目标资格，不自动重提已拒绝审核。迁移不承诺多个目标/来源一个大事务，也不是`/_batch`。

**回滚（数据安全，D5/D10）：**

- 不删表、不删文件、不覆盖旧快照/历史版本；
- **不把 `current_version_id`／`published_version_id` 改回旧值**，不做指针回切；
- 需要停止新分发时用下架或部署隔离，不用指针回切；
- 内容恢复从旧历史生成新版本（forward-fix），走现有发布/审核；
- 保留 batch/mapping/attempt/audit，回滚动作可审计；
- 不恢复 legacy 执行器/旧入口，不重新产生 embedded/inline；回滚接受兼容二进制 + 新版本恢复（D10）。

以下为数据库Expert根更新、内嵌Skill新建的冻结`MigrationPlan`示例（CLI文件组件，不是HTTP包络）。来源摘要使用文档fixture，并非真实生产数据；按上述排序/JCS可核验`plan_digest=d1bedf7b9724c941503338ebf94bdee85bc2eb1610ee43b983f6b2755e9538f2`：

```json
{
  "schema_version": "1.0",
  "input_digest": "8899750166a83bdf613df738f61d617028a9e3e1f802e84d59ded2a882f64c97",
  "source_kind": "plugin_version",
  "source_identity": "demo-expert@00000000-0000-4000-8000-000000000101",
  "source_scope": "068efbd59f64782094bec61e08e0633f63b7aea760da24c27271c1e9fc1ed52c",
  "source_plugin_id": "demo-expert",
  "source_version_id": "00000000-0000-4000-8000-000000000101",
  "expected_edit_revision": 1,
  "resources": [
    {"source_node_key":"root","origin_node_key":"root","plugin_type":"expert","source_content_digest":"45e59bae59bf2f21a5aa71a7d0015e9c638d3f3e0369143809c62b0f4b592f6f","action":"update_source","owner_id":"demo-owner","space_id":"demo-space","visibility":"space","version_label":"2.0.0","target_plugin_id":"demo-expert"},
    {"source_node_key":"root/skills/0","origin_node_key":"root/skills/0","plugin_type":"skill","source_content_digest":"c2131fff66b00a0b446a94ebae9d11b59f28d7c113ad2878041bed0429150461","action":"create","owner_id":"demo-owner","space_id":"demo-space","visibility":"space","category_id":null,"version_label":"1.0.0"}
  ],
  "relations": [{"source_node_key":"root","target_node_key":"root/skills/0","relation_type":"expert_skill","sort_order":0,"data":{}}]
}
```

MP-04 负责 CLI、冻结计划/摘要、拓扑创建/复用、run 恢复、独立可见核验与数据安全回滚；MP-02 的 receipt 底座提供 operation 扩展。生产 apply 仍须离线授权；CLI 不得自动执行生产迁移。

### 5.1 离线授权制品与恢复契约

`apply --plan <plan.json> --plan-digest <digest> --authorization <authorization.json>`；`resume --run-id <run_id> --authorization <authorization.json>` 使用同一校验器。授权文件为 `{payload,signature}`，signature 为 Ed25519 对 UTF-8 JCS(payload) 的签名（base64url、无 padding）；不接受文件自带公钥或算法覆盖。payload 必需字段为 `{schema_version:"1.0",authorization_id,key_id,issuer_id,actor_id,environment_id,source_scope,source_identity,plan_digest,batch_id,issued_at,expires_at}`；ID/范围非空，时间为 UTC RFC3339，issued_at ≤ 当前时间 < expires_at；期限由签发者明确填写，无无限期默认。

可信配置由部署管理提供：environment_id、key_id→公钥/issuer_id、签发者可授权范围和启停状态、撤销的 key_id/authorization_id；不从 plan、授权附件或操作者临时参数导入信任。actor_id 必须匹配执行环境已认证主体，不能由 `--actor` 或手写 approved_by 声明；不能可靠解析主体或加载信任配置时拒绝写入。逐次验证签名、环境、范围、计划摘要、来源身份、操作者及有效期，不因是 Admin 跳过迁移执行授权。

authorization_id 唯一绑定一个批次和原 run（同冻结计划恢复返回同一 run），首次占用与 run 关联原子提交；不能用于其他环境、范围、计划或创建第二个 run。同 run 恢复仍重验当前权限、授权有效期及撤销状态，不重做已提交步骤。过期/撤销后暂停写入并保留原 run/map；重新签发的新 authorization_id 仅在 actor/environment/source/plan_digest/batch 完全匹配时绑定原 run，记录授权更替审计，不能创建替代 run。修改计划必须重新 freeze/批准，不能用换授权掩盖变化。

轮换时新增可信 key_id 并停用旧钥签发；历史公钥保留供审计，执行是否接受由启停/撤销状态决定。授权失效或不匹配使用 CLI reason `authorization_invalid` 并非零退出，既有 run 保持 blocked；未开始不得创建业务对象。日志仅记授权 ID、签发者、主体、计划摘要和安全 reason，不含私钥/凭据。CLI reason 不新增 HTTP 顶层错误枚举（R2）。只读 inventory/preview/verify 不因持有授权而获得额外读权限。

## 6. 安装与窗口内安全重试（客户端 → Marketplace）

| ID | 方法/路径 | 请求 | 返回 | 所属issue |
|---|---|---|---|---|
| A31 | POST /plugins/{plugin_id}/installations | 单个X-Workspace-Id、必填Idempotency-Key；JSON `{runtime_id,resource_name?,custom_env?}` | 200 `{agent_id}`或`{squad_id}`，保留现有命名 | MP-03 |
| A32 | POST /plugins/install | 原JSON `{plugin_id,workspace_id,runtime_id,custom_env?}`；必填Idempotency-Key，workspace_id必须UUID | 与原成功结果一致，调用同一原子引擎，不走legacy create/bind/delete | MP-03 |
| A34 | GET /plugins/{plugin_id}/distribution | path ID＋既有认证/Space上下文；无body，无Workspace/runtime/custom_env/幂等键，不接受根version覆盖 | 200 `{data:DistributionBundle}`；按B最新解析的完整原始内容，无分页/后续内容请求；operationId=plugin.distribution.get | MP-03 |

**A31/A32 都必须使用用户身份、必填 `Idempotency-Key` 和单个 `X-Workspace-Id` UUID，共用同一个 `InstallCoordinator`；A32 只保留 URL，不保留 legacy create/bind/delete 行为。** A31/A32输入归一化后相同唯一域；不能同一key通过换URL创建另一attempt。A32 的 Workspace header 与 body 均必填且必须相等，否则400。runtime_id UUID；resource_name最多128字符；custom_env为map<string,string>，最多100项、key最多128字节、单值32KiB、合计256KiB；key为1～200可见ASCII且单值。安装JSON沿用3MiB上限，无压缩body、禁止redirect；最终Fleet请求16MiB上限。

安装attempt只用于Marketplace本地关联与重试控制，不新增安装结果查询接口。**首次构建plan时冻结一次请求可见的完整结果：根当前正式发布版、全部依赖的最新解析版本、Connector effective、物化字节、命名、runtime和bindings**；记录首次可能发出Fleet请求的时间及不晚于其后24h的retry_until。attempt状态为`prepared|dispatching|succeeded|failed|unknown|expired`：仅`prepared`及窗口内`unknown`可按原plan重试；`succeeded`回放；`failed`不自动重试；超窗`expired`。已有attempt先按原用户/Space/Workspace及原输入核对，再决定是否需要构建首次plan；**重试不得因根发布、依赖发新版、Connector或其他live内容变化重建plan、重新解析、重选版本或修改出站请求**；仅这些正常发布变化不触发 409，原 key 继续原 plan；显式输入变化、节点下架/删除或权限失效按下文分别处理。

调用者须重新提供与首次相同的运行时Secret；Marketplace结合原plan重建完整请求，校验包含Secret的完整规范化请求一致后，才可在现有24h窗口内以原Fleet幂等键重试F01。Secret只在处理请求的内存中使用，不写入客户端本地记录、Marketplace attempt/plan/日志。仅有幂等键或不含Secret的plan不足以重发；应用重启后若无法重建原参数，不自动POST，也不承诺无凭据自动恢复。

已知SUCCEEDED先验证原caller/Space/Workspace访问及`intent_digest`（归一化root/Space/Workspace/runtime/resource_name，不含custom_env），可回放本地验证结果，不要求根仍上架或重填Secret；若本次显式给custom_env仍校验完整input_hmac。不同非Secret意图即使同key也409 idempotency_input_mismatch，不能回放另一根/Runtime的结果。回放是历史提交结果，不保证资源仍存在。无本地记录、后续4xx或资源名称均不能证明此前未提交。

**同 key、配置与依赖变化（用户本轮确认）：**

- 同 key 同输入网络重试只重放首次冻结 plan；上游根、Skill、成员 Expert 发新版或 Connector 正常更新 effective，不阻断重试、不重选版本。
- 本次显式输入（root/Workspace/runtime/resource_name/custom_env 等）与首次不一致 → 409 `idempotency_input_mismatch`，不重建 plan。
- 原 plan 节点下架/删除、权限失效等仍按资格校验拒绝；错误不是先前安装未提交的证明，已存在 UNKNOWN 时保留未知结果和原 attempt。
- 想使用最新内容必须新 key 新安装，首次按当时最新可用图冻结计划。新 key 不用于自动绕过未知旧结果，旧结果先核对。
- 审核窗口变化仍按 §2.3 返回 `dependency_changed`，与正常上游发新版后的原安装重试分开处理。
- **安装计数只在首次 `succeeded` 时增加一次**；同 key 回放、UNKNOWN、failed 均不计数。

POST准备失败按§8错误；dispatching竞争409 installation_in_progress；上游结果未知503 installation_result_unknown，details仅含安全attempt_id/retry_until/can_retry_same_key，不含Secret或原响应。can_retry_same_key只表示仍有窗口且能重建同完整请求，不承诺查询。超窗返回409 installation_attempt_expired，无法重建或输入不同时不重发。后续4xx或资源状态变化不能证明先前UNKNOWN未提交，必须保留未知并提示人工核对，不换key、删记录或fallback。POST成功带Cache-Control:no-store，只有明确回放证据才带Idempotency-Replayed:true。

### 6.1 A34：单请求完整专家与依赖内容（B 语义）

网关URL为`GET /market/api/v1/plugins/{plugin_id}/distribution`。此处的distribution仅指完整内容读取，不是安装、Fleet结果查询或市场运营动作。沿用现有用户/Bot读取鉴权和Space上下文；认证Token仍必需，但不要求运行时凭据。请求只有path plugin_id，无body，不要求Workspace/runtime/custom_env/Idempotency-Key，不提供历史根版本选择器或editing入口。

在同一个短只读一致性事务内，按 B 选定根的当前正式发布版、全部浮动依赖的最新正式发布版，以及全部 Connector 完整 effective 行（含revision/digest/payload）复制到内存，核对投影及完整有效内容摘要后退出事务，再读不可变OSS并校验。不得先记旧revision后晚读新配置。根或任一依赖的最新发布版不可用时整图失败，**不回退旧版**；根与依赖必须当前可发布/可访问且通过既有访问/上下架及历史受众检查。**A34 每次都重新解析，同一 Expert/Team 版本不同时间可能返回不同依赖内容与不同 `bundle_digest`；这是 B 的明确代价，不是错误、缓存失效或需要“补 latest”的失败分支。** 依赖不可用时 owner 只能在 A01 mine 看到诊断，不能通过 A34/安装绕过。Skill/Connector单根沿用现有本地入口，不新增产品操作。

`DistributionBundle={schema_version:"1.0",root:{plugin_id,plugin_type,plugin_name,selected_version_id?,effective_revision?},graph:Graph,assets:ContentAsset[],bundle_digest:string}`，包装在通用`data`中：

| 字段/类型 | 必需性及不变量 |
|---|---|
| root | 版本化根必有selected_version_id（根当前正式发布版）；Connector根必有effective_revision且省略selected_version_id；与graph.root_node_key对应节点一致 |
| graph | 本次请求按 B 解析的完整图与角色/Leader/顺序/绑定关系；节点按资源＋解析版本去重，Connector按ID＋有效修订区分，不按名称合并；同一依赖插件只出现一个最新版本 |
| assets | 每个graph node_key恰好一项，无遗漏、重复或图外项；`ContentAsset={node_key,market_name,files:ContentFile[],mcp_config?}` |
| ContentFile | `{path,mime_type,content,content_hash}`全部必填；path为该节点内的安全相对路径，content为完整原始UTF-8文本，content_hash为原字节SHA-256小写hex，不归一化换行 |
| files | Expert/Team仅自身AGENTS.md；Skill含SKILL.md及全部支持文件，完全对应graph文件清单的路径/mime/大小/hash。Connector固定[]，不额外返回mcp.json或缺清单文件 |
| mcp_config | 仅Connector必填，对应本次选中有效修订的完整公开声明/Secret占位符；其他节点省略，不含运行时秘密、不读Expert旧附件。服务端按完整effective payload验证effective_digest；客户端不从仅mcp_config伪重算整行摘要 |
| market_name | 必须等于对应graph节点plugin_name，来自本次解析的Version/effective投影，不读current草稿名，不用安装副本名替代 |
| bundle_digest | 排除自身后完整data按RFC8785 JCS计算SHA-256小写hex，无sha256:前缀。先将nodes/assets按node_key、edges按source_node_key/relation_type/sort_order/relation_id、files按path排序；字符串比较用UTF-8字节字典序，sort_order用整数比较。再执行JCS（对象key按其UTF-16规则），不依赖Go/JS默认JSON编码。**依赖最新解析变化会改变bundle_digest，这是预期行为。** |

同一节点内容只在assets存一份，通过node_key与graph关联；同一依赖插件在一次响应中只有一个最新版本，不会同时出现两个版本。`bundle_digest` 不承诺跨请求稳定，也不承诺同一 Expert 版本永久同一依赖内容。 A34 不生成 `installation_name`、不改写 `SKILL.md` frontmatter、不产安装副本，返回市场原名/原字节；读取成功不代表 Fleet 安装或同名共存成功。

读取复用最新解析 resolver、版本文件及对象完整性校验，不在服务端环回调用A03/A05/A08拼HTTP。现有capability_definition.go及capability_assets.go可复用文件读取/预算/校验能力，但先拆出不依赖Workspace/runtime/Secret的内容解析，不能原样暴露按ID/current或旧inline MCP的安装builder。远端安装在读取结果之后另行序列化为Fleet请求。

全量成功才200：校验所有节点/文件、无重复安全路径、UTF-8和摘要、有效配置及既有预算后才能发送成功。JSON响应总量最多16MiB（包含包络、元数据及转义后的UTF-8编码），沿用单文本1MiB、Skill支持文件50/全图500及图预算；超限413 budget_exceeded，不能分页、截断、跳文件或返回下载链接让客户端补齐。缺失/无权目标按既有安全404，依赖未发布/最新版不可用/状态不合法409，路径/类型/二进制400，文件或索引完整性故障500；不返回残缺图后200，不补latest、不回退旧版。

返回`Cache-Control:no-store`。本GET不调用Fleet、不创建安装attempt/业务资源、不改变市场发布状态；查询成功不能作为安装成功或共存支持证据。Buddy/Web/CLI完整内容入口一次A34取得全部数据，客户端只校验和消费，不再调用依赖detail/versions/skill_md/download/files补内容，不在A34失败后改用其他接口拼装。

以下是专家＋依赖Skill的完整示例。Skill 的版本是**本次请求解析到的最新发布版**，不是关系审计值；同一 Expert 版本稍后再次请求可能解析到更新版本，`bundle_digest` 随之变化。文件摘要与`bundle_digest`按上述规则可计算校验；Connector及Team按类型规则扩展，不是线上响应实测：

```json
{
  "data": {
    "schema_version": "1.0",
    "root": {"plugin_id":"demo-expert","plugin_type":"expert","plugin_name":"demo-expert","selected_version_id":"00000000-0000-4000-8000-000000000101"},
    "graph": {
      "root_node_key": "demo-expert@00000000-0000-4000-8000-000000000101",
      "nodes": [
        {"node_key":"demo-expert@00000000-0000-4000-8000-000000000101","plugin_id":"demo-expert","version_id":"00000000-0000-4000-8000-000000000101","plugin_type":"expert","version_label":"1.0.0","plugin_name":"demo-expert","manifest_json":{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"demo-expert","plugin_type":"expert","name":"demo-expert","description":"Demonstration expert","labels":[]},"files":[{"path":"AGENTS.md","mime_type":"text/markdown","content_size":68,"content_hash":"b520d8711e8ffc516af5115f40c4ff6e18286961f1a5fb372b39891eb55ab357"}]},
        {"node_key":"demo-skill@00000000-0000-4000-8000-000000000102","plugin_id":"demo-skill","version_id":"00000000-0000-4000-8000-000000000102","plugin_type":"skill","version_label":"1.0.0","plugin_name":"demo-skill","manifest_json":{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"demo-skill","plugin_type":"skill","name":"demo-skill","description":"Demonstration skill","labels":[]},"files":[{"path":"SKILL.md","mime_type":"text/markdown","content_size":99,"content_hash":"7d33231a4649f18dd3c82f6b3ba980fce8e89e1dda636f49bbc708681b912128"},{"path":"references/guide.txt","mime_type":"text/plain","content_size":58,"content_hash":"3fced32b8c9b36947d3304b4fdaa08ad2fab1c28dcdb86178e5c844617df2b14"}]}
      ],
      "edges": [
        {"relation_id":"demo-relation","source_node_key":"demo-expert@00000000-0000-4000-8000-000000000101","target_node_key":"demo-skill@00000000-0000-4000-8000-000000000102","source_version_id":"00000000-0000-4000-8000-000000000101","target_plugin_id":"demo-skill","target_version_id":"00000000-0000-4000-8000-000000000102","relation_type":"expert_skill","sort_order":0,"data":{}}
      ]
    },
    "assets": [
      {"node_key":"demo-expert@00000000-0000-4000-8000-000000000101","market_name":"demo-expert","files":[{"path":"AGENTS.md","mime_type":"text/markdown","content":"# Demo expert\nUse the resolved latest skill and its reference file.\n","content_hash":"b520d8711e8ffc516af5115f40c4ff6e18286961f1a5fb372b39891eb55ab357"}]},
      {"node_key":"demo-skill@00000000-0000-4000-8000-000000000102","market_name":"demo-skill","files":[{"path":"SKILL.md","mime_type":"text/markdown","content":"---\nname: demo-skill\ndescription: Demonstration skill\n---\nRead references/guide.txt before acting.\n","content_hash":"7d33231a4649f18dd3c82f6b3ba980fce8e89e1dda636f49bbc708681b912128"},{"path":"references/guide.txt","mime_type":"text/plain","content":"This reference belongs to the latest resolved demo-skill.\n","content_hash":"3fced32b8c9b36947d3304b4fdaa08ad2fab1c28dcdb86178e5c844617df2b14"}]}
    ],
    "bundle_digest": "63e4e7d2012d3407262301bbaf182dad4007f74c02cee4466877d291ffd42a5a"
  }
}
```

## 7. Marketplace → Fleet

### 7.1 原子安装主体保留

F01=`POST /v1/capabilities/install`，仅消费现有`{definition,bindings}`协议，不修改Fleet协议、实现或部署，不是Marketplace Relation API。

```json
{"definition":{"schema_version":"1.0","name":"marketplace:demo-expert","skills":[{"name":"demo-skill","description":"Demonstration skill","content":"---\nname: demo-skill\ndescription: Demonstration skill\n---\nRead references/guide.txt before acting.\n","config":{},"files":[{"path":"references/guide.txt","source":{"type":"inline_text","content":"This reference belongs to the latest resolved demo-skill.\n"}}]}],"experts":[{"name":"demo-expert","description":"Demonstration expert","instructions":"Use the resolved latest skill.","skill_names":["demo-skill"],"custom_env":{}}]},"bindings":{"experts":[{"expert_name":"demo-expert","runtime_id":"00000000-0000-4000-8000-000000000001"}]}}
```

示例为普通无命名冲突安装，使用市场原名/原字节。单Expert省略expert_team；Team为`{name,description,instructions,members:[{expert_name,role,is_leader}]}`，每Expert绑定相同runtime_id，custom_env属于expert而非bindings。成员按市场sort_order/relation_id输出，member_key及顺序映射仅留本地plan，不能加进Fleet JSON。Leader出站role="leader"，非Leader保留原合法role；市场/A34原role/member_key/sort_order不改。无MCP省略mcp_config，非空才发送，空mcpServers会被现有Fleet拒绝。Definition不新增市场ID/pin或Runtime绑定接口。

**命名与共存（D3）：** 本期**明确不允许同名/多版本共存**。同一个 Definition 内同名不同完整内容安装冲突失败；不生成 `mk1` 或其他稳定别名、不改写 `SKILL.md` frontmatter、不改目录名、不改 Fleet/Daemon。普通无冲突安装保留市场原名/原字节；Fleet 现有 `Definition.Skill.Name` 唯一约束和 workspace+owner+name 冲突行为保持不变，实际触发现有冲突就明确失败。未来若要做共存，需另立决策并补真实 Fleet→PostgreSQL→Daemon 联调证据，不得从本示例推导已启用。

### 7.2 消费既有24h幂等窗口，不改Fleet

Fleet现有F01以workspace+认证owner+key及规范化完整请求判定幂等，在既有24h窗口内，同键同请求可回放原结果。Marketplace使用原attempt_id作为Fleet幂等键；首次plan一旦冻结，任何重试都必须沿用该plan并由调用者重新提供相同运行时Secret，不能按当前根或依赖最新解析重建内容，也不能通过新key规避冲突。**同 key 不重新解析最新；上游正常发新版仍重放原 plan。显式输入变化或原节点资格失效才按 §6 拒绝；想使用最新内容则新 key 新 attempt。**

Fleet仍用其规范化请求SHA-256作为内部重复比较；Marketplace本地`fleet_request_hmac`只校验自己重建的完整出站请求，包括运行时Secret，不冒充Fleet归一化指纹，两种摘要不跨服务比较。HMAC和不含Secret的冻结plan不具备还原Secret的能力。这里“不落Secret”专指客户端本地记录与Marketplace的attempt/plan/日志，不改变Fleet既有运行态持有职责。

Marketplace只在自首次可能发出请求起不超过24h的保守窗口内允许自动重试；一旦超期，即使Fleet自身可能允许过期key重新占用，Marketplace也不得再次自动POST。本方案不改变Fleet过期重占行为、cleanup、数据库、receipt保留期限或任何其他实现，不承诺远端永久去重。

重试直接调用现有F01，不以先查询receipt为前提，不新增Fleet或Marketplace安装恢复GET，也无新增查询DTO/operationId。请求缺少原Secret、无法重建完整参数、输入不同、超过窗口，或安全重试后仍无法确认结果时，保留未知并明确提示人工核对；不得自动换key、删attempt或fallback安装。窗口内再次重试仍须满足原plan、同键及同完整请求条件。应用重启仅在仍有原plan、窗口和用户重新提供的相同完整参数时具备重试条件，不承诺自动恢复。

F01返回仍须执行§7.1完整性验证；有效窗口内不同请求的冲突沿用现有Fleet行为，不把所有409当同键冲突，也不将4xx当作先前未知请求未提交的证据。本地缺少attempt或当前资源名称相同/不同，都不能用于判断未知提交。REL-01只收集既有Fleet的真实联调证据；旧pending/UNKNOWN无法证明结果时保留未决并人工核对，不将其迁移成“未执行的新尝试”。

### 7.3 工作空间定制组合：直接消费既有 Fleet 资源能力

本节是职责说明和现有能力核验，不新增本次Marketplace接口或Fleet改造任务。公开Fleet服务已提供`POST /v1/skills`、`PATCH /v1/skills/{skill_id}`、`PUT /v1/experts/{expert_id}/skills`（全量替换）、`POST /v1/experts/{expert_id}/skills`（增量添加）；使用原用户/Workspace鉴权。

本地Skill推送时读取真实SKILL.md与支持文件，创建请求沿用`{name,description,content,config,files:[{path,content}]}`，其中content为SKILL.md正文；获得Fleet Skill ID后，以`{skill_ids:[...]}`绑定工作空间实例。只传本地目录路径无法让远端读取。全量PUT必须包含需要保留的既有绑定，不能误删其他Skill；同名或共享资源冲突不能自动覆盖，必要时显式创建实例使用的副本。

此链路没有市场发布、审核或private Skill步骤。市场下载后对实例追加本地Skill只是运行态定制，不改变已发布市场版本；将组合再发布市场必须另走独立Skill正式发布和Expert引用，Expert的依赖仍按B解析最新。上传与绑定失败分别明确呈现，不声称两次既有调用整体原子或推断已运行会话立即热更新。这里确认服务端底座可用，不声称Buddy现有UI已实现完整本地选择推送。

## 8. 错误、无降级与版本隔离

| HTTP/code | reason | 行为 |
|---|---|---|
| 400 VALIDATION_ERROR | relations_required / dependency_version_mismatch / dependency_embedded / connector_version_forbidden / team_wiring_invalid / team_member_name_conflict / unsupported_type / zip_only_required / dependency_scope_mismatch / view_invalid | 明确字段；`target_version_id` 仅在提供时校验归属/release，缺失不再报错；不补current、不把Admin JSON旁路接入ZIP内容；space/system 引用 private 目标按 `dependency_scope_mismatch` 拒绝且不扩大受众；view 非法组合 `details.field=view` |
| 404 NOT_FOUND | dependency_unavailable / version_not_found | 无权目标不泄漏ID/名称/存在性；private/跨Space/隐藏路径匿名；不删边后成功；A34/读取路径不回退旧版 |
| 409 CONFLICT | dependency_changed / dependency_unavailable / dependency_in_use / edit_conflict / version_label_conflict | 阻止变更/发布；依赖最新版变化或不可用时不替换审计值、不回退旧版；人工审核窗口变化或原安装节点下架/删除等资格失效用 `dependency_changed`；上游正常发新版不阻断原 plan 重试 |
| 409 CONFLICT | installation_in_progress / idempotency_input_mismatch / installation_attempt_expired | 保留原尝试/计划；同 key 不静默改为最新、不换key、不伪造完成 |
| 409 CONFLICT | same_install_identity_different_content | 同名不同内容安装冲突失败；不清空依赖、不随机重命名、不生成别名 |
| 400 VALIDATION_ERROR | mcp_server_name_conflict / unsupported_binary / incompatible_skill_path / legacy_container_format | 整体失败；旧容器引导显式转换，不自动转换 |
| 413 PAYLOAD_TOO_LARGE | budget_exceeded | 返回安全max/actual，不能截断文件/成员 |
| 500 INTERNAL_ERROR | artifact_integrity / relation_index_inconsistent | 内容/索引不一致是服务数据故障；不回退live/最新版本 |
| 503 UPSTREAM_UNAVAILABLE | installation_result_unknown / fleet_unavailable | 不回显上游Secret原文；仅在既有24h窗口内以原plan、同键和同完整请求重试，否则保留未知/人工核对 |

权限前置失败使用既有401/403；其他人的目标按404隐匿。**任何错误都不向无权者返回 private 目标的存在性、名称、ID 或数量；** `dependency_scope_mismatch`只在有权查看目标但来源受众不覆盖（含 space/system→private 且目标对操作者可见）时400，不透露隐藏路径。`dependency_version_required` 与固定 pin 相关语义删除；`dependency_version_mismatch` 仅表示“提供了审计 target_version_id 但不属于目标/未发布”。**最新解析失败的错误映射：** 读取/A34 目标不可见 → 404 `dependency_unavailable`；发布/批准/A34 最新版不可用 → 409 `dependency_unavailable` 或 `dependency_changed`；原安装节点资格失效 → 409 `dependency_changed`（权限错误仍按 401/403/404）；上游正常发新版不触发该错误；不新增顶层 code，也不回退旧版。A29/A30 的 `plan_stale`/`migration_incomplete` 不出现在 HTTP 错误表，CLI 迁移使用 §5 的计划/run 状态与退出码。所有handler需声明401/403/404/409/413/500/503中实际可达项，按原固定code枚举，不为reason新造顶层code。

## 9. 实施责任与验证口径

MP-01拥有版本/文件/CAS及A01唯一owner，负责A01/A02/A04～A08/A12/A13/A21～A24/A26的版本底座；**A09 已删除，不在任何 issue/客户端范围**。MP-02扩展A01依赖可用性（B 最新解析、四字段诊断、公共COUNT/facets、mine/Admin保留行）、共享最新解析服务、关系/图/refs、Connector effective 审核适配、删除并集，拥有A03/A10/A11/A14～A19/A25/A27。MP-03仍唯一拥有A31/A32安装与A34完整读取，A31/A32同一InstallCoordinator，A34复用最新解析resolver，Fleet只既有安装联调、不改代码；A34不依赖Fleet在线。MP-04负责§5的CLI生产迁移、冻结计划/摘要、run恢复、独立可见核验与数据安全回滚；**A29/A30 HTTP 已撤回，不属于本发布，也不属于任何实现issue的交付物**。CL-01消费A21～A27；CL-02负责Web/CLI的A01～A08/A10/A12～A19/A32/A34、mine四字段、CAS与新key重装；CL-03消费A01/A02/A03/A05～A08/A31/A34，单次A34、不补依赖；REL-01验收真实四端、数据层、Fleet/Daemon、部署门禁、旧执行器不可达及数据安全回滚。8issue依赖保持；**A34仍属MP-03，A09/A29/A30不在任何范围**；生成物、客户端字段形状须同步，不靠前端扇出/过滤或运行时兼容补救。

实现端点时将本文DTO落到Go struct与swag及OpenAPI3.1；每个拥有API的issue运行check/diff并保存真实结果。这里只生成设计，不运行未实现API，不将旧main OpenAPI校验结果当作新协议通过证据。最终REL-01按真实四端流量逐个核对A/F接口和失败行为。

# MP-01：不可变版本、发布、Connector effective 与统一读取（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型与 D2/D4/D7/D8；本 issue 不实现依赖解析，只提供被 MP-02/MP-03 复用的版本、文件、effective、CAS 与读投影底座。**无 A09 restore，不引入 `restored_from_version_id`。**

## 目标（Goal）

交付可运行的版本/发布底座，不按“建表、codec、读接口”拆 issue。硬依赖无；技术输入为[方案](../01-technical-design.md)及[API 契约](../02-api-contracts.md)。完成后可独立验证无依赖 Skill 的保存/发布/审核隔离、历史读取和 Connector 编辑/有效态；新的引用式专家入口在 MP-02 完成前关闭，不能暴露无授权引用写入。MP-02 以完整底座为合并条件；A34 端点由 MP-03 实现，本 issue 只提供版本文件与 effective 底座。

范围：octo-marketplace。

## 背景（Background）

B 模型下 Expert→Skill、Team→Expert 的 `target_version_id` 不再作为公共读取、A34、安装、审核的权威依据，只保留为上次解析/审计值。因此本 issue 的快照 codec 必须能无损保存“目标资源 ID + 可选审计版本”，并把版本解析留给 MP-02 的唯一最新解析服务；不能在保存/发布时把当前最新版写死成 pin，也不能因为目标是浮动引用而拒绝保存。

同时按 D2 明确**不做 A09 restore**：不新增恢复路由、不写 `restored_from_version_id`、不在快照复用规则里保留 restore 例外；A04–A08 历史读取继续保留。D3 明确本期不允许同名/多版本共存：安装名、原字节与 frontmatter 均保持市场原样，不生成 `mk1` 别名。

## 承重行为（Load-bearing）

1. **current/published/release 分离。** 正式发布身份唯一来自 `plugins.published_version_id` + release；保存只写 current/editing。公共读取默认 published，编辑显式 editing，Admin 默认 current。发布/批准不回写 current，不覆盖更晚草稿，不把 published 指针回切旧值。
2. **快照不可变。** 文件原字节、路径、hash、关系身份、标签、declared_visibility、content_digest 冻结后不可改。`content_digest` 覆盖文件与规范化关系身份，但**排除浮动的 `target_version_id`/effective 解析值**；仅换候选标签、拟定受众、relation_id 或关系集合也必须产生新不可变快照。
3. **CAS 与 relations 必填。** 每个版本化资产有从 1 开始的 `edit_revision`；更新/重传必须带 `expected_edit_revision`，新建必须省略；缺失/过期 409 `edit_conflict`，不因内容相同绕过。完整 Write 的 `relations` 必填数组，缺失/null 400，显式 `[]` 才清空。四端（Admin/Web/CLI/Bot）同批接入。
4. **Connector effective（D7）。** Connector 的编辑（editing）/审核/生效（effective）分离，不新增业务 Version；需审核的 Tenant 编辑批准后才产生新的 `effective_revision/effective_digest`；Admin 不审核，直接写入同步 effective。A34/安装读 effective，编辑态不对外生效。审核 observation 由 MP-02 接入，本 issue 只提供表和事务。
5. **统一读投影与 view 规则。** 公共列表只接受 `view=published`（缺省）；`mode=mine` 只接受 `view=editing`，mode 只控制范围、不等于投影开关；Connector 可读 `effective`；Admin 走 A21 默认 current，不混用 A01。其余组合 400，`details.field=view`。历史读取按当前有效受众 ∩ 版本 `declared_visibility`，后来公开不追溯公开旧私有历史。
6. **A01 四字段 DTO 底座（D4）。** A01 只有 `mode=mine` 的 Expert/Team 必返 `dependency_availability={status,evaluated_view,evaluated_version_id,reasons}`；`evaluated_view/evaluated_version_id` 指来源自身的 published/editing（有 published 诊断 published，从未发布才诊断 current/editing）；`reasons[].code` 固定 `dependency_delisted|dependency_deleted|dependency_unavailable`；`target.version_id` 为本次解析到的最新目标版本（MP-02 填）；无权依赖合并为匿名 `dependency_unavailable`，不带 ID/名称/类型/版本/数量。公共行和其他类型省略该字段。本 issue 只定义 DTO、view 与列表接入点，不实现最新解析、reason 授权和 SQL 谓词。
7. **B 解析只做一次、在 MP-02。** 本 issue 不写任何“取最新”算法；A01 公共过滤、A34、安装、审核必须复用 MP-02 的同一解析器，每次请求各自使用一致性快照。客户端不能靠 `target_version_id` 固定依赖，服务端也不得在解析失败时回退旧版本。
8. **无 A09、无共存改造。** 不注册 restore 路由、不写恢复字段；不实现 `mk1` 改名、不重写 `SKILL.md` frontmatter、不改 Fleet/Daemon 命名。

## 非目标（Out of scope）

关系最新解析/pin 校验/删除保护/历史影响/A01 reason 授权（MP-02）、Fleet 安装与 A34 HTTP（MP-03）、存量资产转换（MP-04）、客户端 UI（CL-01/02/03）、A09 restore、A29/A30 HTTP、同名共存、Fleet/Daemon 改造。不能以非目标为由破坏既有关系快照或附件保留。

## 源码与数据所有权

- `internal/model/plugin.go`；`internal/repository/plugin/{write,read,history,review,listing}.go`。
- `internal/service/plugin/{service,listing,review,admin,admin_import,import,skill_tree,skill_artifact}.go`；`internal/api/handler/plugin/{handler,listing,review,admin,skill}.go`。
- `migrations/sql`；`internal/storage` 现有本地/OSS 适配；OpenAPI 生成入口按仓库 Makefile，补入其当前遗漏的 handler/plugin/skill.go 扫描，不能漏 Tenant import/skill_md/download 生成物。
- 本 issue 唯一创建：plugins 的 published/edit_revision/declared_visibility 基础字段、plugin_versions 完整快照字段、plugin_version_releases、plugin_connector_effective 及 review 冻结源版本/修订/摘要字段；不新增分发状态列，不创建 `restored_from_version_id`。
- 统一 relation snapshot codec 要无损保存现有关系（目标 ID、`relation_id/data`、可空 `target_version_id`）；`target_version_id`/版本 refs 的实际列和索引归 MP-02，MP-01 预留可空字段与无调用者接口但不提前建表。MP-01 提供冻结内容与唯一 release/effective 事务；MP-02 拥有 A14～A17 审核端点适配、依赖观察与完整锁集接入，不能再实现第二个发布事务。
- A01 唯一 owner 仍为 MP-01：本 issue 提供公共/mine 模式分流、版本根投影与 `dependency_availability` DTO 底座；MP-02 扩展 A01，拥有共享解析/依赖谓词、reason 计算及授权，不能在 MP-01 另写一套可用性算法。原 8 issue 硬依赖不变。

## 实现步骤

1. 加增量迁移：current/published 可空、同资源版本归属、revision_no 并发唯一；保存序号与 SemVer 分开。plugins 与版本快照均新增 declared_visibility，保存拟定受众但不直接扩张线上 `plugins.visibility`。定义版本 `content_digest` 包含文件和规范化关系的共享 codec，**不含浮动目标版本**；只换候选标签或拟定受众也必须得到对应的新不可变快照，不能仅按 content_digest 去重或原地改旧 Version。新增 `edit_revision`、Connector effective 表/列；不新增 `restored_from_version_id`。
2. 不可变对象：每个文件记录相对 path/mime_type/size/hash，原始字节先校验上传、后提交 snapshot；先完成安全相对路径、软链接、UTF-8 和预算检查，只复用已校验对象，禁止同 key 覆盖。原 ZIP、SKILL.md 与全部支持文本均有可验证映射。失败清理检查所有资源的 current/versions/pending 引用；向 MP-03 提供安装计划引用保留接入点，不能沿用只查当前 plugin_id 的 `retainedAttachmentKeys`。历史版本不自动淘汰。
3. 保存写入 CAS `expected_edit_revision`：新建必须省略，更新必须提供；锁内不匹配返回 409 `edit_conflict`，不因内容相同绕过 CAS。仅完整冻结内容、候选标签、declared_visibility 及关系身份均一致时才可复用 current；同 digest 但标签/受众/relation_id 变化仍建新快照。完整 Write 的 relations 必填数组，缺失/null 均 400、空数组清空；既有 Skill parse-task 导入使用其独立 wire，不要求新增 relations 数组。对调用者更新协议同步 handler 与 OpenAPI，不保留隐式旧语义。将现有 published+space 禁止保存的旧闸门改为可保存独立 editing 草稿；保存不得改 published 或 listing。
4. 提取唯一 release 事务：Skill/Expert/Team 写 release+published+listing 及有效 visibility，校验版本归属和正式标签；不得改 current 行/编辑关系。标签严格 MAJOR.MINOR.PATCH，新增正式标签大于该资源最高 release 标签、且与指定 Version 一致；不比较更高 current 草稿。unique(version_id) 与 unique(plugin_id,version_label) 兜底；同一已发布 Version 重试复用原 release，不用它重新激活被后续版本替代的历史发布。历史快照名、摘要、标签与文件均不可变。
5. 审核冻结 `version_id/source_edit_revision/frozen_content_digest`，批准读取该快照，直接进入唯一发布事务，不再执行旧 ApproveReview 的回写 current→sync live→重新 snapshot 链。一个资源最多一个 pending；approve/reject/cancel 均 CAS pending，重复批准同请求返回原已批准结果、不重新发布，旧请求不能覆盖后续 published。Connector 审核冻结完整编辑内容/修订/拟定受众，批准只更新 effective 行及有效 visibility，不新增 Connector 业务版本；`edit_revision` 与 `effective_revision` 独立，所有 Connector 写入口停止新写 `plugin_versions/release`。MP-02 再加依赖观察与锁内规则。
6. 将版本/effective 身份接入现有 AdminCreate/Update、AdminImport 及 Skill 重传写入策略，不以新矩阵替换既有保存、发布、审核、上下架行为或访问检查。Admin 创建版本化资产同事务设置 current/published/release 及原 default 投放；Connector 对应 effective。Admin 更新保留原 listing、归属、受众与投放，仅原直接生效的内容走 release/effective，不因改造额外上架；Expert/Team 的内容、内容元数据与关系只走 MP-02 的 A25 ZIP，A24 拒绝这两类。评分/展示位仍走原专用入口，不写内容快照；Owner/Space 保持原身份，不从请求覆盖。
7. 列表/详情/搜索/标签/名称排序/历史文件按 `view` 统一投影。普通默认 published，编辑显式 editing，Admin 默认 current；不是查 current 后替换 Package。版本列表区分保存与曾发布，正式身份来自 release。A01 公共列表预留 MP-02 的依赖可用性谓词接入点（Repository 共享 from/where），使 COUNT 与分页前过滤同口径；分类 `plugin_count` 和公共 tags 复用该谓词，不逐卡调用 A34/读取文件。`mode=mine` 不套用公共隐藏条件，保留原 owner/Space/非删除资产及各 listing 状态、total，mine tags 同样保留；Admin 保留管理入口。历史 Version 读取不得沿用 `history.go::redactVersionRelations` 吞解析错误/静默剪边；按契约鉴权失败或完整性错误明确失败。
8. 实现 API A01/A02/A04～A08、A12/A13 的版本底座、A21～A24/A26；A12 同时拥有既有 `POST /plugins/upsert` 与 `POST /plugins/import` 的 Skill 保存适配，后者从 parse task 完整写入 SKILL.md 和支持文件。Connector 不提供 versions。Version 正文和文件接口不暴露 object key。A01 仅 mine 的 Expert/Team 必返 `dependency_availability` 四字段；其他类型/公共行省略。已有 published 时以发布根诊断，即使展示 editing；仅从未发布时取 current。应有版本/refs 缺失及 DB 故障按完整性/服务错误处理，不伪造 available。reason 授权与实际判定由 MP-02 接入；MP-02 完成前通过部署隔离保持新引用功能未放通，不新增未定义的业务开关作为实现前置，不提供假成功。
9. 旧 pending review 在启用前必须按原冻结内容形成可证明新快照或明确 fence 并要求重新提交；不拿当前编辑覆盖旧审核。盘点旧发布证据可以复用 MP-04 工具，但新资源的底座 smoke 不等待历史迁移。

## 接口边界与失败语义

- 输入、输出和错误唯一以 [API 契约 §2～§3、§8](../02-api-contracts.md) 为准：A12 upsert 使用完整 Write，更新 CAS；既有 Skill `POST /plugins/import` 新建使用 parse_task_id+plugin_name（其余导入字段按原 wire），更新使用 parse_task_id+plugin_id+expected_edit_revision，返回 Detail，不要求 relations 数组，不将导入改为审核正文。A13 显式 version_id 与匹配 version 标签，Connector 省略 version_id 并提交 expected_edit_revision；审核端点由 MP-02 接入且只引用确定版本，不能在 handler 猜 current 或接受第二份审核正文。
- A04 默认 scope=released，scope=all 仅 owner/有权管理者；A05/A06/A07/A08 均验证 version 归属、release 及历史受众交集。**A09 不实现、不注册**：没有 restore 路由、没有 `restored_from_version_id` 输出或写入；旧客户端调用按 404/网关拒绝，不保留兼容 handler，也不做一键回滚上线。Connector 不提供版本/恢复接口。
- 文件响应统一使用 path/mime_type/content_size/content_hash 映射；A07 成功为 ZIP 字节流，其余按各自 data 包络。缺对象或摘要异常 500 `artifact_integrity`，不能返回新包/空正文；Version/list 读取的解析或索引故障不伪造 `[]`。普通人不可见或版本不存在按 404 安全隐匿，只有授权编辑/管理读取可见未发布快照。
- 完整 Write 结构/缺 relations 为 400 `relations_required`（`details.field=relations`），Skill parse-task 导入按独立请求字段校验；CAS 为 409 `edit_conflict`，正式标签冲突为 409 `version_label_conflict`，均使用固定顶层 code 和安全 details。`view` 非法组合 400，`details.field=view`；不把 `mode=mine` 当编辑投影，也不允许公共列表读取 editing。同步声明实际可达的 401/403/404/409/413/500；无内部 key、SQL 错误或凭据回显。A01 保留真实 query 名 scene_code/plugin_type/category_id/tag/q/mode/sort/page/page_size，mine 仍需 scene_code，只有 mine 可省 plugin_type；view 是版本投影参数，不改名兼容猜测。
- MP-01 负责在唯一保存/发布/审核底座采用[方案 §5.3](../01-technical-design.md#53-保护查询与事务)锁序及事务 CAS；MP-02 扩展完整依赖锁集与 Policy。review（存在时）先于按 ID 排序的全部 Plugin，再 Version、effective/placement/refs；不能保留旧 ApproveReview 与 Delete/Delist 取消 pending 之间的反向锁序。锁后复核预读身份，图变则重算；无 OSS/Fleet 长事务，死锁最多重试 3 次后 409 `CONFLICT`。

## 验收（Acceptance）

MP-01 独立合并覆盖无依赖 Skill/Connector 及唯一保存/发布/审核事务底座；以下冻结批准场景先在底座事务边界证明，A14～A17 真实 HTTP 和含依赖图的验收由 MP-02 集成完成，不反向将 MP-02 作为 MP-01 硬依赖。

- [ ] Skill 发布 v1、保存 v2、提交 v2 审核、再保存 v3、批准 v2：current 完整为 v3，published 完整为 v2；内容/标签/文件/关系均无回退。
- [ ] 对外列表/搜索/详情仍取发布版，作者 editing 取草稿；消费者不能通过版本 ID 取未发布草稿；`view` 非法组合 400 `details.field=view`。
- [ ] private v1 正式发布后用公开 v2 替换：普通新受众不能列出/读取/下载旧 private v1；owner/Admin 取证仍走现有授权路径，保留快照/文件证据，不新增查询目的或授权模式，当前公开不追溯公开历史。
- [ ] 同标签不同正式内容冲突；更高 current 草稿不阻止批准合法旧申请；重复批准不重新激活历史发布版。
- [ ] v1 含 A、v2 替换 A 并新增 B、v3 删除 A，仍能取 v1 原 A 并校验摘要；跨资源复用、取消审核、失败上传不误删历史对象。
- [ ] 两个并发 save 只有匹配 `edit_revision` 者提交，另一个 409；没有静默 last-write-wins。缺 CAS 明确 409；缺 relations/null relations 明确 400，陈旧 CAS 即使内容相同也 409，创建携 CAS 拒绝。
- [ ] 仅标签/仅 declared_visibility/仅关系身份变化都新建不可变 Version 且保留旧候选；完整候选及关系身份一致、CAS 匹配才不增 Version。**快照的 `target_version_id` 可空且仅作审计，不参与 digest 的浮动部分，不因目标后来发新版而改写旧快照。**
- [ ] 正式 1.2.3 后拒绝 1.2.2 及另一 Version 复用 1.2.3；合法 1.2.4 冻结申请不受 current 9.0.0 草稿阻塞。重复发布/批准不增 release；已发布后续版后重放旧请求不回切 published。标签格式与 version_id 快照不一致明确失败。
- [ ] A04 released/all 与 A05～A08 交叉验证草稿隔离、错归属及历史受众；未发布快照不可被消费，历史受众交集正确。
- [ ] Connector 的 Admin 直接生效、Tenant 待审编辑/人工或自动批准均走对应 effective 事务；批准旧冻结编辑时保留更新的 editing 内容及 edit_revision，不新增 Version/release，不把编辑 tool_count/名称/配置泄漏到 effective 列表。
- [ ] 需审核的 Tenant Connector 未批准编辑不进入市场/安装读取；批准后 `effective_revision` 递增，插件业务 Version 行数不增加。
- [ ] Admin Skill/Connector 的 Create/Update 及 A26 保留原归属、listing、发布/审核与上下架访问检查，只接版本/effective 底座；Admin 默认 current 不冒称线上版。Expert/Team 的 A24 明确拒绝，A25 由 MP-02 实现，不保留旧 JSON 覆盖旁路。
- [ ] 跨 Space/他人草稿/错资源 version/缺对象/摘要不一致明确失败，无对象 key 泄漏。
- [ ] Tenant `POST /plugins/import` 保留 parse-task 独立 wire：新建 parse_task_id+plugin_name、更新 parse_task_id+plugin_id+匹配 CAS 可保存完整 Skill 草稿；不传 relations 也合法，旧 published 不变。新旧版本 SKILL.md 与每份支持文件都可精确重取；陈旧 CAS 或无权/失效 parse task 失败不改 current/published，不误删历史对象。
- [ ] 本 issue 不实现最新解析、不回退旧版本、不写第二套“取最新”代码；依赖相关断言留给 MP-02 集成验收。

### MP-02 接入后的集成验收（由 MP-02 实现判定，不阻塞 MP-01 独立合并）

- [ ] A01 公共依赖过滤在 SQL COUNT/分页前生效，分类 `plugin_count` 与公共 tags 同口径；mine 的异常来源行与 total 保留，草稿/待审/发布/下架及既有 owner/Space 管理范围不变，不新增回收站、不返回本人已删除资产，Admin 原管理入口保留。
- [ ] mine Expert/Team 必返四字段诊断，公共及其他类型省略；published e1 异常而 editing e2 已修复时 `evaluated_view=published/evaluated_version_id=e1`；只有从未发布才为 editing/current，正常无依赖图 `available` 且 `reasons=[]`，异常至少一条，缺应有版本/refs 或 DB 故障不假成功。
- [ ] B 解析下，Skill/成员 Expert 发布新版后引用它的 Expert/Team 在下一次查询自动跟随；最新版下架/删除时公共隐藏，不回退旧版、不改来源 `listing_state`；修复发布或依赖恢复后下一次查询恢复。
- [ ] MP-02 接入后，Skill/Connector/成员 Expert 异常及 Team 间接异常从公共结果排除；匿名原因不泄漏 ID/名称/类型/版本/数量，读列表不改 listing_state、不新增状态/接口/推送/轮询。

## 验证与交付（Verification）

使用隔离 MySQL 和本地对象存储/OSS 适配运行实际保存→发布→历史下载 smoke；至少一个跨资源共享对象取消/失败清理场景。永久回归覆盖隔离、CAS、历史完整性、发布标签和 effective 事务，不测试源代码字符串或简单透传。

按仓库先相关包再全量测试，运行 `make openapi-check`/`make openapi-diff`；提交增量迁移、Go 模型/handlers、生成物、部署前 pending review 处置说明与真实输出。此 issue 不能单独宣布旧市场读取全面切换；生产启用由 REL-01 门槛控制。

## 本轮评审修订验收

- [ ] Admin 无审核步骤：Skill/Expert/Team 沿用现有直接生效事务，Connector 同事务更新 effective；仍校验 CAS/依赖/范围，不创建 pending review。
- [ ] 需审核的 Tenant Connector 编辑不改 effective；与 Admin 直接生效分别测试，避免通用“必须审核”分支覆盖 Admin。
- [ ] 基于实际合并基线复用 release/refs 等既有表，只实施增量 DDL；MP-01 → MP-02 → MP-03 顺序保持，不提前放通缺失依赖保护的入口。

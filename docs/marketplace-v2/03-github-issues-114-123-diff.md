# GitHub #114–#123 与 Marketplace v2 方案差异记录

> **2026-10-26 15:00 历史说明：** 本文比较的是“远端 issue vs 固定 pin v2 方案”。此后用户已锁定 **B 模型**（Expert→Skill、Team→Expert 都解析最新；A09/A29-A30/共存不做；private 同 owner 允许；删除按并集保护）。本文中与 [00-decisions](00-decisions.md) / [决策记录](decisions-20261026.md) 冲突的“当前依据”不再代表现行方案，仅保留为历史比对。

核对日期：2026-10-26。**以 [00-decisions](00-decisions.md) 的 B 模型为当前方案；本文的固定 pin 段落为历史。**

本记录只比较需求、接口、任务边界和验收口径，不审查实现是否满足方案，不修改远端 issue，不执行生产操作。远端 issue 关闭、PR 合并、正文中的测试声明均不等于通过当前 v2 验收。

## 1. 依据、范围和分类

### 1.1 权威依据

- [方案总览](README.md)，尤其“方案权威”及“逐文档审查与编码就绪边界”。
- [规则裁定](00-decisions.md)：B 模型、最终决策与已撤回项（本文的固定 pin 段落仅历史）。
- [技术方案](01-technical-design.md)：内部模型、不变量、事务、读取、安装和迁移。
- [API 契约](02-api-contracts.md)：URL、DTO、必填字段、错误、幂等和恢复的唯一 wire 依据。
- [issue 索引](issues/README.md)、[plan.json](plan.json)及其引用的八份本地 issue：实现所有权、硬依赖和可观测验收。

下文简称“裁定”“设计”“API”“MP/CL/REL”，均指以上当前文件，不是远端正文引用的历史 task brief 或旧开发方案。行号取本次核对时的工作区内容；后续编辑以章节和链接为准。

### 1.2 远端快照

通过 [GitHub Issues REST API](https://api.github.com/repos/Mininglamp-OSS/octo-marketplace/issues?state=all&per_page=100)读取完整正文与状态，并对 #114 单独查询交叉核对。#114–#123 均为 issue，均 `comments=0`，因此没有另行覆盖正文的 issue 评论。下表 `updated_at` 为 GitHub 返回的 UTC 时间，不是本地修改时间。

| Issue / 标题 | 状态 | updated_at（UTC） | 当前本地能力映射 |
|---|---|---|---|
| [#114 · Overview](https://github.com/Mininglamp-OSS/octo-marketplace/issues/114) | open | 2026-10-24T06:46:49Z | 全项目总览；需覆盖 MP-01～04、CL-01～03、REL-01 |
| [#115 · V1 immutable versions/artifacts](https://github.com/Mininglamp-OSS/octo-marketplace/issues/115) | closed | 2026-10-26T01:42:18Z | MP-01 的版本/文件底座子集 |
| [#116 · V2 publication/history](https://github.com/Mininglamp-OSS/octo-marketplace/issues/116) | closed | 2026-10-26T01:42:19Z | MP-01 的发布/读取子集 |
| [#117 · R1+R2 pinned references/permissions](https://github.com/Mininglamp-OSS/octo-marketplace/issues/117) | open | 2026-10-24T06:45:47Z | MP-02 的写入/关系/索引子集 |
| [#118 · R3+R4 publication/dependency protection](https://github.com/Mininglamp-OSS/octo-marketplace/issues/118) | open | 2026-10-24T06:44:33Z | MP-02 的冻结审核/保护/读取子集 |
| [#119 · I1+I2 pinned installation content](https://github.com/Mininglamp-OSS/octo-marketplace/issues/119) | open | 2026-10-24T06:44:36Z | MP-03 的 resolver/物化子集；缺 A34 |
| [#120 · I3+I4 installation/runtime verification](https://github.com/Mininglamp-OSS/octo-marketplace/issues/120) | open | 2026-10-24T06:44:39Z | MP-03 的安装/重试及部分 REL-01 |
| [#121 · M1 read-only inventory](https://github.com/Mininglamp-OSS/octo-marketplace/issues/121) | open | 2026-10-24T06:45:50Z | MP-04 的 inventory 子集 |
| [#122 · Enable rollout/rollback](https://github.com/Mininglamp-OSS/octo-marketplace/issues/122) | open | 2026-10-24T06:44:45Z | REL-01 的部分就绪验收；缺四端完整条件 |
| [#123 · M3 stock migration](https://github.com/Mininglamp-OSS/octo-marketplace/issues/123) | open | 2026-10-24T06:45:59Z | MP-04 的存量迁移子集；显式排除了当前必需的转换接口 |

映射是能力归属，不要求把已有十个远端 issue 机械删除或重编号为八个本地 issue。远端可以保留历史追踪，但验收必须覆盖当前完整能力。

### 1.3 差异分类

- **冲突**：远端明确要求的行为与当前方案相反，或明确排除了当前必需范围。
- **遗漏/未细化**：正文没有足够约束或验收；不据此断言实现一定错误，也不把简写当成相反规则。
- **过时进度/交付组织**：PR、状态、依赖或分组描述落后/不同于当前安排，不是产品行为错误。
- **一致**：保留，不为制造差异而改写。
- 优先级：**P0** 为会改变授权、数据安全、安装结果或必需交付范围的冲突；**P1** 为实现/验收所需的关键缺口；**P2** 为追踪信息与组织更新。不是线上故障分级。

## 2. 十项共同携带的旧规则

### G01 · P2 · “恰好三个 PR”不再是当前实施约束

**远端证据：**十项正文均有 `Exactly three implementation PRs`，将 V1+V2、R1–R4、I1–I4+M1+M3+readiness 固定为三批，且反复使用 #113、`PR 2/3 ... not yet opened`。

**当前依据：**[issue 索引 §1、§2、§4](issues/README.md)（L15–24、44–50、64–68）、[plan.json](plan.json)明确四个 Marketplace 能力 issue、三个客户端 issue 和一个联合切换 issue，不强制一 issue 一 PR；本仓库 `AGENTS.md` 默认两个后端交付 PR（MP-01+02、MP-03+04），不强制按远端旧三批执行。

**影响与修订：**保留远端历史评审边界，但用当前能力/仓库/硬依赖映射安排交付；不能以旧第三 PR 已提交或后端四项完成代替客户端和 REL-01 完成。不要把“八个本地 issue”误写成“必须八个 PR”。

### G02 · P0 · “曾发布一律禁止删除/收窄/转移”改变了本期生命周期边界

**远端证据：**十项均重复 `Published resources are delist-only (no delete, narrowing or transfer)`；#118 更明确写 `DELETE is allowed only for drafts/never-published resources`。

**当前依据：**[裁定“本轮确认状态”、§2 C03、§5](00-decisions.md)（L11–13、34、69–73）；[设计 §5.2～5.3](01-technical-design.md)（L156–170）；[API §3.2](02-api-contracts.md)（L204–219）。本期沿用既有上下架、删除、访问和投放动作，不新建“曾发布即永不删除”的规则。删除保护是**既有有效 live incoming（含草稿、不按 listing 过滤）＋published 固定快照直接/间接 refs 的并集**。

**影响与修订：**撤去“一旦发布就永久禁止”总规则，改为既有动作＋上述保护和受众校验；这不表示无条件允许删除、收窄或改归属。引用式来源删除只删自身和自身关系，不级联独立共享目标；保留该一致要求。影响查询只读也保持，但不能用总禁令代替真实保护集合。

### G03 · P0 · `restore-to-draft` 被整体排除，与 A09 必需范围相反

**远端证据：**十项共同写 `Restore-to-draft and security-disable are outside the first release`；#116 多次明确排除恢复端点。

**当前依据：**[API A09、§3.1](02-api-contracts.md)（L186–198）、[MP-01“接口边界”与验收](issues/01-version-publication.md)（L35、50–56）。版本化资源由 owner 携新标签和 `expected_edit_revision` 恢复成**新 ID、新 editing 快照**，完整保留文件和 pins，不切 published；Connector 不支持恢复。

**影响与修订：**将历史恢复草稿纳回 MP-01 与 CL-02；不要误恢复成“一键回滚上线”。`security-disable` 仍不在当前范围，不能因两者在同一句中出现而一起加入。

### G04 · P2 · 正文中的 PR/实现进度已过时，关闭状态不能替代新方案验收

**远端证据：**#114–#123 多处将 #113 称为 sole PR1，PR2/PR3 未打开，若干正文仍写 `not implemented`/`planned`。

**当前可观察事实：**同次 API 快照中，[#113](https://github.com/Mininglamp-OSS/octo-marketplace/pull/113)已 closed 且未合并；[#124](https://github.com/Mininglamp-OSS/octo-marketplace/pull/124)于 2026-10-26T01:42:17Z 合并；[#125](https://github.com/Mininglamp-OSS/octo-marketplace/pull/125) open、非 draft；[#126](https://github.com/Mininglamp-OSS/octo-marketplace/pull/126) open、draft。#115/#116 已 closed，但正文仍保留旧 #113 进度。

**影响与修订：**更新实际承载 PR/基线/Refs 或 Closes，历史实现和评审记录仍保留。这里只核对 API 元数据，未审查 #124～126 代码或复跑其验证；不判定已合并实现符合或不符合 v2，也不据此建议回滚主干。

## 3. 跨 issue 缺失的完整交付范围

### X01 · P0 · 没有覆盖已确认 A34 单次完整内容读取

**远端证据：**十项正文没有 A34、`/distribution` 或等价的“一次返回固定图全部原始内容、客户端不补取”验收；#119 只有安装计划 resolver，#120 只有安装入口/运行验收。安装用 resolver 不等于对外完整读取接口。

**当前依据：**[裁定 §7](00-decisions.md)（L95–106）、[API §6.1](02-api-contracts.md)（L351–376）、[MP-03 实施步骤11～12](issues/03-installation-fleet.md)（L30–31）、[CL-02](issues/06-web-cli.md)、[CL-03](issues/07-buddy.md)。

必须补齐：

1. `GET /market/api/v1/plugins/{plugin_id}/distribution`，只有 path ID＋既有认证/Space 上下文；不要求 workspace/runtime/custom_env/幂等键，不提供历史根覆盖。
2. 一次返回 `{data:DistributionBundle}` 的 `schema_version/root/graph/assets/bundle_digest`。Expert/Team 含自身和成员的 `AGENTS.md`，Skill 含准确 pin 的 `SKILL.md` 及全部支持文件，Connector 含选定有效配置声明/占位符、`files:[]`。每个 `node_key` 恰好一个 asset。
3. 同一短只读一致性事务选定 published 根、refs、完整 effective 行，退出后读不可变文件并校验；防止旧 revision 配新 payload。文件 hash 对原始 UTF-8 字节计算；bundle 按指定数组排序及 RFC8785 JCS 计算摘要。
4. 原市场名和字节不变，无 `installation_name`、frontmatter 改写或安装副本。能表示同 Skill 两版本不等于批准运行时共存。
5. 最终完整响应 JSON（包括包络、转义）≤16MiB；文件/图预算同时校验。缺节点/文件/权限/完整性全失败，不分页、裁剪、补 latest、返回链接让客户端补齐；`no-store`，零 Fleet 调用、零安装记录/业务资源写入。
6. Web 完整内容界面、CLI 完整查询、Buddy 完整准备均只消费一次 A34；删去先 A03/BFS 再按依赖请求 detail/version/skill_md/download/files 的生产链路。独立历史工具及 A03 轻量图仍可保留。

**归属与风险：**MP-03 唯一拥有端点，复用 MP-01 文件和 MP-02 固定图底座；CL-02/03 接入、REL-01 验收。若只按 #119/#120 关闭安装任务，当前必需的内容查询和客户端迁移仍未交付。

### X02 · P0 · 没有覆盖公共列表过滤及“我的发布”依赖诊断

**远端证据：**十项正文没有 `dependency_availability`，也没有公共 SQL 分页/COUNT/facets 与 mine 保留行、独立标记/Tooltip 的对应验收。#118 的 dependents/candidates 是不同接口，不能替代 A01。

**当前依据：**[裁定 §8](00-decisions.md)（L109–117）、[API §2.2.1](02-api-contracts.md)（L76–136）、[MP-02](issues/02-dependency-governance.md)、[CL-02](issues/06-web-cli.md)、[REL-01](issues/08-cutover.md)。

必须补齐：

1. 公共 A01 从来源 published refs 出发，Team 沿固定 Expert 版本递归检查；任一直接/间接 Skill、Connector、成员 Expert 下架、删除、缺失，来源不进入公共列表，不修改来源 `listing_state`。
2. 同一 metadata predicate 在 SQL 排序/分页/COUNT 前生效，分类 `plugin_count`、公共 tags 同口径。版本/refs 完整性错误不能被不可用过滤吞掉；不得前端丢卡、逐卡调用 A34 或读文件。
3. mine/Admin 保留原管理范围内全部非删除资产和 total，保留原草稿/审核/发布/下架状态和动作；不新增回收站。Web 全部/专家/专家团 tab 显式 `mode=mine&view=editing`；mine+published 和公共+editing 返回400，而不是猜视图。
4. 仅 mine 的 Expert/Team 必返 `status/evaluated_view/evaluated_version_id/reasons`；已有 published 总是诊断该发布根，从未发布才诊断 current 草稿。正常无依赖为 available/空 reasons；文件读取或 Fleet 安装是否成功独立判定。
5. 原因固定为 `dependency_delisted|dependency_deleted|dependency_unavailable`。目标及完整 via 路径都可读才返回具体身份；不可披露部分只合并一条匿名 unavailable，不泄漏隐藏身份、数量或删除/下架细节。
6. Web 所有对应 tab 显示独立“依赖异常”及中英文、hover/键盘 focus Tooltip，不替换原状态。仅修草稿不清除发布根异常；新修复版发布或依赖合法恢复后下次查询重算，不新增状态列、推送或轮询。

**归属与风险：**MP-01 是 A01 唯一端点 owner；MP-02 扩展 predicate/原因授权；CL-02 做 Web 诊断，CL-03 只消费公共过滤，REL-01 验收。只交付引用写入/影响查询不满足此项。

### X03 · P0 · 缺少显式客户端工作与联合放行依赖

**远端证据：**#114 三批只安排 Marketplace 逻辑任务；#122 的依赖仅列 #115～#121/#123，未列 Admin/Web/CLI/Buddy 的完整适配与当前 CL 验收。#120 的双入口服务端接线不能代表客户端已切换。

**当前依据：**[plan.json](plan.json)中的 CL-01、CL-02、CL-03、REL-01；[issue 索引 §1～3](issues/README.md)。REL-01 必须等 MP-03、MP-04、CL-01、CL-02、CL-03 五个直接依赖完成。

需要单独追踪（可沿用远端 tracker，不限定新增 PR 数）：

- [CL-01 Admin](issues/05-admin.md)：Expert/Team 原 ZIP 整体上传/重传、schema2/固定预览；不设依赖选择器或页面覆盖；逐 ZIP 幂等和不确定重试；Skill/MCP 全量 CAS；评分失败只重试评分。
- [CL-02 Web/CLI](issues/06-web-cli.md)：全部写入/发布/审核调用改确定快照和 CAS；parse-task 完整 Skill 包导入；A34 及 X02；用户安装与真实 token-kind 限制；`custom_env` 在 `--data`、dry-run/verbose/错误链路均脱敏；稳定意图与未知结果保留。
- [CL-03 Buddy](issues/07-buddy.md)：完整内容改一次 A34，移除补 ZIP/BFS/字节改写；保留纯本地导入。Workspace/runtime UUID、安全错误从 HTTP 经 IPC 到 renderer，pending/已知成功持久化，不因409、关闭重开或结构升级删除未知记录/换 key。
- [REL-01](issues/08-cutover.md)：真实四端、MySQL/OSS/Fleet/PostgreSQL/Daemon/身份环境及可复现失败证据，才可放行；后端 build/tests 或 PR 合并不替代这些验收。

## 4. #114 · 总览逐项差异

来源：[Overview #114](https://github.com/Mininglamp-OSS/octo-marketplace/issues/114)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D114-01 · P2 · 交付组织 | `Exactly three implementation PRs`；PR3 包揽安装、inventory、全部迁移和 readiness | 按 G01 更新为能力与仓库依赖，不让合并组织代替逐项验收。当前 MP-02 后 MP-03/MP-04/CL-01 可并行；CL-02 完整完成仍等 MP-03，CL-03 等 MP-03。 |
| D114-02 · P0 · 范围冲突 | `M2 conversion entrypoints` 明列 Out of scope | [API §5/A29/A30](02-api-contracts.md)（L234–235、270–310）和 [MP-04](issues/04-migration.md)必需支持旧 ZIP 或数据库来源的只读 preview／批准 apply。拒绝旧包进入普通新导入与允许显式转换不冲突，不能整体删除转换入口。 |
| D114-03 · P0 · 范围遗漏 | Goal/Load-bearing/Acceptance 没有一次完整读取、公共列表诊断或显式客户端交付 | 完整纳入 X01～X03，并标端点/客户端 owner；不能称原三批已覆盖整个当前方案。 |
| D114-04 · P0 · 错用决策 | 把 `stable installation identity` 列为负载项，且将共存验收交给 #119/#120 | [裁定 §6](00-decisions.md)、[MP-03 步骤2](issues/03-installation-fleet.md)将安装名/共存保留 deferred；不能继承为本期批准算法和完成前置。只要求普通无冲突安装真实可用。 |
| D114-05 · P0 · 共通规则 | delist-only、排除 restore；migration `patch+1` | 生命周期/恢复分别按 G02/G03；标签由批准计划显式冻结，不能把自动 patch+1 当通用迁移规则，详见 D123-03。 |
| D114-06 · P2 · 进度 | `#113 remains open`、`PR 2 and PR 3 are not opened` | 同次远端元数据已不同，按 G04更新；同时保留“实际生产操作另授权”和“未完成只用 Refs”两条正确边界。 |

**一致项：**独立资源、Skill/Expert 精确 pin、Connector 无新业务版本、历史不改写、不造 release/pin、真实证据与生产另授权。范围大方向一致，不等于边界和验收一致。

## 5. #115 · V1 版本与文件底座逐项差异

来源：[V1 #115](https://github.com/Mininglamp-OSS/octo-marketplace/issues/115)；当前对应 [MP-01](issues/01-version-publication.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D115-01 · P1 · 快照身份未细化 | `Immutable snapshots`、`manifest/package/relations/attachment inventory` | MP-01 L22–25、API §2.3 L145–150 不仅比较内容摘要：只换 version_label、declared_visibility 或完整关系身份也应新建 Version；完整输入一致且非 restore 才可复用。补确定规则，避免同 digest 吞掉候选标签/受众变化。 |
| D115-02 · P1 · CAS/导入未细化 | 并发验收仅说 immutable writes 不能覆盖，没有完整保存 CAS wire | API §2.3、MP-01 L24、34、49–58：更新必须 `expected_edit_revision`，新建省略；完整 Write 的 relations 显式数组，null/缺失拒绝。Skill `POST /plugins/import` 仍用 parse-task 独立 wire、不传 relations，服务端保存原 ZIP/全部支持文件；CAS失败释放消费权。不能以统一 Write 误改导入协议。 |
| D115-03 · P1 · 历史权限未细化 | `correct resource and owner/Space authorization` | API §2.2 L58–72、MP-01 L46：历史可见范围是当前有效受众与该版本 declared_visibility 的交集。private v1 后来公开 v2 不追溯公开 v1。补 release/受众联合校验，而不是仅检查“同一Space”。 |
| D115-04 · P1 · 保留集合未细化 | `shared historical objects are never reclaimed as staging` | 设计 §3.3、MP-01 L23 覆盖任意资源 current、任意历史 Version、pending review、安装计划，不能只检查当前 plugin_id；MP-03 接入 plan 对象保留。补完整文件与 UTF-8/路径/预算、失败清理及跨资源引用验收。 |
| D115-05 · P1 · 旧审核兼容未细化 | `Legacy read/review compatibility` | MP-01 L30 要在启用前将旧 pending review 形成可证明冻结快照，或明确 fence 并由有权者重提；不能拿 current 补旧审核。补迁移/fence 分流和证据。 |
| D115-06 · P0/P2 · 共通差异 | 末尾 delist-only/restore 排除；#113 sole PR1 | 分别按 G02/G03/G04；已 closed 只说明远端状态，不证明补齐上述新验收。A34 端点归 MP-03，不应倒塞进 V1；V1 提供完整版本文件底座即可。 |

**一致项：**不可覆盖对象与原始字节保留、大小/摘要校验、坏对象失败关闭、不泄漏 storage key、不自动宣告历史已发布、生产切换另计。

## 6. #116 · V2 发布与历史读取逐项差异

来源：[V2 #116](https://github.com/Mininglamp-OSS/octo-marketplace/issues/116)；当前对应 [MP-01](issues/01-version-publication.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D116-01 · P0 · Admin 行为冲突 | `listed-system update/reupload publish atomically; delisted/draft-system or tenant edits save only`；要求 confirmed matrix | [裁定 C08](00-decisions.md)、[设计 §4.2](01-technical-design.md)、MP-01 L27 已撤回新矩阵。按真实既有 AdminCreate/AdminUpdate/import/reupload 行为接 release/effective，保留 listing/归属/受众/投放及访问检查，不按此旧矩阵重新设计。这里不推导“所有Admin更新都发布”。 |
| D116-02 · P0 · 恢复被排除 | Goal/Out of scope/Acceptance 均排除 `restore-to-draft`，要求 `no first-release restore-to-draft endpoint` | G03：A09 必需，只生成新草稿不切上线；Admin 新增独立 publish/restore/任意文件端点仍不要求。不能把“不新增Admin动作”外推成“不提供Tenant A09”。 |
| D116-03 · P1 · Connector 有效态未完整定义 | `Connector history is preserved but no new connector version model is added` | 设计 §4.3、API §2.2/2.3、MP-01 L26/L53–54：新写入停止创建 Connector Version/release，编辑与完整 effective payload 分离，两个 revision 独立；批准旧冻结编辑不覆盖新 editing。旧历史保留≠安装读 current。 |
| D116-04 · P1 · 发布身份/重放边界未细化 | `append-only formal release identity`、`release identity/pointer updates are atomic` | MP-01 L25–26/L47/L51：严格 SemVer、正式新标签大于最高 release（不比较更高 current 草稿）；version_id/标签匹配；重复旧批准返回旧结果、不重新激活历史 published。补唯一约束和重放验收。 |
| D116-05 · P1 · 读协议/权限未细化 | `Authorized immutable historical content/files` | API A01～A09 明确 selected view、scope=released/all、VersionSummary 与 Version、正文/文件接口及错误；历史受众交集见 D115-03。补完整投影一致性而不只替换 Package，列表名/搜索/排序/成员数不得读 current。A01 新底座及 MP-02 诊断扩展见 X02。 |
| D116-06 · P1 · 具体 wire/冻结审核缺口 | 区分 current/published，但未约定全部保存/发布/提审字段与冲突 | API §2.3：publish/submit 精确 version_id，version 标签与快照一致；approve 不回写 current。更新 CAS、完整 relations 及 parse-task 保存再提审见 D115-02。审核 HTTP 完整接入归 MP-02，不反向设为 MP-01 独立验收硬依赖。 |
| D116-07 · P0/P2 · 共通差异 | no-delete 总规则、restore 排除、#113 已过时 | 分别按 G02/G03/G04处理，不因远端 closed 跳过差异复核。 |

**一致项：**保存/正式发布隔离、批准冻结版本不覆盖后续草稿、release 有真实证据、历史不 fallback current、不泄漏对象 key；固定 Admin 版本详情及 version-aware skill_md/download 保留，与 A21～A23 大方向一致。

## 7. #117 · 引用写入与授权逐项差异

来源：[R1+R2 #117](https://github.com/Mininglamp-OSS/octo-marketplace/issues/117)；当前对应 [MP-02](issues/02-dependency-governance.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D117-01 · P0 · private 依赖冲突 | Acceptance：`Own authorized published private skills/connectors work in a private expert` | 裁定 L10–11、32；API §2.1 L54：private 来源仍只能新引用已发布 system/同Space space，**private 目标一律禁止**。本人已发布 private Skill 也不是例外；纯本地/工作空间组合不通过 private 市场中间层解决。远端尾部只约束 system/space 来源，不能消除此条显式冲突。 |
| D117-02 · P1 · placement语义需限定，非直接冲突 | 同一验收写 `placement is not required` | API §2.4/A10 将候选接口独立于场景列表，“候选不要求先投放指定场景”可与当前一致，不能只凭此句判相反规则。但裁定 C02、API §3.2 L219 保留既有非默认投放可见性、default不可隐藏及访问/发布资格。建议明确候选与场景列表的区别，不把此句外推为取消所有投放检查。 |
| D117-03 · P0 · 旧包显式转换被删除 | Out of scope：`old-format ZIP conversion (removed)` | 普通新导入拒旧内联包一致，但 [API A29/A30、§5](02-api-contracts.md)必需提供显式批准转换，归 MP-04；更新“从当前 issue 排除”而非“全项目删除”，并建立对应依赖/引导。 |
| D117-04 · P1 · Admin/Tenant 入口边界不够具体 | `across User/Admin/Bot writes`、排除 Admin dependency-selector UI | API §2.3/§3.3/§4：Admin Expert/Team 只能 A25 原ZIP，A24 拒绝其内容/元数据写；Tenant 仍 A12 完整 Write+relations，不是全端 ZIP-only。A25 强制幂等头、重传 CAS，最终关系回显 resolved 只含定义身份，不接受页面覆盖。补 schema2 字段、额外文件拒绝、成员 wiring、标签冲突与错误定位。 |
| D117-05 · P1 · 关系协议/refs 完整性未细化 | `Nullable target_version_id`、snapshots/index 同事务、保留 relation identity | API §2.1/§2.4、MP-02：DB nullable 不等于 wire 接受 null。固定两类必非空 pin，Connector 字段必须彻底省略（null/空也拒）；禁止客户端observed_*，先分配/协调最终relation_id，再同事务写快照/refs，保留data/sort_order。refs 与 relations_json 双向逐项核对 source/ID/type/pin，包括零边、等数量错pin，不能只验 count。 |
| D117-06 · P1 · A01 责任遗漏 | 未安排公共完整依赖可用性和 mine 原因授权 | 按 X02，MP-02 扩展 MP-01 的 A01，不新造列表、持久化状态或前端依赖检查器。 |
| D117-07 · P0/P2 · 共通差异 | delist-only/restore 排除、未开 PR2 | 按 G02/G03/G04，不能从这些旧共同段落继续扩权限/生命周期需求。 |
| D117-08 · P1 · A25导入receipt未细化 | 只泛称所有写入口及快照/索引同事务，未定义容器重复提交 | API §4 L268：唯一域actor＋operation＋target（新建new）＋key，摘要绑定原ZIP/schema/version/category/CAS；先查已提交receipt，同key同输入回放原Detail+resolved，不拿自己已推进的修订重做旧CAS，不重取latest。receipt与结果同事务，7天完整回放后tombstone拒复用；不是仅给HTTP加一个header。 |

**一致项：**Skill/Expert pin 必须属于目标且有 release、Connector 不 pin、快照和索引同事务、跨Space安全拒绝、Team递归受众校验、独立共享目标不级联删除、Admin可见不自动获得引用资格。

## 8. #118 · 冻结审核与保护逐项差异

来源：[R3+R4 #118](https://github.com/Mininglamp-OSS/octo-marketplace/issues/118)；当前对应 [MP-02](issues/02-dependency-governance.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D118-01 · P0 · 保护集合被总禁令替代 | `Published resources are delist-only`；DELETE 仅 never-published | 按 G02 改回既有动作＋live/published间接引用并集。仅查历史 published refs 会漏草稿有效 live 边；仅按 ever-published 总禁令则错误改变原生命周期。 |
| D118-02 · P0 · 下架依赖分发含糊且指向旧豁免 | `Ordinary delisting blocks new references and preserves legal existing pins`，验收再次要求保留 existing legal pins | “保留历史 pin/字节”本身一致；若意味着下架后仍可通过原 pin 分发则冲突。裁定 C03、设计 §5.2、API §8 不批准下架依赖分发豁免，A01隐藏坏依赖来源，A34/安装按当前访问/上下架检查失败。改写为保留历史、不保证可再次分发，明确两层语义。 |
| D118-03 · P1 · Connector 审核防漂移未定义 | `Lock-time audience, version/release and lifecycle revalidation`，未规定有效配置观察值 | 裁定 C05、API §2.3 L163：冻结完整图 Connector 的 effective_revision/digest，批准时变化409 dependency_changed；仅新增未批准 editing 不误报。Connector 根提审也记录 effective 基线，首次无 effective 的根才允许 null/null。不能只重验ID/权限。 |
| D118-04 · P1 · 冻结预览与 diff 缺口 | 只要求 frozen publication/review，未给审核详情冻结DTO | API §2.3 L158–164：source_version_id/source_edit_revision/frozen_content_digest、frozen_version 或 frozen_connector、readme_content/frozen_relations、observations、提交时 baseline_version_id/dependency_diff。审核端不扇出 current 补正文；坏快照不能解码失败吞空，详情错误不自动改审核状态。 |
| D118-05 · P1 · 保留审核职责，不再等待 G5 | Out of scope：`extra publishing roles/processes without G5 confirmation`；Current progress：`G5 limits final scope` | 裁定 L12、17、57 和设计 §4.0：角色/owner提交/User-Bot权限/Space自动批准开关和入口已经明确沿用，不是新的待决策前置；自动/人工批准执行相同固定图校验，不加角色、不改成一律人工。 |
| D118-06 · P1 · 影响/候选契约已冻结，不应再猜 | `exact Admin dependents response shape still needs technical contract freezing` | API §2.4 L176–178、A10/A11/A27 已定义 Candidate、标准分页＋meta.has_hidden_dependents、live/published根与via、稳定去重排序、只有完整授权路径进入data/total。隐藏部分仅布尔，不泄漏数量；补按此契约实现和生成物，不另造响应。 |
| D118-07 · P1 · 事务与查询完整性未细化 | 泛称 lock-time checks 和 verified coverage | 设计 §5.3、MP-01 L38、MP-02 的统一锁序/CAS：review→排序Plugin→Version→effective/placement/refs，锁后重验预读身份；无OSS/Fleet锁内I/O。refs不完整是完整性错误，不按空依赖或空影响放行；发布保护不能依赖调用者是否先查 impact。 |
| D118-08 · P0 · 列表/诊断遗漏 | 只有影响和候选，没有 A01/公共计数/mine | 按 X02 补到 MP-02、CL-02、REL-01，而不是给 #118 新加生命周期动作。 |
| D118-09 · P1 · 图/候选读取形状未细化 | Background只提 `remaining T12 candidate metadata`；验收泛称queries/candidates scoped | API §2.4/A03/A10：图以plugin_id＋version_id（Connector为effective修订）标识节点，边有准确source_version_id和pin，二跳读固定成员版本；1500节点/2000边，缺节点全失败。Candidate带published_version_id/version_label/effective_revision/can_select/is_already_bound，无权目标不返回，有权不合格才给安全reason；不能仅改写入pin而保留旧ID-only读取。 |

**一致项：**冻结图锁内重验、Team固定Expert旧版本的间接Skill仍在图中、影响读取不泄漏无权目标、共享目标不随来源删除、不新增 security-disable 或强停实例。

## 9. #119 · 固定解析与安装内容逐项差异

来源：[I1+I2 #119](https://github.com/Mininglamp-OSS/octo-marketplace/issues/119)；当前对应 [MP-03](issues/03-installation-fleet.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D119-01 · P0 · 共存候选被当成已批准能力 | `Stable Fleet installation identity from resource ID and version ID`；要求两类同名Skill `coexist using stable suffixes`，目录/Fleet/SKILL.md名一致 | 裁定 §6、API §7.1 L420、MP-03 L20/L36：同名/多版本共存 deferred，不默认采用 suffix/frontmatter改写，不把真实双版本加载作为本期完成前置。普通无冲突安装及现有同名不同内容冲突仍要验证；不新增市场发布/查询拒绝策略。 |
| D119-02 · P0 · 完整读取接口缺失 | Goal 只有 portable install plan，Out of scope 排除 standalone-skill install API | 补 X01：A34 是无副作用内容读取，不是 standalone-skill mutation，支持Skill/Connector单根既有本地入口；不能因“安装 API 非目标”排除读取。resolver先输出完整原市场内容，再单独Fleet序列化。 |
| D119-03 · P1 · 一致性及有效配置选定未细化 | `Connector ID selects current effective MCP configuration` | API §6.1 L355、MP-03 L18：短只读事务同时固定根/refs/完整 effective 行，退出后读不可变文件，不能旧revision晚读新payload；不混入未批准编辑，不在DB锁内做OSS/Fleet I/O。 |
| D119-04 · P1 · 合并与预算只给原则 | `deterministic merging`、`existing payload constraints` | 设计 §7.1、API §2.4/§7.1：每Expert按自身关系集合合并，重复server key冲突；无MCP省略 mcp_config，不发空mcpServers。图1500节点/2000边，Team≤30、每Expert Skill/Connector各≤20、固定Skill≤600、支持文件每Skill50/全图500、单文本1MiB、最终完整JSON16MiB；逐步累计，不能裁剪适配。Fleet字段/角色/文件source另按既有协议映射，市场图不改写。 |
| D119-05 · P1 · Secret 持久化边界未细化 | 只写 `errors/logs do not contain secret values` | MP-03 L21–24、API §6～7.2：plan也必须无运行时凭据，不能只去掉custom_env却保留MCP硬编码秘密；发现疑似凭据只报字段位置。首次plan固定完整非Secret内容，重试由用户重给相同Secret，不能存可恢复秘密的请求体。 |
| D119-06 · P0/P2 · 共通差异 | delist-only/restore排除；PR3未开 | G02/G03/G04。版本化resolver复用MP-01/02；不因#119排除retry persistence而漏掉#120/MP-03统一状态机。 |

**一致项：**published根→固定Expert/Skill pin，不用latest；文件/授权/摘要失败在Fleet写入前阻断；Connector按ID读有效态；不改Fleet/Daemon协议、不同ID重复server key不覆盖、保留每个Expert的精确绑定。

## 10. #120 · 安装接入、安全重试与运行验收逐项差异

来源：[I3+I4 #120](https://github.com/Mininglamp-OSS/octo-marketplace/issues/120)；当前对应 [MP-03](issues/03-installation-fleet.md)及 [REL-01](issues/08-cutover.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D120-01 · P0 · 保留两个执行器和补偿 | Goal `add bounded retry/compensation`；`new capability-install entry and legacy create/bind entry use the same resolved content`；失败清理本attempt创建物 | 裁定 A04、设计 §7.3、MP-03 L25/L46：两URL只保留adapter，内部同一InstallCoordinator、一次Fleet原子mutation。删除市场legacy create/bind/delete补偿执行路径，不是共享相同内容后继续两个执行器；“只删除自己创建的Skill”仍可能删掉并发共享绑定。独立非市场Expert/Squad仍有caller的共享实现不误删。 |
| D120-02 · P0 · Connector更新使旧key冲突 | Acceptance：`changed input or changed connector configuration conflicts` | API §6 L343–345、设计 §7.2：改变调用者输入应冲突；**仅市场Connector/root后来更新不能重建或拒绝原计划重试**。同key从原plan恢复原完整请求；新key新意图才使用新有效配置。须把输入变化与市场live变化拆开。 |
| D120-03 · P0 · 共存被设为当前必需 | 必须安装two same-name/different skills并证明stable suffix/runtime discovery | 同 D119-01，改为普通无命名冲突真实Fleet/Daemon验收；条件性共存验证只在产品另行批准后执行。实际运行证据仍必需，不能把整个Daemon验收一起取消。 |
| D120-04 · P0 · “请求不变”与已批准breaking不符 | `Existing request/response ... unchanged` | Fleet wire保持不变、成功agent_id/squad_id保持不变；但API §1.2/§6已明确旧A32新增必填Idempotency-Key、仅用户、workspace/runtime UUID，A31单Workspace头；同key跨URL同域，header/body workspace冲突400。改写为保留URL/成功映射并同步必要新约束，不保留无幂等旧调用。 |
| D120-05 · P1 · 安全窗口/未知结果不充分 | `install-attempt persistence; remote outcome uncertainty and retry semantics`，只禁 uncertain后切入口 | API §6/§7.2：从首次可能发送起保守≤24h，同key/原plan/同完整请求/同Secret；无参数或超窗零自动POST。UNKNOWN不因后续4xx、资源名称或本地无记录改为“未提交”；不换key、不删attempt，不新增查询、不改Fleet过期重占/cleanup/保留。补原文没有定义的必要边界。 |
| D120-06 · P1 · 状态机/成功回放和计数缺口 | 只列root/version/adapter/fingerprint和attempt persistence | MP-03 L22–29：PREPARED→DISPATCHING→SUCCEEDED/FAILED/UNKNOWN，CAS/租约；中断不回PREPARED、不延窗口，旧错误不覆盖晚到成功；先查成功并核对原身份及非Secret intent，成功回放不要求根仍上架/重填Secret，显式给Secret仍验HMAC；Fleet结果完整性验证通过后首次成功与计数同事务，重试不重复计数。 |
| D120-07 · P0 · 读取/客户端验收遗漏 | runtime验证只围绕安装服务端两入口 | 补 X01/X03：读取单GET与完整文件独立smoke、客户端真实意图持久化/IPC状态、旧调用链退役及四端验收，不能用安装结果反证读取已实现。 |

**一致项：**准备失败零Fleet写、未知不能盲切入口、不持久化Token/Secret/含凭据请求体、升级不改变旧pins、fixture MySQL不能替代未修改Fleet/Daemon真实环境。

## 11. #121 · 只读盘点逐项差异

来源：[M1 #121](https://github.com/Mininglamp-OSS/octo-marketplace/issues/121)；当前对应 [MP-04](issues/04-migration.md)的前置子能力。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D121-01 · P1 · object验证是明确有限子集，不等于完整就绪 | Out of scope：`object-store existence/digest verification`；`object_verification=not_checked` | [设计 §8](01-technical-design.md) L282、[MP-04](issues/04-migration.md)要求文件可取性/内容证据，API §5冻结preview必须重验完整来源和文件。M1元数据工具可以保留not_checked，但它不能证明资产完整或可apply；把独立对象验证/后续preview证据明确归给MP-04、REL-01，缺证据不可放流。这是覆盖缺口，不是只读工具禁止写入的冲突。 |
| D121-02 · P1 · 新迁移计划缺少直接衔接 | 只要求 `M3 migration candidates/exceptions can be reconciled with #123` | API §5 L274–298：inventory要能完整枚举稳定source_node_key/origin、源图/范围、文件摘要和安全候选，后续由两次A29冻结完整proposed_plan/plan_digest；不能凭同名或唯一候选自动认定映射。补到MP-04统一服务，不另造CLI权限旁路。 |
| D121-03 · P1 · 历史发布/索引证据还需验收化 | `Unknown release/pin/history is reported without guessing` | MP-01旧pending处置、MP-04和设计§8要求把可证明release、可证明refs与未知项分别处理，未知阻断新分发，不能仅输出报告后继续切读；旧快照不补latest、不自动宣告release。保留原正确原则并补逐源资格/排除证据。 |
| D121-04 · P2 · 独立阶段与进度 | M1可提前，工具/sample已部分打勾，后续在PR3包含 | 可提前只读研究与本地索引一致，MP-04整体完成仍依MP-02；本记录未重跑3a98150/c2b296a，勾选是远端历史声明，不是本次验证结论。分布扩展仍未勾，不能宣称全部M1完成；PR3未开描述按G04更新。 |
| D121-05 · P0 · 附带规则不应污染盘点 | 末尾重复delist-only、restore排除 | G02/G03。只读盘点应如实报告现有数据，不按旧总禁令修改/重新解释生命周期。 |

**一致项：**SELECT一致性快照/rollback、只读账号拒写、不启动迁移、不输出凭据/正文/storage key、不猜release/pin、private/space/system×生命周期×embedded/top-level分布、真实环境访问另授权。该项核心与当前方案最接近。

## 12. #122 · 放行与回滚逐项差异

来源：[Enable #122](https://github.com/Mininglamp-OSS/octo-marketplace/issues/122)；当前对应 [REL-01](issues/08-cutover.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D122-01 · P0 · 放行依赖缺失 | Requires只列#115～#121与#123，Readiness是PR3一部分 | REL-01 L9–10、L93明确五个直接能力依赖及四端真实surface；补X03，不把readiness代码随PR3提交等同联合验收或生产就绪。三PR冻结要求按G01调整。 |
| D122-02 · P0 · legacy path边界需限定 | 验收要求未转换资源有 `their valid legacy path or explicit rollout exclusion`，负载项说legacy compatibility | 若指新线上继续分发/安装旧市场模型，与裁定A04/§3、REL-01 L67–81冲突：未转换来源阻断全部市场分发，首轮新链路试点前旧市场执行器/fallback必须不可达。若只指历史读取/迁移reader则可保留，应明确限定；独立非市场业务不误删。删除“旧市场执行路径”选项，保留历史证据和逐源排除。 |
| D122-03 · P1 · 业务flag门禁不足 | `Verify default-off compatibility`、`actual flag enabling` | 设计§8 L292–294、REL-01 L73–81/L85–89要求真实gateway/部署门禁，覆盖全部市场写入/发布/批准回调/apply/安装、直连和后台发送，记录命令、操作者、制品和拒绝/放通证据。不能假设源码已有所需业务开关；已有flag可作为额外手段，不替代部署隔离。 |
| D122-04 · P0 · 回滚验收可被降级 | `Rehearse data-safe rollback ... or document a tested forward-fix-only boundary` | REL-01 L85–89/L102要求新模型兼容二进制/客户端、保留schema/对象/attempt/map，演练阻断、内容新版本恢复和放流；不能以只记forward-fix边界免除当前可操作兼容回滚验收。Flag-off不等于回滚，这条远端警示保留。 |
| D122-05 · P1 · 受控审核/旧pending/UNKNOWN处置遗漏 | 泛称rollout order/recovery commands，未规定可执行冻结窗口流程 | REL-01 L30、73–81：先隔离新副本、停旧写入/发送，受控开放迁移所需既有审核/回调；不能冻结所有approve却等待waiting_review完成。旧pending转换或fence，未决attempt保留UNKNOWN；实际无法只放通原意图重试时保持关闭，不用管理员代装或新GET。 |
| D122-06 · P0 · 新读取/列表/独立可见未纳入全端证据 | 仅概括 `consistent ... behavior across active readers/writers` | 补X01/X02和REL-01 L34–61/L95–100的真实Web/CLI/Buddy单GET、COUNT/facets、mine三tab/隐私/恢复、递归拆分后A01与Web/Buddy独立市场可见。数据库/目录/测试绿都不能替代。 |
| D122-07 · P0/P2 · 旧范围和进度 | 共存随I4必验、restore排除、PR3未开 | 共存按D119-01条件化；恢复按G03；进度按G04。不得因此删除普通Daemon文件/绑定/MCP/技能发现真实smoke。 |

**一致项：**实际生产启用另授权，异常必须处理或隔离，fixture不替代真实证据，flag-off本身不是回滚，不删历史表/文件，不强停既有实例。

## 13. #123 · 存量迁移逐项差异

来源：[M3 #123](https://github.com/Mininglamp-OSS/octo-marketplace/issues/123)；当前对应 [MP-04](issues/04-migration.md)、[API §5](02-api-contracts.md)。

| 编号 / 类型 | 远端原文或明确范围 | 当前依据、影响与修订建议 |
|---|---|---|
| D123-01 · P0 · 明确删除当前必需协议 | `only existing rows are migrated`；`no legacy_import endpoint is added`；替代removed M2 | API L234–235、274–310：A29 preview/A30 apply兼容file或source_plugin_id二选一，HTTP/CLI复用统一领域服务。普通schema2导入仍拒旧包；旧ZIP通过显式批准转换，不自动升级。必须恢复转换范围而非只交数据库stock工具。 |
| D123-02 · P0 · 私有/下架目标迁移豁免冲突 | `private published stays private published`；拆出目标镜像listing；`may create pins to released-but-not-listed targets ... audited system operation` | 裁定§4、API §5 L272/L292、REL-01 L50/L76–77要求不扩大范围、遵守引用与当前上下架资格。不能靠system operation绕过private目标禁用或建立下架目标分发豁免。原private历史保留，无法同时满足合法已发布依赖与原受众的来源blocked；不为完成迁移公开旧private资产。 |
| D123-03 · P1 · 标签建议与冻结授权未区分 | `previous label patch+1`；无法解析/占用用 `deterministic forward label` | API L289/L291/L298/L300：每个create/update_source的版本化节点须显式version_label，绑定冻结plan_digest，apply不替换。patch+1/forward label若仅作preview建议可兼容；若执行时自动决定或冲突后自行换则不兼容。须限定为候选建议，经完整preview及批准后使用；标签冲突重新批准，不能用旧摘要执行新标签。 |
| D123-04 · P1 · Connector 拆组边界不同 | 默认 `one per old expert's mcp_config`；将G2视为one-connector已决 | 裁定§4 L63、API L288/L294：默认保留整个server集合，与“一份配置一Connector”大方向兼容；但允许批准计划显式拆成多Connector，组完整覆盖、无重叠/额外server。不按URL或名称猜映射。补批准拆组能力，不能固定成永不允许拆组。 |
| D123-05 · P1 · 复用授权/子图身份未细化 | `reuse only when content hash, owner, Space and visibility are identical` | API L290/L296–298：上述条件是必要检查之一，远端未明确宣称“满足就自动复用”，不据此判反向规则。但reuse仍必须明确目标Plugin/精确已release Version；Connector绑定观察effective_revision/digest。Expert复用还须真实子图目标ID/pin完全一致；子节点待create而真实ID未知时不能确认复用该Expert。补人工映射与完整身份校验，不以hash替代。 |
| D123-06 · P0 · 回滚直接恢复指针 | `restores pointers/flags`，验收 `pointer rollback` | 设计§8 L294、REL-01 L87–89：只退兼容新模型二进制，内容回退从完整历史生成新引用式草稿/新合法发布版，不直接回切published指针、不恢复embedded/inline。修改回滚策略并实际演练，保留原历史、新行/文件/map。 |
| D123-07 · P1 · 批次清理边界未细化 | `A partial failure cleans up only objects created by this batch` | 远端未明确要求删除已提交共享资源，不据此判实现破坏。MP-04/设计§8、API L302–308仍要求补足：多节点不是整批原子，保留已提交目标/checkpoint/map，只恢复未提交步骤；清理仅限本次新建且确实无引用的上传对象，须检查跨资源current/历史/pending/plan，不能只凭“本批创建”补偿删除。 |
| D123-08 · P1 · 缺两次只读冻结计划及身份 | `dry run produces a mapping and exception report`，只有mapping table/batch id | API L274–300：discovery不可apply；完整proposed_plan经第二次preview冻结，create/reuse/update_source、source/origin节点、符号边/标签/范围/server分组都入摘要。DB source_identity=plugin_id@version_id、ZIP为原字节SHA256，source_scope绑定root owner/Space/visibility，root DB用update_source保持ID，ZIP根create。补全不是另造简单dry-run格式。 |
| D123-09 · P1 · 并发/恢复/审核语义不足 | `re-running skips mapped resources`、中断重跑无重复 | API L302–308：同key关联原run，不同key同计划也由root map唯一仲裁；目标创建与map更新同事务。source_commit_json记录自己的已切换输出，恢复不能拿原CAS对自己新current再切一次；新current不被覆盖。waiting_review/blocked非终态，同key能续跑，拒绝取消不自动重提，真正completed才固定终态和回放期。 |
| D123-10 · P1 · 完成状态与独立可见不充分 | `standalone`、`independent, released and pinned`，未验A01/Web/Buddy独立展示；draft/private表述与release验收也需协调 | 裁定§4、API L272/L306、REL-01 L76/L100：旧内嵌Skill/Expert（含Team成员内的Skill）递归拆成对应system/Space市场独立可发现资产，不能继续embedded隐藏或建private依赖。只有全部节点、来源切换、正式发布、列表资格就绪才completed；待审202，部分已提交后失败409 migration_incomplete，保留逐源异常。真实Web/Buddy由REL验收，MP-04先验列表资格。 |
| D123-11 · P1 · Connector“released and pinned”措辞不准确 | 验收统称 `Extracted skills/connectors are ... released and pinned` | API §2.1/§5：Skill/Expert有release与固定版本，Connector只有有效配置、关系只ID；计划中的effective_revision/digest是批准观察值，不是业务版本pin。改成分别验收，避免给Connector造Version或target_version_id。 |
| D123-12 · P2 · 进度/跨项旧规则 | `Not implemented; PR 3 is not opened`，尾部重复delist-only/restore排除 | G02/G03/G04；不把PR126的本地演练声明当本次证据，不默认认定其满足本表。历史refs只对可证明pin建立，不能伪造历史release；这条远端正确要求继续保留。 |

**一致项：**完整stock extraction而不是只backfill索引；先inventory、另行授权apply；Skill/Connector→Expert→Team拓扑；owner/Space不擅改；保留角色/顺序/Leader；历史文件/快照不改写；缺证据不猜latest；映射、重跑和隔离演练必需；不修改Fleet/Daemon，不自动生产执行。

## 14. 修订远端说明的落地顺序

1. **先统一会改变实现的规则：**private目标、Admin矩阵、生命周期、A09恢复、共存deferred、唯一安装引擎、原plan重试、显式迁移协议和回滚。删除共同旧段落中的相反要求，避免单改Acceptance但页尾继续冲突。
2. **补齐全项目范围与所有权：**把X01/X02/X03纳入总览和验收映射，按plan.json关联MP/CL/REL，不用PR个数代替范围。
3. **冻结执行契约：**各远端任务直接链接当前API，补CAS/relations、冻结审核、refs完整性、attempt状态、完整migration plan/run/checkpoint；不再写“technical contract待定”或客户端自行推导。
4. **最后更新追踪状态：**换成实际承载PR与完成证据，保留历史review链接；closed项若缺当前能力，按实际代码核验后决定补充issue还是重开，不仅凭本报告自动重开或关闭。

本次只生成差异记录，没有修改GitHub正文、重开/关闭issue、调整PR、承诺生产切换或判定现有代码验收通过。

## 15. 本次验证口径

- 已取得并比较#114–#123完整正文；覆盖连续十项，全部评论计数为0；没有用截断正文判断遗漏。
- “明确冲突”引用远端实际语句；“遗漏”只表示当前正文未覆盖，不断言实现不存在。未额外读取正文所指向的分支task brief；本记录不声称该分支内部所有文件也都遗漏相同内容。
- 按当前工作区文档核对本地章节、路径与接口owner；远端PR只用于校正元数据，不以PR测试自述作为实现证据。
- 本次没有业务源码修改，因此不运行服务、数据库、Fleet/Daemon或UI smoke。文档链接、逐项覆盖及引用检查与任何服务验收分开，不冒称运行时验证通过。
- 文档校验已运行：本记录和README的本地链接目标均存在，README入口有效；#114–#123十个逐项章节连续覆盖，73条逐项记录编号无重复；逐项表格来源栏中无省略号的反引号短引用均与相应远端完整正文匹配。73条包含共通差异回指、缺口和进度项，不代表73个独立冲突。该检查不代表外部URL可用性或业务实现验收。

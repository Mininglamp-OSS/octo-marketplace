# PR #124–#127 实际实现 × v2 方案差距与待拍板决策

> **2026-10-26 14:55 更新：** 用户已锁定 **B 模型**（Expert→Skill、Team→Expert 都解析最新；A09/A29-A30/共存不做；private 同 owner 允许；删除按并集保护；D4 四字段；D7 effective；D8 CAS；D9 A32 保留 URL / 新 key 最新）。本文的“建议”保留为分析背景，**已不再代表当前建议**；当前口径以 [决策记录](../decisions-20261026.md) 与 [00-decisions](../00-decisions.md) 为准。

核对时间：2026-10-26 14:00 +08。范围：**只看你的开发需要改什么、你需要拍板什么**，不再重复差异记录里“远端 issue 文案该怎么改”的部分。

## 0. 基线与结论

| 项 | 值 |
|---|---|
| 已合并 | PR **#124**（`52e4d91`，版本/发布底座）、PR **#125**（`c9d2a84`，引用/审核/保护） |
| 待合并 | PR **#126** 最新 `639d1f1`（在 `94de531` 上增加迁移审计/索引；仍无 A34/Coordinator）；PR **#127** 最新 `016baef`（在 `d7a1ffe` 上修复了 Team 固定成员读取；仍是 `dependency_issues`） |
| 方案 | `docs/marketplace-v2/`（已入仓库工作区，待提交） |
| 关键前提 | 三个业务开关默认 `false`：`PLUGIN_VERSION_MANAGEMENT_ENABLED`、`PLUGIN_REFERENCES_ENABLED`、`PLUGIN_PINNED_INSTALLATION_ENABLED`；下文“已实现”均指开关打开时 |

**一句话结论：** #124/#125 交付的是“版本底座 + 引用骨架”，#126 交付的是“固定版本安装 + 盘点/迁移 CLI”，#127 交付的是“公共列表按依赖过滤”。它们**没有实现方案中的 A09、A34、A29/A30、CAS、Connector effective、A01 诊断 DTO、单一安装引擎、原 plan 重试、客户端接入**；同时有几处实现方向与方案相反（删除生命周期、private 目标、共存命名、迁移回滚）。所以你的开发分两类：**约 60% 是纯代码补齐，约 40% 要先做产品/架构决策**。

## 1. PR #124–#127 实际实现了什么

| 方案能力 | PR #124 | PR #125 | PR #126 | PR #127 |
|---|---|---|---|---|
| 不可变版本/文件快照 | ⚠️ 有快照/artifacts，但无 `declared_visibility/content_digest/revision_no/restored_from` | — | — | — |
| release / published 指针 / SemVer | ⚠️ 有 release/SemVer/单调标签；publish 无 `version_id`，重复 approve 409 | — | — | — |
| CAS / relations 必填 | ❌ 全树无 `expected_edit_revision`；relations 省略当清空 | ⚠️ 关系校验有，但 null/省略仍宽 | — | — |
| 历史受众交集 | ❌ 只用当前 visibility + 有 release | — | — | — |
| A09 restore-to-draft | ❌ 明确排除 | — | — | — |
| Connector effective | ❌ 无表/修订/摘要 | ❌ 无观察值 | ❌ 安装读 live | — |
| 冻结审核 DTO | ⚠️ 只有 frozen_relations/readme | ⚠️ 无 observations/diff/baseline | — | — |
| 版本化读投影/view/VersionSummary | ❌ 无 view/VersionSummary/selected/published 字段 | — | — | — |
| pin/refs/Team 递归 | — | ✅ 主链已做 | — | — |
| 引用矩阵 | — | ❌ 允许 private→同 owner private | ❌ 安装/迁移允许 delisted pin | — |
| 删除保护 | — | ❌ 曾发布一律拒删（总禁令） | — | — |
| A01 公共依赖过滤 | — | — | — | ✅ SQL 前置/COUNT/facets；但遗留 legacy live fallback、无完整性检查 |
| A01 mine 诊断 | — | — | — | ⚠️ `dependency_issues` 旧 DTO；无 `view`、无 editing 根、无匿名聚合 |
| A03/A10/A11 | — | ⚠️ 旧图/旧 DTO/旧路径 | — | — |
| A25 ZIP-only/receipt | ❌ A24 未拒绝 Expert/Team | ⚠️ ZIP 解析有，无 Key/CAS/resolved/receipt | — | — |
| A34 完整内容读取 | — | — | ❌ 0 命中 | — |
| 单一安装引擎 | — | — | ❌ A31 pinned / A32 legacy 两套；A32 无 Key/用户/UUID | — |
| 原 plan 重试/状态机 | — | — | ❌ 重试重读 live，漂移 409；只有 `completed` 布尔 | — |
| 同名/多版本共存 | — | — | ⚠️ 已实现 `mk1` + frontmatter 改写（未批准） | — |
| 只读盘点 | — | — | ✅ 核心可用；缺稳定 node_key/scope/文件证据 | — |
| A29/A30 迁移 | — | — | ❌ 只有 CLI 一遍计划 | — |
| 迁移回滚/审核/独立可见 | — | — | ❌ 指针回滚、直接发布、无 waiting_review | — |
| 客户端/REL | — | — | — | — |

## 2. 开发必须修改的部分（按 PR 归属）

### 2.1 PR #124 留下的（版本/发布底座）

| # | 缺口 | 方案要求 | 具体改什么 | 依赖决策 |
|---|---|---|---|---|
| 1 | A09 restore | 新 version_id、复制旧文件/pins、CAS、不动 published | 新增 route/handler/service/repo + `restored_from_version_id` + OpenAPI + CL-02 | D2 |
| 2 | CAS/relations | `expected_edit_revision`、缺失 409、relations 省略 400、[] 清空 | `edit_revision` 列、wire 字段、`write.go:Update`、`upsertRequest.Relations` 指针、import CAS | D8 |
| 3 | 快照身份 | label/declared_visibility/关系身份变化也新建版本 | `declared_visibility/content_digest/revision_no` 列 + snapshot codec + 复用键 | D8（相关） |
| 4 | 历史受众 | 当前受众 ∩ 版本 declared_visibility | `versionAccess`、A05/A06/A07/A08 加交集；private v1 不因 v2 公开泄露 | 无（安全，直接做） |
| 5 | Connector effective | 有效行/修订/摘要、编辑与生效分离 | 新表 + 写/审批事务 + 读投影 + 安装读 effective | D7 |
| 6 | 冻结审核 DTO | source_version_id/frozen_content_digest/frozen_version/observations/baseline/diff | 扩表/DTO；坏快照 500；approve 回放 | D7（部分） |
| 7 | 读投影 | view、VersionSummary、selected/published/edit_revision/has_unpublished、A04 scope、A06/A07 version_id、A08 JSON | 统一读模型 + handler/OpenAPI | 无 |
| 8 | 发布 wire | publish 必填 version_id、响应 published_version_id、approve 幂等 | `listing.go`/`version_management.go`/`review.go` | 无 |
| 9 | Admin | A24 拒绝 Expert/Team JSON；A21 支持 view=effective | `admin.go`/handler/OpenAPI | 无 |
| 10 | parse-task 导入 | 保存原 ZIP + 全部支持文件 | `import.go`/`skill_tree.go` | 无 |

### 2.2 PR #125 留下的（引用/审核/保护）

| # | 缺口 | 方案要求 | 具体改什么 | 依赖决策 |
|---|---|---|---|---|
| 1 | private 目标 | 一律禁止 private | 删 `references.go:55` private 分支、`reference_reads.go:141-144` 候选 | D6 |
| 2 | 删除保护 | live ∪ published refs/固定 Expert 间接 | 去掉 `rejectPublishedMutation` 总禁令；`Delete/DeleteGraph` 用并集 | D1 |
| 3 | A01 诊断 | `dependency_availability` + view + 匿名 | 重做 #127 DTO/查询/授权 | D4 |
| 4 | A25 receipt | Key、输入摘要、7 天回放、同事务 | 新表 `plugin_mutation_receipts` + 服务层 + handler | 无 |
| 5 | 冻结观察值 | Connector observations / dependency_changed | 与 D7 一起 | D7 |
| 6 | A03/A10/A11 | 版本节点/边、1500/2000、Candidate/Dependent 字段 | `service.go` graph、`reference_reads.go` DTO、`repo.go:106` | 无 |
| 7 | refs 完整性 | JSON_TABLE 双向比对；等数量错 pin 也 500 | `references.go:224-292` + list/tags/history 前置 | 无 |
| 8 | 严格 wire/限制 | null connector pin 拒、relations 省略 400、member_key 正则/role/sort_order/20-30 | `buildRelations`/`ValidateReferenceMetadata`/handler | D8 |
| 9 | 锁序/重试 | review→Plugin 排序→Version→effective；≤3 次 | `review.go`/`references.go`/`classifyDeadlock` | 无 |
| 10 | A29/A30 | 显式旧包转换 | 与 D5 一起 | D5 |

### 2.3 PR #126 留下的（安装/盘点/迁移）

| # | 缺口 | 方案要求 | 具体改什么 | 依赖决策 |
|---|---|---|---|---|
| 1 | A34 | 单 GET 完整固定图/文件/JCS/16MiB/no-store | 新端点 + DTO + 共享 resolver + OpenAPI + CL-02/03 | 无（已确认） |
| 2 | 单一引擎 | A31/A32 共用 Coordinator/attempt | A32 接 pinned engine；删市场 legacy 补偿 | D9 |
| 3 | A32 契约 | Key/用户/UUID/header-body 一致 | handler/service/OpenAPI | D9 |
| 4 | 原 plan 重试 | 持久化 plan/请求/HMAC，不重读 live | attempt 表/状态机/lease/UNKNOWN；重试只读原 plan | D9 |
| 5 | 成功回放/计数 | 本地回放、仅首次成功计数 | attempt 查询 + 同事务计数 | D9 |
| 6 | 共存命名 | deferred，不默认改写 | 默认关闭 `mk1`/frontmatter；迁移不依赖 | D3 |
| 7 | 迁移协议 | A29/A30、两次冻结、root map、waiting_review、completed | 新端点/表/状态机；CLI 作为 adapter | D5 |
| 8 | 迁移回滚 | 不指针回切 | 改 forward-fix/新版本；保留历史 | D5 |
| 9 | private/delisted | 禁止 private；下架不新装 | `install_read.go`/`stockmigration` 按 listing+矩阵 | D6 |
| 10 | 盘点证据 | 稳定 node_key/origin/scope/文件摘要 | inventory 扩展 | 无 |
| 11 | 灰度/REL | 部署隔离、全入口阻断、四端证据 | REL-01 runbook + gate | D10 |

### 2.4 PR #127 留下的（A01 过滤/诊断）

| # | 缺口 | 方案要求 | 具体改什么 | 依赖决策 |
|---|---|---|---|---|
| 1 | DTO | `dependency_availability` 四字段 + 固定 reason | `model/plugin.go`、handler DTO、service 填充 | D4 |
| 2 | view | mine 仅 editing、公共仅 published、Connector effective；非法 400 | handler/service/filter | D4 |
| 3 | 评估根 | 有 published 始终诊断发布根；仅从未发布才 editing | `DependencyIssues` 去掉 listing 过滤 | D4 |
| 4 | 原因安全 | 逐项授权；不可披露合并一条匿名；不泄漏数量/身份 | repo 授权 + DTO | D4 |
| 5 | Team pin | 用边上 `target_version_id`（**016baef 已修**） | 保持；补回归 | 无 |
| 6 | 完整性 | refs↔relations_json 双向 500 | list/tags/history/diagnostic 前置 | 无 |
| 7 | detail/graph | 方案未要求 404/root-only | 回退或写入方案 | D4 |

### 2.5 完全没有对应 PR 的

- CL-01（Admin 接入）、CL-02（Web/CLI）、CL-03（Buddy）、REL-01（四端/部署/回滚）。
- 本地 octo-web/octo-cli 仅做关键词检查，未见 A34/版本协议接入；buddy 仓库未找到。

## 3. 需要你拍板的决策（详细版）

### D1 · 已发布资源到底能不能删除

**一句话：** published/曾发布资产是“只能下架、永远不能删”，还是“没有 live/published 引用就可以删”。

**方案要求：** 保留既有删除/下架/访问/投放动作；删除保护 = live incoming（含草稿、不过滤 listing）∪ published refs ∪ 固定 Expert 间接引用；没有“曾发布即永不删除”的总规则（G02、C03、API A19）。

**PR 实际：** #125 的 `rejectPublishedMutation` 在 `references` 开关打开时，对任何有 release、listing=published/delisted、或有过 approved review 的资源直接 `ErrConflict`；`Delete/DeleteGraph` 都调用（`references.go:254-273`，`write.go:838-862,939-1019`）。也就是**已经实现了远端 #118 的 delist-only**，不是方案。

**为什么必须你定：** 这不是代码风格，是产品生命周期和用户数据权。选了就不能反复改，而且会影响 Admin/Web 的删除按钮、审计、客服话术和已安装用户。

**选项：**

- **A（方案口径）**：删除前只检查 live incoming + published refs + 固定 Expert 间接引用；没有这些引用就可以删。
  - 改动：去掉总禁令；`Delete/DeleteGraph` 改为并集查询；补“草稿 live 边仍保护”“current 去边但 published 仍引用”“Team→旧 Expert→旧 Skill”测试。
  - 影响：已发布但无人引用的资产可以被 owner 删除；删除后已安装的 Agent 可能在下一次解析/安装时报错，因为**安装记录不在 `plugin_relations` 里，删除保护看不到已安装用户**。
  - 风险：中高；需要额外的“是否被安装/有 attempt”保护才能安全。

- **B（远端/现状口径）**：published/曾发布只能下架，不能删；只有从未发布的草稿可以删。
  - 改动：代码基本不动；改方案 G02/C03、API A19、CL/Admin 文案；删除按钮对已发布资产禁用/改为下架。
  - 影响：最安全，已安装用户不受影响；但 owner 永久失去清理已发布资产的能力，需要“下架 + 归档”运营流程。
  - 风险：低；产品体验变化。

- **C（折中）**：已发布资产允许删除，但必须同时满足“无 live/published 引用 + 无安装 attempt/安装记录”；否则只能下架。
  - 改动：A 的并集 + 检查 `plugin_install_attempts`/安装记录；需要定义“安装记录”口径（attempt 可能只保留 24h）。
  - 影响：兼顾清理与安全；但引入新的保护维度，方案没有定义，测试量大。

**建议：B。** 理由：#125 已合并且测试按 B 写；删除保护看不到已安装用户，A 的实际风险比文档写的高；远端 #118 也已经是 B。选 B 的代价只是改方案/文案，不是改代码。如果你确实需要清理能力，选 C，不要选纯 A。

**不决定的后果：** Web/Admin 删除按钮行为与方案文档继续相反；后续做 A34/安装时无法确定“删除后已安装用户怎么办”。

---

### D2 · 要不要做 A09 restore-to-draft

**一句话：** 用户能不能把某个历史版本“恢复成新草稿”，继续编辑后再走发布/审核。

**方案要求：** A09 是 MP-01 必需：owner 携 `expected_edit_revision` + 新标签，复制旧版本完整内容、文件、pins 为**新 version_id** 的 editing 快照；不改 published、不直接上线；Connector 400（G03、API §3.1、MP-01 验收）。

**PR 实际：** 全仓库没有 `_restore` 路由/handler/service；`docs/api/plugin-version-management.md:75` 明写 “Restore-to-draft is not part of this release.”。远端 #116 也明确排除。

**为什么必须你定：** 这是功能范围，不是实现细节。它决定 MP-01/CL-02 的验收是否包含恢复，也决定历史版本页要不要做“恢复”按钮。

**选项：**

- **A（实现）**：按方案做新版本恢复。
  - 改动：route + handler + service + repo；新 `restored_from_version_id`（或复用现有字段）；复制 artifacts/relations/pins；CAS；OpenAPI；Web/CLI 后续接。
  - 影响：用户可恢复历史；文件对象可能被复制/引用，需要确认存储复用策略；标签冲突要 409。
  - 工程量：M（后端 1 个端点 + 测试；UI 另算）。

- **B（descope）**：正式不做。
  - 改动：从 MP-01 的 owns_api_ids/A09、API §3.1、plan.json、CL-02 验收中删除；保留历史读取 A04–A08。
  - 影响：用户只能查看/下载历史，不能恢复；客服/运营需知道。
  - 风险：低，但必须同步所有文档，不能一边写必需一边不实现。

- **C（先做后端、UI 后置）**：实现 A09 API，但 CL-02 本期不做按钮。
  - 影响：API 完整，客户端后续接；适合后端先行的排期。

**建议：A 或 C。** 方案已把 A09 写成确认范围，且恢复是版本化的核心价值之一；如果本期确实排不进，选 B 并彻底删掉所有“必需”表述，不要留半套。

**不决定的后果：** MP-01/CL-02 验收无法签字；历史版本功能永远缺一块。

---

### D3 · 同名/多版本 Skill 共存与安装命名

**一句话：** 安装到 Fleet 时，是否给每个 Skill 生成稳定别名（`-mk1-...`）并改写 `SKILL.md` 的 `name`，从而让同名/不同版本可以共存。

**方案要求：** 共存 **deferred**，未批准、真实 Fleet/Daemon 未验证；候选算法只用于临时安装副本，不默认采用；A34 返回市场原件，不生成 `installation_name`、不改写 frontmatter；普通无冲突安装必须真实可用（C07、§6、§7.1、D119-01）。

**PR 实际：** #126 已经默认启用：`SkillInstallAdapterVersion = "mk1"`，每个 Skill 安装名 = slug + `-mk1-` + SHA256 前 32 位（`install_primitives.go:17,123-150`），并改写 `SKILL.md` frontmatter `name`（`:168-231`）；迁移校验也用它（`stockmigration/plan.go:403,480`）；测试断言 `-mk1-` 和改写后的 SKILL.md（`plugin_migration_installation_test.go:52-58`）。它不只用于冲突，而是**每个 Skill 都改名/改写**。

**为什么必须你定：** 这会产生 de facto 标准：目录名、Fleet Name、绑定、SKILL.md 内部名全部变化；客户端/用户看到的名字、Skill 自引用、升级路径都会受影响。方案说 deferred，代码却已经做了。

**选项：**

- **A（默认关闭/移除）**：普通安装保留市场原名和原字节；同名冲突明确失败 `same_install_identity_different_content`；迁移校验不依赖 `mk1`。
  - 改动：`PreparePinnedSkills` 去掉默认改名/改写；仅冲突时按方案候选生成；迁移去掉 `mk1` 依赖；更新测试。
  - 影响：符合方案；同名不同内容无法共存（方案接受）；用户看到的名字稳定。
  - 风险：低；但 #126 的现有测试/演练要改。

- **B（批准共存）**：保留 `mk1`，但按方案候选算法对齐（`mk-` + 32 hex of plugin_id NUL version_id NUL variant），补真实 Fleet/Daemon 双版本加载证据，更新 C07/§6/§7.1 和客户端文档。
  - 影响：可以共存；但需要真实运行证据（同 Workspace 两专家分别拿 v1/v2、支持文件、共享扫描根不混版）；否则就是拿未验证行为上线。
  - 风险：高；frontmatter 改写可能破坏 Skill 自引用/脚本。

- **C（仅冲突时启用，默认关闭）**：新增独立 flag（默认 false），只有同名冲突且显式开启时才生成 `mk1` 副本；普通安装不改名；迁移不依赖。
  - 影响：兼顾普通安装稳定与未来共存；需要产品/运维知道这个 flag。
  - 风险：中；两套路径要测试。

**建议：C，之后有真实证据再考虑 B。** 现在最危险的是“每个 Skill 都改名/改写”却把它当普通安装。至少先做到“普通安装不改，冲突才走候选，且默认关闭”。

**不决定的后果：** 安装出来的 Skill 名和内容与市场不一致；客户端/用户排障困难；未来想改回原名会造成已安装实例的迁移问题。

---

### D4 · A01 的 mine 诊断契约

**一句话：** “我的发布”里的“依赖异常”到底返回什么字段、给谁看、能不能看到具体依赖的身份。

**方案要求：** 仅 `mode=mine` 的 Expert/Team 必返 `dependency_availability` = `{status, evaluated_view, evaluated_version_id, reasons}`；reason code 固定 `dependency_delisted|dependency_deleted|dependency_unavailable`；有 published 始终诊断 published（即使行展示 editing），仅从未发布才 editing；目标/整条 via 逐项有权才给具体身份，否则合并一条匿名；公共列表不含该字段；`view` 非法组合 400；SQL 分页/COUNT/facets 同口径（API §2.2.1、§6.2）。

**PR #127 实际（最新 `016baef`）：**
- 字段是 `dependency_issues`（`target_plugin_id/target_name/relation_type/reason/via []string`），不是 `dependency_availability`；没有 `status/evaluated_view/evaluated_version_id/reasons`；
- reason 是 `deleted|delisted|release_missing`，不是固定 code；
- 只诊断 `listing_state=published` 的根；没有 editing 根；
- 没有目标/via 授权：只重查根 owner/Space，目标名称/ID 仍可能返回（隐私风险）；没有匿名合并；
- 没有 `view` 参数，`mode=mine&view=published` / 公共 `view=editing` 仍 200；
- 最新版已修复 Team 固定成员读取（用 team edge 的 `target_version_id`），公共过滤/COUNT/facets 结构可用；
- 额外把非 owner 的 detail/graph 变成 404 / root-only，方案未要求。

**为什么必须你定：** 这是 Web 的直接 wire 契约。如果先让 Web 接 `dependency_issues`，以后改 `dependency_availability` 就是第二次破坏性改动；匿名/授权规则还涉及隐私。

**选项：**

- **A（重做 #127 为方案 DTO/语义）**：保留其 SQL 过滤/COUNT/facets 成果，替换 DTO、加 view、editing 根、固定 code、目标授权、匿名聚合、refs 完整性检查；回退 detail/graph 的非契约行为。
  - 影响：Web 可一次接对；隐私达标；需要改 model/handler/service/repo/OpenAPI + 测试。
  - 工程量：M–L。

- **B（改方案接受 `dependency_issues`）**：承认现有 DTO，更新 API/技术/CL-02；但必须补匿名/授权/固定 code/editing 根，否则隐私和“草稿依赖异常”仍不达标。
  - 影响：少改 DTO，但方案/客户端要接受较弱语义；后续想加 evaluated_view 还是破坏性。
  - 风险：隐私 P0 仍在。

- **C（保留 `dependency_issues` 字段名，但补四字段/固定 code/匿名）**：折中，减少 Web 改名字，字段形状接近方案。
  - 影响：改 model/DTO 但不改 query 太多；Web 仍需改类型。

**建议：A 或 C。不要选 B 原样上线**，因为“目标名称/ID 对无权用户可见”是实打实的越权信息泄漏。

**不决定的后果：** Web 先接 `dependency_issues`，之后 A01 契约再改一次；或者带着隐私漏洞上线。

---

### D5 · 存量迁移到底交付什么

**一句话：** 旧 embedded Skill/Expert 的迁移，是做成“A29 只读 preview + A30 批准 apply 的 HTTP 协议”，还是接受“运维 CLI 一遍计划 + 直接改库/指针回滚”。

**方案要求：** MP-04 A29/A30：`file` 或 `source_plugin_id` 二选一；第一次 discovery，第二次提交完整 `proposed_plan` 冻结 `plan_digest`；显式标签、create/reuse/update_source、root map 仲裁、`source_commit_json`、checkpoint、waiting_review/blocked/completed；**不可直接回切 published 指针**；迁移后独立市场可见（API §5、设计 §8）。

**PR #126 实际：** 只有 CLI `cmd/marketplace-stock-migration`（DB source，无 ZIP/file），一遍 dataset digest；自动 `patch+1`；一名旧 Expert 一个 Connector；直接插 release + 设 published；回滚直接 `UPDATE current/published/...`（`store.go:372-478`）；没有 A29/A30、root map、source_commit_json、waiting_review、review 调用。PR 说 `Closes #119 #121 #123`，但 A34/A29/A30 都没有。

**为什么必须你定：** 这是“生产数据迁移”的安全边界和交付形态。CLI + 指针回滚可以快速完成，但无法满足“批准计划、可审计、可恢复、审核后可见”的方案要求。

**选项：**

- **A（完整方案）**：实现 A29/A30 HTTP + 冻结计划 + 审核恢复 + 非指针回滚；CLI 只做 adapter。
  - 影响：最安全、可审计；工程量大（新表/状态机/端点/UI/CLI）；适合正式生产迁移。
  - 风险：周期长。

- **B（接受 CLI 为 MP-04）**：把方案/API 改成 CLI-only、patch+1、指针回滚；明确生产迁移必须离线/单独授权。
  - 影响：交付快；但没有批准计划、没有 review、回滚是直接改指针；与 REL-01 的“数据安全回滚”冲突。
  - 风险：高；不适合有审核/多租户要求的正式迁移。

- **C（分阶段）**：保留 #126 的 inventory + CLI，但**不关闭** #121/#123；先补 A29 只读 preview（两次冻结），A30 apply 与 review 后续；**先删掉指针回滚**，改成新版本恢复。
  - 影响：可先拿到盘点和演练价值；生产 apply 仍等 A30；#126 改 `Closes` 为 `Refs`。
  - 风险：中；需要明确“CLI 不是最终协议”。

**建议：C。** 这是最现实的路径：不要把 CLI 冒充 A29/A30，也不要把指针回滚带进生产。若本期不做生产迁移，B 也可以，但必须正式 descope 并改 plan/REL。

**不决定的后果：** #126 以 `Closes #119 #121 #123` 合并后，issue 显示完成但 A34/A29/A30 缺失；生产迁移一旦用 CLI，回滚/审核没有方案保障。

---

### D6 · private 目标与“下架依赖”能不能继续安装

**一句话：** 市场里的 Expert 能不能引用同 owner 的 private Skill/Connector；某个 Skill 下架后，已经 pin 它的 Expert 还能不能安装。

**方案要求：** system→已发布 system；space→已发布 system/同 Space space；**禁止 private 目标、禁止跨 Space**；下架依赖不获得新增安装豁免；A01 隐藏坏依赖来源；A34/安装按当前访问/上下架检查失败（C01、§5.1–5.2、D117-01、D118-02）。

**PR 实际：**
- #125 `referenceAudience` 明确允许 `private 来源 → 同 owner private 目标`（`references.go:55`），候选接口也返回 private（`reference_reads.go:141-144`）；测试把“owner private → private”断言为成功。
- #126 安装 `install_read.go:13-17` 忽略 listing，`pinned_install.go:272` 允许 delisted；迁移 `released()` 忽略 listing、`audience` 允许 private→private，接受 delisted pin（`plan.go:242-243,514-531`）。
- 也就是代码整体选择了远端 #117/#123 的“private/已有 pin 可继续”，不是方案。

**为什么必须你定：** 这是受众/安全模型。私有资产一旦成为市场依赖，就出现“private 资产被共享 Expert 间接暴露”的风险；下架豁免则让“下架”失去停止分发的能力。

**选项：**

- **A（严格方案）**：新写入禁止 private 目标；安装/迁移/A34 遇到 private 或 delisted 目标整体失败；已有 private 引用做数据审计，必要时迁移成共享资产或保留只读历史。
  - 影响：最安全、语义清晰；可能破坏现有 private→private 的用户组合；需要迁移/沟通。
  - 风险：中；数据要先盘点。

- **B（保留现状）**：允许同 owner private→private；允许已 pin 的 delisted 依赖继续安装；更新方案 C01/§5。
  - 影响：兼容现有用户；但 private 资产的受众边界变复杂，A01 过滤/诊断要处理更多情况；下架不再阻止已 pin 安装。
  - 风险：高（隐私/下架语义）。

- **C（祖父条款）**：新写入禁止 private；已有 private 引用 grandfather（只读/可继续安装，不可新增）；delisted 只允许“已存在且已审计的安装 attempt”继续，不允许新安装。
  - 影响：兼顾兼容与安全；实现/测试复杂；需要审计字段/开关。

**建议：A + C 的 grandfather。** 新数据按方案严格禁止；老数据不静默公开、不静默删除，先盘点再迁移/豁免。下架依赖：新安装一律失败，已有安装不受影响（它们不经过 marketplace）。

**不决定的后果：** private 资产被市场 Expert 间接引用，受众泄漏；下架后仍能安装，运营以为已停止分发。

---

### D7 · Connector 要不要“有效态”模型

**一句话：** Connector 是否引入 `plugin_connector_effective`（有效修订/摘要），把“编辑中的配置”和“已批准生效的配置”分开，并在审核时冻结观察值。

**方案要求：** Connector 不新增业务 Version，但要有 effective 行：`effective_revision` 单调、`effective_digest` 覆盖完整有效内容；编辑与 effective 分离；专家/团队提审冻结全部 Connector 的 revision/digest，批准时变化 409 `dependency_changed`；安装/A34 读 effective（C04/C05、§4.3、D116-03、D118-03）。

**PR 实际：** 全树 0 命中 `plugin_connector_effective`/`effective_revision`/`effective_digest`/`connector_observations`；Connector 编辑直接改 `plugins` 行，editing == effective；审核不检测配置漂移。安装读 live `mcp.json`。

**为什么必须你定：** 这是审核完整性 vs 工程量的取舍。没有 effective，Expert 审核通过后，管理员/owner 改一下 Connector 配置，下一次安装就用到未审核配置；有 effective，需要新表/回填/审核事务。

**选项：**

- **A（实现 effective）**：新表 + 当前 Connector 回填 revision 1；Admin 写入/审批更新 effective；提审记录 observations；批准比对；A01/安装/A34 读 effective。
  - 影响：审核可证明、安装可复现；工程量 L；需要迁移/回填。
  - 风险：中；但方案要求。

- **B（不做，接受 live）**：更新 C04/C05/§4.3，删除 observations/effective；Connector 审核只冻结 ID/权限，不保证配置不变。
  - 影响：简单；但审核可被后续编辑绕过，安装不可复现；与“完整审核”目标冲突。
  - 风险：高。

- **C（过渡：审核时冻结 digest，不建表）**：提审时把 Connector 完整内容的 digest 存 review，批准时比对；安装仍读 live，但至少审核漂移可检测。
  - 影响：中等工程量；没有稳定 effective 指针，安装仍可能变；可作为 A 的第一步。

**建议：A；如果本期排不进，至少 C，并在文档中明确“安装仍读 live”的限制。** 不要 B 静默上线。

**不决定的后果：** Expert 审核通过后配置还能被改；A34/安装无法证明“这次安装用的是审核过的 Connector 配置”。

---

### D8 · 保存并发（CAS）与 relations 严格性

**一句话：** 更新资源时，客户端是否必须带一个“我基于哪个版本改的”令牌；不带/过期就 409，而不是最后写入者覆盖；relations 缺失是否 400。

**方案要求：** `edit_revision`（从 1 开始）；更新必须 `expected_edit_revision`，不匹配 409 `edit_conflict`，新建必须省略；完整 Write 的 `relations` 必填数组，缺失/null 400，`[]` 才清空；Skill parse-task import 更新也要 CAS；所有客户端协议同步（API §2.3、MP-01 步骤 3、MP-02 步骤 1）。

**PR 实际：** 全树无 `expected_edit_revision`；`ExpectedCurrentVersionID` 是未赋值的内部字段；`relations` 非指针，省略被当 `[]` 并软删全部 live 边；import 更新无 CAS。并发保存是 last-write-wins。

**为什么必须你定：** 这是破坏性 wire 变更：Admin/Web/CLI/Bot 都要改；但也是数据安全底线。可以先在开关后启用，但一旦启用新引用/发布，就不能再容忍静默覆盖。

**选项：**

- **A（方案：edit_revision）**：加列 + wire + 所有写入口 + 客户端 + OpenAPI；flag 打开时强制，旧客户端在迁移期会 400。
  - 影响：数据安全；客户端必须同步；需要版本化/灰度。
  - 风险：中；但可控。

- **B（复用 current_version_id 作为令牌）**：不新增列，暴露 `expected_version_id`；语义是“我基于这个 current 快照改”；同样 409。
  - 影响：少一列/少一次迁移；但 version_id 与 release/历史语义容易混淆；客户端仍要改。
  - 风险：中低。

- **C（只在新版本化写入强制，旧路径保留）**：flag off 时旧行为；flag on 时新协议。
  - 影响：渐进；但两套语义容易漏；必须保证 flag on 后所有入口一致。

**建议：A（或 B 若想少一列），并采用 C 的灰度方式。** 关键不是列名，而是“更新必须带令牌、缺失/过期 409、relations 缺失 400”。

**不决定的后果：** 两个并发保存静默互相覆盖；relations 一次漏传就清空依赖，且无法审计是谁覆盖的。

---

### D9（附加执行决策）· 安装引擎与重试语义

**一句话：** A31/A32 是否合并到一个 InstallCoordinator；同 key 重试是“用首次冻结的 plan/请求”，还是“按当前 Connector/根重新构建”。

**方案要求：** 两个 URL 只保留 adapter，内部同一 Coordinator、一次 Fleet 原子 mutation；删除市场 legacy create/bind/delete 补偿；首次 plan 固定完整无凭据请求；同 key 重试只读原 plan + 同一完整请求（含重新提供的 Secret），不因 Connector/根更新重建；状态机 + lease + UNKNOWN；仅首次成功计数（A04、§7.2、D120-01/02/05/06）。

**PR #126 实际：** A31 走 pinned engine；A32 走 legacy expert `CreateAgent→skills→bind→delete` 补偿；attempt 表只有 `completed` 布尔，存 root_version_id/输入/请求指纹；重试重新 `resolvePinnedInstallation` 读当前 Connector/根，漂移即 409；成功回放重新 POST 且要求根仍 published/重填 Secret；每次成功都 `trackInstall`（测试按此断言）；A32 无 Key/用户/UUID。

**为什么必须你定：** 这决定 A32 兼容性和“网络失败后能不能安全重试”。如果保留两套，就必须接受 A32 不保证幂等，或者给 A32 单独做一套（违背方案）。

**选项：**

- **A（方案）**：A31/A32 统一 Coordinator；A32 补 Key/用户/UUID；持久化 plan/请求/HMAC；状态机/lease/UNKNOWN；删除市场 legacy 补偿；独立 `/experts`/`/squads` 保留共享代码。
- **B（保留两套）**：A31 新引擎、A32 legacy；文档明确 A32 不支持安全重试；不声称“同一引擎”。
- **C（过渡）**：A32 先接 pinned engine + Key，但暂不删除 legacy 代码（仅市场入口切换）；下一步补 plan/状态机。

**建议：C → A。** 不要用 B 合并 #126 还写“两个入口同一内容/安全重试”。

---

### D10（附加执行决策）· 灰度与旧链路退役

**一句话：** 新市场能力是靠业务 flag 灰度，还是按方案做部署层隔离 + 全入口阻断；旧市场执行器什么时候不可达。

**方案要求：** 先在部署层隔离新副本、只开健康检查/受控验收流量，阻断生产读写；冻结旧写入/后台任务；REL-01 提供命令/操作者/制品/拒绝证据；flag 只能作为额外手段，不能替代部署隔离；回滚只部署兼容二进制，不从历史回退指针（§8、REL-01）。

**PR 实际：** 三个 flag 默认 false；旧容器导入/A32 legacy 在 flag off 时仍可达；readiness 只有文档，无部署 gate；#126 回滚是指针回滚。

**选项：** A 部署隔离 + gate（方案）；B 只靠 flag（简单但方案明确说不够）；C 代码里硬删旧路径（会影响独立非市场 Expert/Squad，不建议）。

**建议：A**，并把“旧市场执行器不可达”和“指针回滚禁止”写进 REL-01 的放行条件。

## 4. 不需要你拍板、可以直接排期的代码工作

这些是方案已经确认、或纯粹是实现缺口，不依赖上面决策：

1. **A34** 完整内容读取（方案已确认；MP-03 唯一 owner）。
2. **A01 公共过滤** 的 SQL/COUNT/facets（#127 已有基础；DTO 决策 D4 后再接 Web）。
3. **冻结审核字段**：`source_version_id/source_edit_revision/frozen_content_digest/frozen_version|connector/baseline_version_id/dependency_diff`（observations 依赖 D7）。
4. **A25 receipts**：`plugin_mutation_receipts`、Idempotency-Key、输入摘要、7 天回放、`resolved[]`。
5. **A03/A10/A11**：版本图 `plugin_id@version_id`、Connector effective 节点、1500/2000；Candidate/Dependent 字段、via、`meta.has_hidden_dependents`。
6. **refs 完整性/锁序/限制**：JSON_TABLE 双向、等数量错 pin 500、review→Plugin 排序锁、≤3 重试、member_key/role/sort_order/20-30、connector null pin 拒绝、relations 省略 400。
7. **发布/读取 wire**：publish `version_id`、响应 `published_version_id`、approve 幂等、A04 `scope`、A06/A07 `version_id`、A08 统一 JSON、VersionSummary/view/selected。
8. **A24 ZIP-only**：AdminCreate/Update 拒绝 Expert/Team；A25 头/CAS/resolved。
9. **A32 Key/用户/UUID**（若 D9 选 A/C）。
10. **CL-01/02/03 + REL-01**。

## 5. 建议的拍板顺序

**第一批（本周，决定返工方向）：**

1. D1 删除生命周期（B 推荐）
2. D2 A09（A/C 推荐）
3. D3 共存命名（C 推荐）
4. D6 private/下架目标（A+grandfather 推荐）

**第二批（决定 A01/迁移/Connector 的契约）：**

5. D4 A01 DTO（A/C 推荐）
6. D5 迁移形态（C 推荐）
7. D7 Connector effective（A 推荐）

**第三批（决定写入协议/安装/放行）：**

8. D8 CAS（A/B 推荐）
9. D9 安装引擎/重试（C→A 推荐）
10. D10 灰度/退役（A 推荐）

**依赖关系：** D4 依赖 D1/D6（坏依赖语义）；D5 依赖 D6（private/delisted 目标）；D7 影响 D4/A34/安装；D8 影响所有写入口；D9 影响 D3/A34；D10 最后。

## 6. 验收建议（避免“文档说完成”）

- 每个决策落地时，先改 `02-api-contracts.md`/`01-technical-design.md` 的对应段，再改代码；不要代码先跑、文档后补。
- 每个 PR 只关闭**自己实现**的 issue；#126 在补 A34/A29/A30 前，把 `Closes #119 #121 #123` 改成 `Refs`。
- 以下证据不能省：A34 单 GET 的完整 bundle/JCS/16MiB/no-store；A01 的 COUNT/facets/匿名/编辑根；安装的原 plan 重试/UNKNOWN/计数一次；迁移的两次 preview/plan_digest/审核恢复/非指针回滚；REL 的四端真实流量。
- `make openapi-check`/`openapi-diff` 与真实 MySQL smoke 是合并门槛，不是“文档已验证”。

## 7. 边界

本文只读静态核对 PR #124/#125（`origin/main 59ca051`）、#126（`639d1f1`）、#127（`016baef`）；未运行服务/MySQL/Fleet/Daemon/客户端。决策建议基于方案与当前代码，不代表生产验证通过。

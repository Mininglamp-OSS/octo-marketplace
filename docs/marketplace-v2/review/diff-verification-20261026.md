# Marketplace v2 差异记录 × 实际仓库核对（2026-10-26）

> **后续确认：** 用户明确确认 Team→Expert 也跟随当前正式发布版，以及无保护引用的曾发布资源可删除。本文保留决策前的代码事实分析；逐条“修改建议”若与[决策记录](../decisions-20261026.md)冲突，以决策记录为准。
>
> **结论先行：** `03-github-issues-114-123-diff.md` 的“远端 issue 文案 vs v2 方案”文字比对大体成立（本次逐字复核了 #115–#118 的引文，全部命中），但它没有核对代码。实际合入/待合入的代码选择的是**远端三-PR 口径**，不是 `docs/marketplace-v2` 方案口径。因此大量“远端 issue 需要改”的条目，首先要回答的是“我们的代码和方案二选一”，而不是只改 GitHub 正文。

## 1. 核对基线

| 项 | 实际值 |
|---|---|
| 本地工作区 `main` | `99cc261`（2026-10-13），**落后 origin/main 16 个提交**，不含 #124/#125 |
| `origin/main` | `59ca051`（2026-10-26 11:01 +08），已合入 **#124**（09:42）与 **#125**（11:01） |
| 代码审计基线 | `origin/main` `59ca051`（审计时临时只读 worktree，已清理；结论基于该提交） |
| 待合并 PR | **#126** `94de531`（install + inventory + stock migration + readiness，open，已 rebase 到 #125 之上）；**#127** `d7a1ffe`（dependency availability，open） |
| 远端 issue（本次重查） | #114/#119/#120/#121/#122/#123 **open**；#115/#116 **closed**（01:42Z）；**#117/#118 closed**（03:01Z，文档快照写的是 open） |
| 方案文档实际位置 | `docs/marketplace-v2/`（本次已从 `.codex/marketplace-v2-docs/marketplace-v2/` 移入，待提交） |
| 差异记录条数 | 实际 **80** 个唯一编号（文档 §15 写“73 条”，计数不一致） |

**核对口径：** 静态阅读代码、迁移、OpenAPI 与 git 历史；未运行服务/MySQL/Fleet/Daemon/客户端。`internal/db` 集成测试在无 `TEST_MYSQL_DSN` 时 skip，不能把本报告当作运行验收。#119–#123 因 GitHub API 限流未能重新逐字抓取正文，沿用差异记录引文；其状态已单独重查。

**开关前提：** `PLUGIN_VERSION_MANAGEMENT_ENABLED`、`PLUGIN_REFERENCES_ENABLED`、`PLUGIN_PINNED_INSTALLATION_ENABLED` 默认均为 `false`（`internal/config/config.go:206-209`）。下文“代码已实现/代码行为”均指对应开关打开时；默认部署仍走旧路径（旧容器导入、A32 legacy 安装等）。这也是 REL-01 必须做部署层隔离、不能只靠 flag 的原因之一。

## 2. 文档自身需要修正的 6 点

| # | 问题 | 证据 | 怎么改 |
|---|---|---|---|
| T1 | 权威路径已修复 | 文档引用的 `docs/marketplace-v2/` 已建立，原 `.codex/...` 内容已迁入；待提交后即可被 CI/同事看到 | 保留 `docs/marketplace-v2/` 为唯一权威路径；不要再引用 `.codex/...` |
| T2 | 远端快照已过时 | #117/#118 已于 11:01 关闭；#125 已合并；#126 已不是 draft（`draft:false`）并 rebase 到 `94de531`；#127 于 11:39 新建 | 更新 §1.2 状态表、G04、D114-06、D117-07、D122-01 等进度描述；不要再用“PR2/PR3 未打开”作为当前事实 |
| T3 | 条数不一致 | 文档 §15 称 73 条；实际唯一编号 80 条（G01–G04、X01–X03、D114-01…D123-12） | 统一编号与计数；若后续引用，按编号而非条数 |
| T4 | 把“方案存在”当成“能力存在” | 文档 §1.2 的“当前本地能力映射”把 MP-01/02/03/04 写成已有子集；实际 #124/#125 只交付了部分，A09/A34/A29/A30/CAS/Connector effective/A01 诊断均缺（详见 §5） | 增加“代码事实”列；区分“文档定义”与“已实现” |
| T5 | 用 closed 状态代替验收 | #115/#116 closed 但 MP-01 的 A09、CAS、declared_visibility、Connector effective、frozen review 字段未实现；#117/#118 closed 但 A01、receipts、frozen DTO、delete 并集、A10/A11 契约未实现 | 在文中逐条标注“closed ≠ 当前方案完成”；重开/补 issue 的结论必须基于代码核验 |
| T6 | 没有客户端/真实环境核对 | X03/REL-01 只做文字映射；本地 `/home/mlamp/octo-web`、`/home/mlamp/octo-cli` 未见 A34/版本协议接入，buddy 仓库未找到 | 把 CL-01/02/03、REL-01 单列交付物；不要用 #126 合并替代客户端和四端验收 |

## 3. 文档认为“远端错”，但实际代码也站在远端一侧

这五条是全文最关键的纠正。文档的修订建议若只改远端 issue，代码仍不符合 v2 方案。

| 主题 | v2 方案 | 远端 issue | 实际代码（origin/main / PR） |
|---|---|---|---|
| 已发布资源删除 | 保留既有删除，保护集合 = live incoming ∪ published refs/固定 Expert 间接引用（G02、D118-01） | delist-only，published 只能下架不能删（#118） | `rejectPublishedMutation` 对任何 release/曾 published/delisted/approved review 一律 `ErrConflict`，`Delete/DeleteGraph` 直接调用；**代码 = 远端，不是方案**（`internal/repository/plugin/references.go:254`，`write.go:849,950`） |
| private 目标引用 | 一律禁止 private 目标（C01、D117-01） | 同 owner 的已发布 private 资产可被引用（#117 Acceptance） | `referenceAudience` 明确允许 `private 来源 → 同 owner private 目标`，候选接口也返回 private 目标（`references.go:41-56`，`reference_reads.go:136-145`） |
| restore-to-draft | A09 必需，复制旧内容/pins 为新草稿（G03、D116-02） | 明确排除，`no first-release restore endpoint`（#116） | 全仓库无 `_restore` 路由/handler/service；`docs/api/plugin-version-management.md:75` 明写 “Restore-to-draft is not part of this release.” |
| 同名/多版本共存 | deferred，不默认采用 suffix/frontmatter 改写（D119-01、C07） | 把 stable installation identity/coexistence 列为负载项（#119/#120） | PR #126 已实现 `mk1` 稳定名（`-mk1-<digest>`）+ 每次安装改写 `SKILL.md` frontmatter `name`，迁移校验也依赖它（`install_primitives.go:17,123-150,168-231`） |
| 迁移协议与回滚 | A29/A30 两次只读 preview、冻结 `plan_digest`、显式标签、不可直接回切指针（D123-01/03/06/08） | 无 `legacy_import`、`patch+1`/forward label、`restores pointers/flags`（#123） | 无 A29/A30；PR #126 是 CLI `marketplace-stock-migration`：一遍 dataset digest、自动 patch-forward、回滚直接 `UPDATE current/published/...`（`stockmigration/plan.go:395-401`，`store.go:372-478`） |

## 4. 必须先拍板的 8 个决策

在这些决策落地前，逐条改代码会反复返工。

| ID | 决策 | 方案要求 | 代码实况 | 建议 |
|---|---|---|---|---|
| D1 | 已发布资源的删除/收窄 | 保留既有动作 + live∪published refs 并集保护 | 曾发布即禁止删除/改 visibility | 若坚持“不改变生命周期”，改 `Delete/DeleteGraph` 为并集保护；若接受 delist-only，改方案 G02/C03 与 API A19，并保留代码。**P0** |
| D2 | A09 restore-to-draft | MP-01 必需（新 ID、复制文件/pins、CAS、不动 published） | 完全缺失，文档主动排除 | 实现 A09，或正式 descope 并同步 MP-01/API/OpenAPI。**P0** |
| D3 | 同名/多版本共存与安装命名 | deferred；不改市场原件、不写 frontmatter | PR #126 默认启用 `mk1` + frontmatter 改写，迁移也用它 | 默认关闭/移除；若要做，先补真实 Fleet/Daemon 加载证据并更新方案 C07/§6/§7.1。**P0** |
| D4 | A01 诊断 DTO | `dependency_availability`（status/evaluated_view/evaluated_version_id/reasons） | PR #127 是 `dependency_issues`（target_plugin_id/target_name/reason/via[]） | 重做 PR #127 的 DTO/语义，或改 API §2.2.1/技术 §6.2 与 issue。**P0** |
| D5 | 迁移交付形态 | A29 preview/A30 apply、两次冻结计划、审核恢复、非指针回滚 | 只有 CLI 一遍计划 + 指针回滚 + 直接发布 | 不把 #126 当 MP-04 完成；补 A29/A30 或正式缩小 #119/#121/#123 验收。**P0** |
| D6 | private/下架目标 | 禁止 private 目标；下架依赖不得新安装 | 写引用允许 private→private；PR #126 安装/迁移允许 delisted pin | 删除 private 分支；安装/迁移/A34 统一按当前 listing + 矩阵失败。**P0** |
| D7 | Connector 有效态 | `plugin_connector_effective`、effective_revision/digest、审核观察 | 无表、无观察；编辑即生效 | 新增 effective 模型并接审核/A01/安装；否则改方案 C04/C05。**P1** |
| D8 | 保存并发（CAS） | 更新必须 `expected_edit_revision`，缺失/过期 409 | 全树无 CAS；`relations` 省略被当清空 | 加 `edit_revision` + wire/DB 校验；先做 MP-01/MP-02 写入路径。**P0/P1** |

## 5. 逐条核对

图例：**成立** = 文档指出的远端/方案差异真实存在；**部分** = 只部分成立；**不成立** = 代码/文档已满足或文档判断错误；**已过时** = 状态/进度变化导致不再成立。优先级 P0/P1/P2/P3 沿用文档口径，但结合代码风险调整。

### 5.1 共通与跨 issue（G01–G04、X01–X03）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| G01 | 成立（但不影响行为） | 远端固定“恰好 3 个 PR”；方案不强制一 issue 一 PR。实际也确实是 #124/#125/#126 三批 + #127 额外修复 | 不改代码；更新 tracker/PR 描述，明确 #127 也是交付 PR、8 issue 不是 8 PR | P2 |
| G02 | **成立（且代码也如此）** | 代码 `rejectPublishedMutation` 是“曾发布一律禁止删除/收窄”，与方案“保留既有删除 + 并集保护”相反；见 §3 | 决策 D1；按方案改 `Delete/DeleteGraph` 与 visibility 变更，或改方案/API | P0 |
| G03 | **成立（且代码也如此）** | A09 全仓库缺失；`docs/api/plugin-version-management.md:75` 明确排除 | 决策 D2；实现 A09 或 descope | P0 |
| G04 | 已过时 | #125 已合并、#117/#118 已关闭、#126 已非 draft、#127 新建；文档仍写 PR2/3 未开 | 更新 §1.2/G04/D114-06/D122-01；不改代码 | P2 |
| X01 | **成立（且代码也缺）** | 远端十项无 A34；方案 A34 确认；origin/main 与 #126 对 `distribution/DistributionBundle/bundle_digest` 全 0 命中 | 实现 A34 + 共享 resolver + CL-02/03 + REL；见 §6 | P0 |
| X02 | **成立；PR #127 只完成一半且契约不符** | origin/main 无 A01 过滤；#127 有 SQL 前置过滤/COUNT/facets，但 DTO 是 `dependency_issues`、无 `view`、Team 第二跳读成员 published 而非固定 pin、无匿名原因聚合、无 refs 完整性检查 | 按 §6 C1–C6 重做 #127，或改方案/API | P0 |
| X03 | **成立** | 本仓库无 CL-01/02/03 与 REL-01；本地 octo-web/octo-cli 未见 A34/版本接入，buddy 未找到 | 单列四端交付与 REL-01；不要把 #126 合并当客户端完成 | P0（交付） |

### 5.2 #114 · 总览（D114-01…06）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D114-01 | 成立 | 远端 3 PR；实际 #126 把安装/inventory/migration/readiness 打包，和远端 PR3 一致，方案“MP-03/04/CL-01 可并行”未发生 | 文档/issue 组织更新，无代码 | P2 |
| D114-02 | **成立（且代码也缺）** | M2 conversion 被远端排除；方案 A29/A30 必需；全仓库无 `legacy_import`；#126 的 CLI 不接受 ZIP/`file` | 实现 A29/A30 或正式 descope 并同步 API §5/MP-04 | P0 |
| D114-03 | 成立 | #114 的 Goal/Load-bearing/Acceptance 未覆盖 A34/A01/客户端；代码同样缺 | 更新 #114 范围 + 实现 X01–X03 | P0 |
| D114-04 | **成立（且代码反向实现）** | 远端把 stable identity/共存当负载项；方案 deferred；#126 却默认 `mk1` + frontmatter 改写 | 决策 D3；默认关闭/移除，或批准并补真实证据 | P0 |
| D114-05 | 成立 | delist-only（代码已实现）、restore（代码缺）、migration `patch+1`（#126 已实现）三件事分别对应 D1/D2/D5 | 按 D1/D2/D5 处理 | P0/P2 |
| D114-06 | 已过时 | #113 closed 未合并；#124/#125 已合并；#126/#127 open | 更新状态；无代码 | P2 |

### 5.3 #115 · V1 版本/文件底座（D115-01…06）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D115-01 | **成立** | 有不可变快照/artifacts/hashes，但无 `declared_visibility`/`content_digest`/`restored_from_version_id`/`revision_no`（`migrations/sql/20261023-00`）；只改 visibility 不新建版本（`write.go:734-757`） | 迁移 + snapshot codec：复用键加入 label/declared_visibility/关系身份 | P0/P1 |
| D115-02 | **成立** | 全树无 `expected_edit_revision`；`relations` 省略被当 `[]` 清空（`handler.go:145-155`、`write.go:1294-1356`）；import 不保存原 ZIP（`import.go:318-353`） | 加 edit_revision/CAS、relations 指针+400、import CAS+原 ZIP 冻结 | P0 |
| D115-03 | **成立（安全）** | 无版本级受众；`versionAccess` = 当前可见 + 有 release（`version_management.go:54-72`），private v1 在 public v2 后仍可被新受众读取 | 加 `declared_visibility` 与历史受众交集 | P0 |
| D115-04 | 不成立（基本满足） | `retainedAttachmentKeys` 覆盖 live + 全部 version sidecar（`review.go:1083-1131`）；install plan 保留点属 MP-03 | 无代码；MP-03 接入时复验 | P2 |
| D115-05 | 不成立（已实现） | 审批快照要求不可变 namespace，否则 409 `legacy_snapshot_requires_resubmit`（`version_snapshot.go:18-98`） | 无代码；保留 | P3 |
| D115-06 | 部分 | G02 代码已 delist-only（与方案相反），G03 缺 A09，G04 状态 | 同 D1/D2 | P0 |

### 5.4 #116 · V2 发布/历史读取（D116-01…07）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D116-01 | 部分/需澄清 | AdminCreate 发布+default，AdminUpdate listed system 写 release/published（`admin.go:168-345`），与“既有行为”接近；但 A24 仍接受 Expert/Team JSON 写，A21 无 `view=effective` | 文档化实际 AdminUpdate；A24 拒绝 expert/team；A21 view | P1 |
| D116-02 | **成立（代码缺）** | 同 G03/D115-06；无 `_restore` | 决策 D2 | P0 |
| D116-03 | **成立** | 无 `plugin_connector_effective`/effective_revision/digest/observations；Connector 编辑直接改 plugins 行，editing==effective | 决策 D7；新增 effective 表/事务/读投影 | P1 |
| D116-04 | 部分 | SemVer 单调、unique、重复 publish 幂等已有；但 publish wire 无 `version_id`、响应无 `published_version_id`、重复 approve 返回 409 而非旧结果 | publish 必填 version_id、响应加 published_version_id、approve 幂等回放 | P1 |
| D116-05 | **成立** | 无 `view`/VersionSummary/selected/published/edit_revision/has_unpublished_changes；member_count 读 live；A04 无 scope；A06/A07 无 version_id；A08 形状不同 | 统一读投影 + VersionSummary + view/scope/version_id | P1 |
| D116-06 | 部分 | 批准不覆盖 current 已实现（`version_review.go:45-56`）；缺精确 version_id 冻结、frozen_content_digest、source_edit_revision | 冻结 DTO/wire（A13/A14/A15） | P1 |
| D116-07 | 部分 | G02 代码一致（delist-only），G03 缺 restore，G04 状态 | 同 D1/D2 | P0 |

### 5.5 #117 · 引用写入与授权（D117-01…08）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D117-01 | **成立（且代码反向）** | `referenceAudience` 允许 private→同 owner private（`references.go:55`）；候选接口也返回 private（`reference_reads.go:141-144`）；与方案“private 一律禁止”相反 | 决策 D6；删 private 分支/候选，补矩阵负测试 | P0 |
| D117-02 | 不成立 | default 不可隐藏、非默认场景保留、候选不 JOIN placement（`scenes.go:171,208`、`reference_reads.go:146`） | 仅澄清远端文案；无代码 | P2 |
| D117-03 | **成立（代码缺）** | 无 A29/A30；#126 的 CLI 是 DB-only，明确不含 old ZIP conversion；开关关闭时旧容器仍可导入 | 决策 D5；实现 A29/A30 或 descope | P0 |
| D117-04 | 部分 | A25 路由/ZIP 解析有，但 A24 未拒绝 expert/team；A25 无 Idempotency-Key/CAS/resolved；schema2 缺 sort_order/role/summary 校验 | 拆内部 ZIP 写入口；A24 拒绝；A25 头/CAS/resolved；schema2 补齐 | P1 |
| D117-05 | 部分 | pin 必填/归属/release、connector 非 nil 拒绝、observed_* 拒绝、refs 同事务已有；但显式 null connector pin 被接受、relations 省略清空、member_key/regex/role/sort_order/20-30 限制缺、refs 双向校验缺 | 严格 wire + refs 双向 JSON_TABLE + 限制 | P1 |
| D117-06 | **成立** | origin/main 无 A01 诊断/过滤；#127 契约不符 | 同 X02；不应由 #117 单独承担 | P0/P1 |
| D117-07 | 部分 | 代码已 delist-only、无 restore；G04 状态过时 | 同 D1/D2 + 状态更新 | P0 |
| D117-08 | **成立** | 无 `plugin_mutation_receipts` 表、无 Idempotency-Key、无摘要/7 天 tombstone/同事务回放；A25 响应无 `resolved[]` | 新增 receipts 迁移+服务层，接 A25 | P1 |

### 5.6 #118 · 冻结审核与保护（D118-01…09）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D118-01 | **成立（代码用总禁令）** | live incoming 保护有（`write.go:1093-1108`），但 `rejectPublishedMutation` 对任何 release/published/delisted/approved 一律拒删；没有从 refs 计算 published 直接/间接引用 | 决策 D1；替换为 live∪refs 并集 | P0 |
| D118-02 | 部分 | origin/main 下架 space 依赖后安装失败（无豁免）；但 PR #126 `install_read.go:13-17` 忽略 listing，允许 delisted pin | 在 #126 安装/迁移/A34 统一按当前 listing 拒绝 | P0（#126） |
| D118-03 | **成立** | 无 effective 表/observations/`dependency_changed`；批准不检测 Connector 漂移 | 决策 D7；提审冻结 effective_revision/digest，批准变化 409 | P1 |
| D118-04 | **成立** | 只有 readme_content/frozen_relations，且 frozen_relations 丢 pin；缺 source_version_id/frozen_content_digest/frozen_version/frozen_connector/observations/baseline/diff；坏快照降级 `[]`；重复 approve 409 | 扩表+DTO；坏快照 500 `relation_index_inconsistent`；approve 回放 | P1 |
| D118-05 | 不成立 | owner 提交、Space auto-approve、reviewer 权限沿用，未新增角色（`review.go:144-148,336-342`） | 仅更新远端 G5 文案 | P3 |
| D118-06 | **成立** | Dependents 返回 `plugin_id/version_id/name/depth`，无 `source_plugin_id/target_plugin_id/is_indirect/via/meta.has_hidden_dependents`；排序也不是规定顺序（`reference_reads.go:12-19,52`） | 按 API §2.4 重写 DTO/查询 | P1 |
| D118-07 | 部分 | 锁内重验有、锁内无 OSS/Fleet；但 submit 先 Plugin 后 review、approve 先 review 后 Plugin（AB-BA），目标/版本锁未排序，无 3 次重试，refs 不完整返 409 而非 500 | 统一锁序 + 排序 + 重试 + 完整性 500 | P1 |
| D118-08 | **成立** | 同 X02；origin/main 无任何依赖谓词/诊断 | 重做 #127 或改方案 | P0 |
| D118-09 | **成立** | A03 按 plugin_id 合并、无 node_key@version_id/source_version_id，630 上限；A10 路径 `/plugins/{id}/reference_candidates`、字段 `version_id/already_bound/is_selectable`，无新建模式 | 版本化图节点/边/1500/2000；A10 路径/DTO | P1 |

### 5.7 #119 · 固定解析与安装内容（D119-01…06）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D119-01 | **成立（代码反向）** | 远端要 stable identity/共存；方案 deferred；#126 默认 `mk1` + frontmatter 改写（`install_primitives.go:17,123-150,168-231`），迁移也用 | 决策 D3；默认关闭/移除，或批准并补真实加载证据 | P0 |
| D119-02 | **成立（代码缺）** | A34 全 0 命中；#126 无 `DistributionBundle`/`/distribution` | 实现 A34 + 完整内容 resolver | P0 |
| D119-03 | **成立** | #126 resolver 无只读一致性事务，Connector 读 live；无 effective 行/修订 | 短只读事务固定根/refs/effective + 退出后读文件 | P1 |
| D119-04 | 部分 | 有 16MiB/扫描预算；但固定图仍是旧 630/plugin_id 口径，未统一 1500/2000，MCP 合并/重复 server key 需复核 | 对齐图/文件预算与 MCP merge | P1 |
| D119-05 | 部分 | 不持久化明文 token/custom_env（attempt 只存哈希）；但无 HMAC/key_id，安装侧无 MCP 占位符/凭据检查（deliberate no-scanner） | HMAC + 占位符策略（或明确记录 client-side 控制） | P1 |
| D119-06 | 部分 | G02 代码 delist-only，G03 缺 restore，G04 状态 | 同 D1/D2 | P0 |

### 5.8 #120 · 安装接入/重试/运行验收（D120-01…07）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D120-01 | **成立** | A31→Fleet capability；A32→legacy expert `CreateAgent→skills→bind→delete` 补偿；无 Coordinator，两个执行器（`install.go:80-125`，`expert/install.go:247-389`） | 一个 InstallCoordinator；A32 接同一 attempt/请求；删除市场 legacy 补偿 | P0 |
| D120-02 | **成立（代码=远端错法）** | 同 key 重试重新 `resolvePinnedInstallation` 读当前 connector/root，漂移即 409；只冻结 `root_version_id`，无原 plan/请求/HMAC（`install_attempt.go:46-80`，测试 `pinned_install_test.go:231-234`） | 持久化不可变 plan/完整请求；重试只读原 plan，不重读 live | P0 |
| D120-03 | **成立（代码反向）** | 远端把共存当必需；方案 deferred；#126 已实现 | 决策 D3 | P0 |
| D120-04 | **成立** | A32 无 Idempotency-Key/user-only/workspace UUID/header-body 校验；OpenAPI 未变（`handler/plugin/install.go:18-72`） | A32 补必填头、用户身份、UUID、同 attempt 域 | P0/P1 |
| D120-05 | **成立** | 无 UNKNOWN/lease/`retry_until`/`first_dispatched_at`/HMAC；窗口仅 `created_at+24h` | 状态机 + lease + 保守窗口 + HMAC | P1 |
| D120-06 | **成立** | 无 PREPARED/DISPATCHING/SUCCEEDED/FAILED/UNKNOWN；成功回放重新 POST 且要求根仍 published/重填 Secret；每次成功都 `trackInstall`（测试还这样断言） | 状态机/CAS、本地成功回放、仅首次成功计数 | P1/P0 |
| D120-07 | **成立** | A34/客户端验收缺失；不能用安装结果反证读取 | X01/X03 + REL | P0 |

### 5.9 #121 · 只读盘点（D121-01…05）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D121-01 | 成立（覆盖缺口） | `internal/inventory` 是只读、SELECT-only、`object_verification=not_checked`；方案 MP-04 要求文件可取性/内容证据 | 把对象验证归 MP-04；缺证据不可放流 | P1 |
| D121-02 | 成立 | inventory 无稳定 `source_node_key/origin`、source scope、文件摘要；A29 不存在，无法喂 `proposed_plan` | 扩展 inventory + A29 | P1 |
| D121-03 | 部分 | inventory 不猜 release（好）；MP-04 的未知 pin/fence/逐源证据缺失 | MP-04 实现并验收 | P1 |
| D121-04 | 部分/已过时 | inventory 已在 #126（open）实现；远端勾选/PR3 进度过时 | 更新 tracker；无代码 | P2 |
| D121-05 | 成立（但无代码影响） | 远端把 delist-only/restore 附到盘点；盘点本身只读，不应据此改生命周期 | 更新远端文案；不改 inventory | P2 |

### 5.10 #122 · 放行与回滚（D122-01…07）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D122-01 | **成立** | REL-01 依赖 MP-03/04/CL-01/02/03；本仓库无 REL 交付；#126 未合并且不含 A34/A29/A30 | 更新 #122 Requires；单列 REL-01 | P0（交付） |
| D122-02 | **成立** | 远端允许“legacy path 或排除”；方案要求首轮前旧市场执行器不可达；当前 3 个 flag 默认 false，A32 legacy/容器旧路径仍可达 | 决策 rollout/gate；旧路径阻断或明确排除 | P0 |
| D122-03 | 部分 | 有 3 个默认 false 的业务 flag（`config.go:206-209`）；但方案要部署层隔离/全入口阻断，当前只有文档 | REL-01 部署门禁与拒绝证据 | P1 |
| D122-04 | **成立** | 远端允许 forward-fix-only；方案要求数据安全兼容回滚；#126 回滚直接恢复指针/listing（`store.go:372-478`） | 决策 D5；改为兼容二进制 + 新版本恢复 | P0 |
| D122-05 | 成立 | 无受控审核窗口、旧 pending fence 执行流程、UNKNOWN 保留流程 | REL-01 可执行 runbook | P1 |
| D122-06 | **成立** | A34/A01/独立市场可见未纳入全端证据 | X01/X02 + REL 四端验收 | P0 |
| D122-07 | 部分 | 共存/restore/PR 进度旧口径 | 同 D2/D3 + 状态更新 | P0/P2 |

### 5.11 #123 · 存量迁移（D123-01…12）

| ID | 差异是否存在 | 代码/仓库实况与证据 | 需要修改什么 | P |
|---|---|---|---|---|
| D123-01 | **成立（代码缺）** | 远端 `no legacy_import endpoint`；方案 A29/A30 必需；全仓库无 A29/A30；#126 CLI 只支持 DB source、无 ZIP/file | 决策 D5；实现 A29/A30 或 descope | P0 |
| D123-02 | **成立（代码=远端）** | #126 保留 private/listing 镜像，`audience` 允许 private→private，`released()` 忽略 listing，接受 delisted pin（`plan.go:242-243,514-531`） | 决策 D6；按矩阵/当前 listing 阻断并给 blocked reason | P0 |
| D123-03 | **成立** | #126 `forwardLabel` 自动 patch+1，子节点固定 1.0.0；方案要求显式标签、绑定冻结 `plan_digest` | 标签由批准计划显式给出；冲突重新 preview | P1 |
| D123-04 | 部分 | #126 默认一名旧 Expert 一个 Connector（`plan.go:370-390`），无批准拆组；方案默认整组、允许批准拆组 | 增加批准拆组/覆盖校验 | P1 |
| D123-05 | 部分 | #126 复用仅按已审计 mapping，无内容 hash 自动复用（好）；方案要求显式 target/精确 version + 子图身份 | 补 reuse action 与完整身份校验；不按 hash 自动判定 | P1 |
| D123-06 | **成立（代码=远端）** | #126 rollback 直接 `UPDATE current/published/manifest/visibility/listing`（`store.go:437,462-466`），与方案“不可直接回切指针”相反 | 决策 D5；删除指针回滚，改新版本恢复 | P0 |
| D123-07 | 部分/N/A | #126 是 DB-only、无对象复制/清理；方案要求清理仅限本批新建且无引用对象 | 当前无代码；材料化后补引用检查 | P2 |
| D123-08 | **成立** | #126 一遍 dataset digest，无 `proposed_plan`/`plan_digest`/`source_identity`/`source_scope` | 实现两次 preview/冻结计划 | P1 |
| D123-09 | 成立 | #126 仅 per-root `batch,root` 跳过；无 root map/source_commit_json/checkpoint/waiting_review/plan_stale | 补 run/map/commit/checkpoint/review 状态 | P1 |
| D123-10 | **成立** | #126 直接插 release + 设 published（`stock-migration`），无 waiting_review/202/409，无独立可见验证 | 走现有审核或 waiting_review；completed 需列表资格 | P1 |
| D123-11 | 部分 | 方案要求 Connector 无版本 pin；#126 connector 关系不写 `target_version_id`（与方案一致）；远端措辞“released and pinned”不准确 | 只改文案；无代码 | P2 |
| D123-12 | 部分/已过时 | 进度/PR 状态过时；delist-only/restore 旧口径 | 更新状态；同 D1/D2 | P2 |

## 6. 代码修改清单（按优先级）

### P0：会改变授权、数据安全、安装结果或必需交付范围

1. **A34 完整内容读取**（MP-03）
   - 新增 `GET /plugins/{plugin_id}/distribution`、`DistributionBundle`（`schema_version/root/graph/assets/bundle_digest`）、`ContentAsset/ContentFile`。
   - 同一短只读事务选根/refs/Connector effective；退出后读不可变文件并校验；JCS/16MiB；`no-store`；零 Fleet/零写入。
   - 抽出无 Runtime/Workspace/Secret 的固定图 resolver，安装与读取共用；客户端只调一次。
   - 文件：`internal/api/handler/plugin/`（新 handler/DTO）、`internal/service/plugin/`（resolver/assembly）、`internal/repository/plugin/`（批量 scoped 读）、OpenAPI。

2. **A01 `dependency_availability`**（MP-02 / #127）
   - 重做 DTO：`DependencyAvailability{status,evaluated_view,evaluated_version_id,reasons}`；`DependencyReason{code,target?,via?}`；固定 code `dependency_delisted/dependency_deleted/dependency_unavailable`。
   - 删除 `DependencyIssue` 旧 DTO；mine Expert/Team 必返（available 时 `reasons:[]`）；公共/其他类型省略。
   - 实现 `view`：mine 仅 editing、公共仅 published、Connector effective；非法组合 400 `details.field=view`。
   - Team 第二跳用边上的 `target_version_id`，不读成员 published；目标/via 逐项授权，不可披露合并一条匿名。
   - 在 `read.go` 的 COUNT/分页前、`history.go` 分类计数、`tags.go` 公共 tags 共用同一谓词；补 refs↔relations_json 完整性 500。
   - 回退 `service.go:496-503,582-588` 的非契约 404/root-only graph（或明确写入方案）。
   - 文件：`internal/model/plugin.go`、`internal/api/handler/plugin/handler.go`、`internal/service/plugin/service.go`、`internal/repository/plugin/{read,history,tags,dependency_availability}.go`、OpenAPI。

3. **A09 restore-to-draft**（MP-01）
   - `POST /plugins/{plugin_id}/versions/{version_id}/_restore`，只 owner，`expected_edit_revision`+新标签；复制旧文件/pins 为新 ID；不切 published；Connector 400。
   - 新增 handler/service/repo + OpenAPI；删除 `docs/api/plugin-version-management.md:75` 的“not part of this release”。
   - 决策：若不做，正式从 MP-01/API 删除 A09，不能继续留“必需”。

4. **private/下架目标矩阵**（MP-02/MP-03/MP-04）
   - 删除 `referenceAudience` 的 private→private 分支（`references.go:55`）与候选中的 private 返回（`reference_reads.go:141-144`）。
   - #126 安装/迁移按当前 listing + 矩阵阻断 delisted/private 目标，不能留“历史 pin 继续安装”豁免。
   - 测试：system→space、跨 Space、Admin 绕过、Team 递归、delisted/private install 全部失败。

5. **删除保护改为并集**（若 D1 选方案）
   - 移除 `rejectPublishedMutation` 的“曾发布总禁令”；`Delete/DeleteGraph` 用 `rejectLiveIncomingRelations` + `plugin_version_relation_refs` published 直接/间接（固定 Expert 版本）并集。
   - 补：草稿 live 边、current 去边但 published 仍引用、Team→固定 Expert→旧 Skill 间接引用、引用式来源删除不删共享目标。

6. **安装单一引擎 + 原 plan 重试**（MP-03）
   - A31/A32 共用 InstallCoordinator/attempt；A32 补 Idempotency-Key、用户身份、workspace UUID、header/body 一致。
   - 持久化不可变无凭据 plan/完整请求 + HMAC/key_id；同 key 重试只读原 plan，不重读 live connector/root；窗口 ≤24h。
   - 状态机 PREPARED→DISPATCHING→SUCCEEDED/FAILED/UNKNOWN + CAS/lease；已知成功本地回放，不重填 Secret；仅首次成功计数（同事务）。
   - 移除市场 legacy create/bind/delete 补偿；保留独立 `/experts`/`/squads` 共享实现。

7. **A29/A30 迁移协议**（MP-04）
   - 两次只读 preview（discovery → frozen `proposed_plan`/`plan_digest`），`file` 或 `source_plugin_id`；冻结标签/映射/server 分组。
   - apply 幂等、CAS、root map 仲裁、`source_commit_json`、checkpoint、waiting_review/blocked/completed；不指针回滚。
   - 把 #126 的 inventory/CLI 作为底层工具复用，不另造第二套；否则不要用 #126 关闭 #119/#121/#123。

8. **共存/安装命名治理**（D3）
   - 默认关闭 `mk1`/frontmatter 改写；迁移校验不要依赖未批准命名；若保留，补真实 Fleet/Daemon 双版本加载证据并更新方案 C07/§6/§7.1。

### P1：关键缺口

1. **CAS/relations 严格性**：`edit_revision` + `expected_edit_revision`；relations 指针判省略、缺失/null 400、[] 清空；import/update CAS；补跨资源 CAS 测试。
2. **declared_visibility/历史受众**：版本表加列；`versionAccess`/A05/A06/A07/A08 取交集；private v1 不因 v2 公开而泄露。
3. **Connector effective**：新表/单调 revision/digest；提审冻结 observations；批准变化 409 `dependency_changed`；A01/安装读 effective。
4. **冻结审核 DTO**：`source_version_id/source_edit_revision/frozen_content_digest/frozen_version|connector/connector_observations/baseline_version_id/dependency_diff`；坏快照 500；重复 approve 回放；submit 只接 version_id。
5. **A25 receipt/ZIP-only**：`plugin_mutation_receipts`；Idempotency-Key、输入摘要、7 天 tombstone、同事务回放；A24 拒绝 Expert/Team；A25 返回 `resolved[]`。
6. **A03/A10/A11 契约**：版本图 `plugin_id@version_id`/Connector effective、source_version_id、1500/2000；Candidate/Dependent 字段、via、`meta.has_hidden_dependents`、稳定排序。
7. **锁序/完整性**：统一 review→Plugin（排序）→Version→effective/placement/refs；锁内重验；死锁 ≤3 次；refs JSON_TABLE 双向校验 500。
8. **发布/读取 wire**：publish 必填 `version_id`；响应加 `published_version_id`；A04 `scope`；A06/A07 `version_id`；A08 统一 JSON 文件响应；VersionSummary/view/selected 字段。
9. **迁移协议细节**：显式标签（patch+1 仅建议）、批准拆组、reuse 精确身份、waiting_review/completed、独立可见验证。
10. **客户端/REL**：CL-01 Admin ZIP、CL-02 Web/CLI A34+诊断、CL-03 Buddy 单 A34；REL-01 四端/部署门禁/回滚演练。

### P2：文档、状态与追踪

- 已完成：方案目录移入 `docs/marketplace-v2/`，待提交。
- 更新 #114/#117/#118/#119–#123 的状态、PR 关联、进度；#126 的 `Closes #119 #121 #123` 必须改为 `Refs`，除非补齐 A34/A29/A30。
- 更新 G01/G04、D114-01/06、D121-04、D122-01、D123-12 的进度描述。
- 统一差异记录条数（80 条）。
- 修正 `D115-04`、`D115-05`、`D117-02`、`D118-05`、`D121-05`、`D123-11` 等“无代码差异”的表述，避免误派工。

## 7. 建议执行顺序

1. **本周先拍板 D1–D6**（删除生命周期、A09、共存、A01 DTO、迁移形态、private/下架目标）。这 6 项决定 #124–#127 的返工方向。
2. **冻结 #126 的合并条件**：当前不能以 `Closes #119 #121 #123` 合并；至少先把 A34、A29/A30、安装 Coordinator、原 plan 重试拆成可验收的后续 PR，或把 issue 改为 `Refs`。
3. **重做 #127 的 A01 契约**（C1–C6）并与 CL-02 对齐；在此之前不要让 Web 接 `dependency_issues` 旧 DTO。
4. **补 MP-01 基础缺口**（CAS、declared_visibility、Connector effective、frozen review、A09），因为 A34/A01 都依赖它们。
5. **实现 A34 + A29/A30**，再做 CL-01/02/03，最后 REL-01。
6. **每个端点 owner 同步 OpenAPI**；`make openapi-check`/`openapi-diff` 与真实 MySQL smoke 作为合并门槛；不要把文档/brief 的“已验证”当验收。

## 8. 本次核对边界

- 只读静态审计 `origin/main 59ca051`、PR #126 `94de531`/`e3a4d9f`、PR #127 `d7a1ffe`；没有运行服务、MySQL、Fleet、Daemon 或 UI。
- 远端引文：逐字复核 #115–#118；#119–#123 因 API 限流未重新抓取正文，沿用差异记录引文，状态已重查。
- 本地 `/home/mlamp/octo-web`、`/home/mlamp/octo-cli` 仅做 A34/版本协议关键词检查，未见接入；buddy 仓库未找到，不能据此判断其分支/未提交工作。
- 本文只给核对结论和修改建议，没有修改业务代码、远端 issue 或 PR。

## 9. 本轮契约冲突与开发差异处理（最新确认覆盖历史建议）

本节覆盖原 80 个唯一编号，不把决策确认当代码完成。2026-10-26 本轮远端读取：#124/#125 已合并；#126 更新到 e7ffc79，#127 仍 016baef；新增 #128 3467a39 为 #126 替代候选，尚未合并。原审计单独文件本轮被其他编辑过程移除，本节追加于仍存在的核对文档，不重建被移除文件。

#128 直接代码抽查：安装仍固定 pin/mk1/current Connector；两入口共享解析但 legacy 仍调用 ProvisionAgent/ProvisionSquad；attempt 仍重读图、只存指纹和 Completed、每次计数；installAccess 忽略依赖 listing；Rollback 仍直接更新 current/published 指针。新增 inline 负校验、锁前文件冻结、readiness SQL 和过期回执说明应保留。其作者报告的测试不等于本轮重跑；其余原审计细项仍待实施时复核。

### 9.1 产品裁定与契约修正

| 编号 | 冲突与实际含义 | 本轮处理 |
|---|---|---|
| Q1 | 同 key 原计划与“上游一更新就 409”冲突；网络超时后 S v2 发布会阻断原 S v1 重试 | 用户已选原 key 继续原计划；新 key 才取最新。已同步决策/API/技术/任务/plan；输入变化、下架/删除和权限失效另行校验，UNKNOWN 保留。 |
| Q2 | Connector“必须审核”与“Admin 原直接生效”冲突 | 已定位出处：D7 概括“编辑不立即生效”，未明确包括 Admin；现有 admin.go 及 00 C04、01 §4.3、02 §2.3 明确保留 Admin 直接生效。这是适用范围未写清，不应把 D7 推导为已批准新增 Admin 审核。用户随后明确确认 Admin 不用审核；本轮已统一决策、API、任务和评审入口，保留直接生效。 |
| Q3 | 要求依赖更新通知与“不新增推送/轮询”范围不清 | 用户已确认：仅记录审计、展示当前解析版本；本期不主动发送依赖更新通知。已同步 00/01/02 与任务验收。 |
| F1 | Team ZIP 成员 version_id 表结构必填但正文可选 | 已改 version_id?，它是可选审计值，不能固定依赖。 |
| F2 | 删除保护仍写固定 Expert 间接引用 | 已按 B 改为最新成员正式版本间接路径；live 草稿与来源 published 保护并集保留。 |
| F3 | 同一个 Skill 在同一 Team 请求返回两个历史版本的旧验收 | 已改为同一目标最新版本节点去重、保留多条边；不同 Skill ID 不合并。 |
| F4 | “一次快照避免列表 v2、安装 v3”夸大跨请求保证 | 已限定单次响应/首次 plan 内部一致；不同时间新请求允许更新，符合 B。 |
| F5 | CAS 缺失同时出现 400/409；导入 resolved 重放同时写“随查询变化” | 已统一缺 CAS/过期 409，缺 relations/null 400；同 key 导入回放首次 resolved。 |
| F6 | 技术设计末尾声称 API 尚未支持 B/审计版本/观察字段 | 这些字段已定义，已清除过时待办，保留动态摘要 fixture 重算要求。 |
| F7 | A32 一处要求 Workspace header 必填，另一处写“如同时提供” | 统一 header/body 均必填且相等；沿已确认的统一安装身份与 Workspace 契约，不允许两种来源矛盾。 |

### 9.2 80 条开发差异承接表

每行可归并同类编号；“取消”和“保留已有实现”不要求重写代码，其余是待实施/验收工作。Q2/Q3 不阻塞独立的版本、引用、删除、安装计划等工作。

| 原编号（完整覆盖） | 承接任务 | 当前修改与验收要求 |
|---|---|---|
| G01 G04 D114-01 D114-06 D121-04 D123-11 | 追踪文档 | 更新 PR/issue 映射；#128 为 #126 替代候选，Connector 无业务版本；追踪状态不能代替验收。 |
| G02 D118-01 | MP-02 | 移除曾发布总禁令，按 live 草稿引用、当前 published refs、Team 最新成员间接路径并集保护；不改变现有缩权限制。 |
| G03 D116-02 | MP-01/CL-02 | 取消 A09 恢复草稿及 restored_from 字段要求；保留历史读取，不再列为代码缺口。 |
| X01 D119-02 | MP-03/CL-02/03 | 实现 A34 单次最新完整图及原始文件；权限/摘要/预算/零 Fleet/零写入验收。#128 仍无端点。 |
| X02 D117-06 D118-08 | MP-02/CL-02 | 保留 #127 SQL 分页前过滤；按 B 最新图改造，补四字段、草稿诊断、逐节点授权匿名化、完整性错误。 |
| X03 D114-03 D120-07 D122-06 | CL-01/02/03/REL-01 | 明确四端对应 PR，验收 A01/A34/CAS、发布安装及真实运行；不以服务端合并替代。 |
| D114-02 D117-03 D123-01 | MP-04 | 取消 A29/A30 HTTP，保留 CLI 两次 preview/冻结批准计划；数据库来源优先，ZIP 不是默认本期交付。 |
| D114-04 D119-01 D120-03 | MP-03/04 | 移除 mk1 别名与 frontmatter 改写，普通安装保留原名原字节，同名不同内容失败；迁移不依赖别名。 |
| D114-05 D115-06 D116-07 D117-07 D119-06 D121-05 D122-07 D123-12 | MP-01/02/追踪 | 拆开共通项：删除按 D1 并集、A09 取消、共存拒绝、PR 状态更新；旧 delist-only/restore 待定结论失效。 |
| D115-01 | MP-01 | 新增版本拟定受众/摘要/保存序号；标签/受众/关系身份变化新建快照；浮动审计版本不改变内容身份。 |
| D115-02 | MP-01/客户端 | CAS 缺失/过期 409，relations 缺失/null 400；parse-task 保存原 ZIP/支持文件，并正确释放失败消费权。 |
| D115-03 | MP-01 | 历史读取取当前权限与版本受众交集，公开 v2 不追溯公开 private v1。 |
| D115-04 D115-05 D117-02 D118-05 | 保留已有实现，复验 | 保留历史对象保护、旧审核不可变校验、展示位与引用分离、既有审核角色；安装 plan 接入后复验对象保留。 |
| D116-01 | MP-01/CL-01 | A24 拒绝 Expert/Team JSON 写、A25 ZIP 负责内容、A21 支持 effective；Admin Connector 直接生效例外见 Q2。 |
| D116-03 D118-03 | MP-01/02 | 实现 Connector editing/effective 分离、审核观察和批准窗口检查；Admin 例外待 Q2 明确。 |
| D116-04 | MP-01/02 | 发布明确来源 version_id/标签并返回 published_version_id；重复 approve 回放结果，不重新激活历史版。 |
| D116-05 | MP-01/客户端 | 统一 view/VersionSummary/历史 scope 与 version_id；名称、搜索、排序及成员数按选定来源投影。 |
| D116-06 D118-04 | MP-01/02 | 冻结来源版本/修订/摘要及依赖观察；不覆盖新草稿；坏快照明确失败、批准幂等、审核详情完整。 |
| D117-01 | MP-02 | 保留同 owner private→private 合法分支；space/system→private 禁止。旧审计要求删除全部 private 分支作废。 |
| D117-04 | MP-02/CL-01 | A25 ZIP-only 补 Key/CAS/resolved；成员 version_id 可选审计；清单字段/角色/顺序严格校验。 |
| D117-05 | MP-02 | 取消 pin 必填，校验可选审计值归属；Connector 完全禁止版本字段；补关系身份、成员限制、refs 双向一致。 |
| D117-08 | MP-02 | 导入 receipt 与结果同事务，输入摘要、唯一抢占、7 天回放/tombstone；同 key resolved 使用首次结果。 |
| D118-02 D123-02 | MP-02/03/04 | 下架依赖禁止新安装/迁移发布，最新图不可用整体失败；保留合法 private。#128 installAccess 仍忽略依赖 listing。 |
| D118-06 | MP-02 | 影响查询返回来源版本/直接间接路径/隐藏引用标记，B 下沿最新成员；隐藏身份和数量不泄漏。 |
| D118-07 | MP-01/02 | 统一 review→排序 Plugin→Version/effective/refs 锁序；锁后重验、死锁限次重试；不持锁做对象/Fleet I/O。 |
| D118-09 | MP-02 | 图按本次解析身份，同一 Skill 多路径去重节点保留边；补候选接口/新建模式及图预算。 |
| D119-03 | MP-02/03 | 单次请求在一致性快照选根/依赖/effective，退出后读不可变文件；不同时间的新请求允许不同版本。 |
| D119-04 | MP-02/03 | 统一图 1500 节点/2000 边及文件/JSON 预算，MCP server 重名冲突，每成员独立合并，不截断。 |
| D119-05 | MP-03 | HMAC/key_id 校验原请求，运行时凭据不落盘；公开 MCP 占位符与内容校验明确。 |
| D120-01 D120-04 | MP-03/客户端 | #128 已共享 pinned 解析，但 legacy 仍 ProvisionAgent/Squad；两入口须共用 Coordinator/attempt 和 Key/用户/Workspace UUID 契约。 |
| D120-02 | MP-03 | Q1 已确认：同 key 原计划，上游正常发新版不冲突；新 key 才取最新。#128 仍重新 resolve 比较指纹，须改持久化原 plan。 |
| D120-05 D120-06 | MP-03 | 状态机/租约/首次发送截止/HMAC/UNKNOWN；成功本地回放、仅首次计数。#128 仍 Completed 布尔且每次 trackInstall。 |
| D121-01 D121-02 D121-03 | MP-04 | 补来源身份/范围/文件证据/未知历史和 pending fence；复用 #128 文件锁前冻结与 readiness SQL，不重复开发。 |
| D122-01 | REL-01 | 放行依赖 MP-03/04 和 CL-01/02/03；A09/A29/A30 不再阻塞，A34/跨端仍必需。 |
| D122-02 D122-03 | REL-01 | 复用 #128 readiness SQL/手册，补目标环境全入口旧执行器不可达证据；默认 false 不等于切换完成。 |
| D122-04 D123-06 | MP-04/REL-01 | 去掉 current/published 指针回切，历史/审计保留，用兼容二进制或新版本恢复；#128 Rollback 仍直接 UPDATE 指针。 |
| D122-05 | REL-01 | 形成审核维护窗口、pending fence、UNKNOWN 保留/人工核对流程；#128 已补过期回执说明，仍需实测。 |
| D123-03 | MP-04 | 标签由批准计划明确指定，冲突重新 preview；不默认 patch+1 或子节点固定 1.0.0。 |
| D123-04 | MP-04 | Connector 分组/拆组进入批准计划，逐源覆盖且不猜秘密/映射。 |
| D123-05 | MP-04 | 显式 reuse 目标 ID 与资格，版本仅观察；不按内容相似自动复用，不要求固定目标版本。 |
| D123-07 | MP-04（条件项） | 若新增对象材料化，清理仅限本批新建且无引用对象；DB-only 现状不凭空加清理动作。 |
| D123-08 | MP-04 | 两次 preview 产出冻结批准计划/plan_digest；apply 只认来源/映射/标签/分组，数据集摘要不能替代批准摘要。 |
| D123-09 | MP-04 | run/root map/来源提交记录/checkpoint 与 waiting_review/blocked/completed，可恢复逐步提交。 |
| D123-10 | MP-04 | 新资源走既有审核，等待人工批准后恢复；独立列表资格通过才 completed，不能直接插 release。 |

### 9.3 验证范围

本轮执行主工作区 `make openapi-check`：通过，0 errors、10 个既有 warnings；生成物与本地提交基线一致。这只验证本地已有 handler 规范，不代表 Markdown 中的新契约已经实现，也不覆盖 #128 全量测试。本轮没有修改业务代码或发布远端 issue/PR。

## 10. 五组意见修订闭合（文档层）

人工审核取消后新轮次提交、同 key 原计划与请求级一致性、当前引用删除保护、Admin 无审核/通知仅审计展示、迁移签名授权与 PR 基线已同步至 00/01/02、REVIEW、README、8 个任务及 plan.json。原 80 个差异编号映射保留；历史审计不重写为实现结果。此处记录文档修订，不宣称源码完成或远端 issue 已更新。

本轮修订验证：全部本地 Markdown 文件链接、代码围栏、plan JSON/8 issue 路径、任务 DAG、API 唯一所有权通过；原差异表 80 个编号仍各出现一次。`git diff --check` 通过。`make openapi-check` 通过（0 errors、10 个既有 warnings），生成物未改变。默认 `make openapi-diff` 对比 origin/main 59ca051 与较旧本地 HEAD 99cc261 报 9 errors/22 warnings（7 个路径缺失、2 个请求 oneOf 差异），属于基线差异，不能记为通过或本次接口删除；本次仅改文档。相对本地 HEAD 的生成物差异检查通过（No changes detected），不代替新契约实现验收。

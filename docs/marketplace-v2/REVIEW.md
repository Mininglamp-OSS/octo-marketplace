# Marketplace v2 方案评审指南（2026-10-26）

> **执行基线更新（2026-10-26）：** 已重新查询 GitHub 并 fetch：当前 main 为 `eec5223`，已合入 #124/#125/#128；#126 已关闭，#127 仍开放。下文旧状态表保留核验历史，不作为本轮施工基线。后续工作从已合并代码增量改造，既有版本/release/refs/attempt/migration 代码复用；任务及 PR 追踪见 execution.md。

**状态：五组评审意见已修订，待开发与实现验收。** 本目录已同步决策、wire、内部实现和 8 个开发 issue；文档闭合不代表源码已实现或运行验收通过。

## 0. 评审目标

请重点确认三件事：

1. **B 模型是否准确落实已确认决策**：Expert→Skill、Team→Expert 每次解析目标当前正式发布版；`target_version_id` 只作审计值；最新版不可用时不回退旧版。
2. **12 条决策是否被正确落到 API/技术/issue**：尤其是删除并集、A09 不做、无共存、A01 四字段、CLI 迁移、Connector effective、CAS、A32 同 key/新 key。
3. **有没有会导致返工的契约矛盾**：同一语义在 00/01/02/issues 是否一致；错误码/幂等/审核窗口/隐私是否可执行。

## 1. 阅读顺序

| 顺序 | 文件 | 看什么 |
|---|---|---|
| 1 | 本文件 | 评审范围、关键问题 |
| 2 | [00-decisions.md](00-decisions.md) | B 模型、D1–D10、C01–C08、A01–A09、迁移/审核/安装口径 |
| 3 | [02-api-contracts.md](02-api-contracts.md) | wire 唯一权威：A01/A34/A31/A32/A03/A10/A11、DTO、错误、幂等 |
| 4 | [01-technical-design.md](01-technical-design.md) | 内部模型、事务、最新解析、锁、审核、安装、迁移 |
| 5 | [issues/README.md](issues/README.md) + 8 个 issue | 交付切片、依赖、验收、PR 映射 |
| 6 | [plan.json](plan.json) | 机器可读依赖/所有权/API 映射 |
| 7 | [decisions-20261026.md](decisions-20261026.md) | 用户决策记录与理由（含 D4/D7/D8/D9 解释） |
| 8 | [review/](review/) | 决策前的代码事实审计与 PR 差距（历史背景，结论以 00 为准） |

## 2. 已锁定的 12 条决策

| ID | 决策 |
|---|---|
| B | Expert→Skill、Team→Expert 解析最新；Connector 按 ID 读 effective；Team 角色/Leader/顺序仍由 Team 边决定 |
| D1 | 保留原删除能力；保护 = live incoming（含草稿）∪ published refs ∪ 成员 Expert 间接；无“曾发布即禁止删除” |
| D2 | A09 restore 不做；无 `restored_from_version_id` |
| D3 | 不允许同名/多版本共存；同名冲突失败；无 `mk1`/frontmatter 改写；不改 Fleet/Daemon |
| D4 | A01 `dependency_availability` 四字段；evaluated_view/version 指来源自己的 published/editing；target.version_id 用最新解析值；无权匿名 |
| D5 | 生产迁移用 CLI + 离线授权；无 A29/A30 HTTP；两次 preview/冻结 plan_digest；数据安全回滚 |
| D6 | 同 owner private→private 允许；space/system 不得引用 private；最新依赖下架/删除导致隐藏 + A34/安装失败 |
| D7 | Connector effective + 审核观察；变化 409 `dependency_changed`；安装/A34 读 effective |
| D8 | `edit_revision` + `expected_edit_revision`；relations 缺失 400；Admin/Web/CLI/Bot 都改 |
| D9 | A32 保留 URL；A31/A32 同一引擎；同 key 同输入原 plan，上游正常发新版不阻断；显式安装输入变化 409 + 新 key 新安装；仅首次计数 |
| D10 | 部署门禁 + 老执行器不可达；兼容二进制 + 新版本恢复；不指针回滚 |
| 审核 | 默认自动（Space 可关）；自动内部重试；人工窗口变化 409；批准后依赖继续浮动 |

## 3. 评审重点（建议逐项打勾）

### B 模型

- [ ] 按已确认定义解析当前正式发布版；不可用时整体失败，不搜索旧的“可用版本”。
- [ ] 最新版不可用时**不回退旧版**，检查实现与负例测试。
- [ ] Team→Expert 按已确认规则浮动；成员内容跟随最新，Team 角色/Leader 仍由 Team 边决定。
- [ ] A01/A34/首次安装/审核各自一次请求内部使用一致性快照；先后请求可以分别解析 v2、v3。同 key 重试仍使用首次计划。

### 删除与生命周期（D1）

- [ ] 删除保护 = live incoming（含草稿）∪ published refs ∪ 成员 Expert 间接，是否覆盖真实引用。
- [ ] 无“曾发布即禁止删除”后，已安装用户可能因删除受影响；是否接受（当前决策接受）。
- [ ] 只改删除、不改“已发布资源收窄 visibility”的现有行为，是否明确。

### A01 诊断（D4）

- [ ] `evaluated_view/evaluated_version_id` 指来源自己的 published/editing，而不是依赖版本，是否清楚。
- [ ] 已发布来源始终诊断 published，仅从未发布才 editing；是否满足“草稿修好但线上仍坏”的场景。
- [ ] 无权依赖合并一条匿名 `dependency_unavailable`，不泄漏名称/ID/数量；是否足够。
- [ ] mine 仅 editing、公共仅 published、Connector effective；非法组合 400 `details.field=view`；是否与 Web 计划一致。

### Connector effective（D7）

- [ ] Tenant 需审核的 Connector 编辑批准后生效；审核 observations 变化 409 后按原权限取消并重提新 review_id，不修改旧基线。
- [ ] Admin 不用审核（用户已确认）；既有写入直接生效并同步 effective，仍验证权限、CAS 和依赖范围。
- [ ] 已安装实例不受新 effective 影响；新安装用新 effective；是否清楚。

### CAS（D8）

- [ ] `edit_revision` 从 1 开始；更新必带、新建省略；缺失/过期 409；relations 缺失 400；四端都改。
- [ ] 灰度/旧客户端 400 的迁移策略是否可接受。

### 安装（D9）

- [ ] A32 URL 保留、内部接同一 Coordinator、要求 Key/用户/UUID；旧客户端升级前 400。
- [ ] 同 key 同输入只重放首次冻结 plan；上游正常发新版不阻断，显式输入变化 409；新 key 新安装用最新；仅首次成功计数。
- [ ] 状态机/UNKNOWN/lease 是否足够；A32 不再走 legacy create/bind/delete。

### 迁移（D5）

- [ ] CLI + 离线授权；两次 preview/冻结 `plan_digest`；apply 只认冻结计划。
- [ ] 数据安全回滚：不恢复 current/published 指针、不删行/文件，forward-fix 新版本；是否满足“回滚必须数据安全”。
- [ ] 新版本走现有审核（默认自动/人工 waiting_review）；不直接插 release。
- [ ] 迁移后各节点独立可见；completed 条件是否足够。

### 共存/权限/兼容

- [ ] 同名/多版本共存拒绝；普通安装保留市场原名/原字节；不改 Fleet/Daemon；是否满足产品预期。
- [ ] 同 owner private→private 允许；space/system 不得引用 private；是否与现有 private 资产兼容。
- [ ] A09/A29/A30 完全移出范围；是否还有客户端/文档依赖。

### 错误/幂等/隐私

- [ ] `dependency_changed`/`idempotency_input_mismatch`/`installation_result_unknown` 等错误码是否覆盖真实场景。
- [ ] 无权目标不泄漏存在性/名称/数量；private/删除/下架匿名规则是否一致。
- [ ] A25 receipt 的 7 天回放/tombstone/同事务；A31/A32 同 key 域。

## 4. 已确认边界与实现检查

1. **Admin 不用审核**，直接生效并同步 effective；Tenant 沿用原审核策略。依赖更新**仅审计展示**，无主动通知。
2. **A03 历史根**可选 version_id，但依赖最新；A34 不允许根版本覆盖。Team 成员同名按解析后名称报 team_member_name_conflict。
3. **删除间接保护**只取成员 Expert 当前正式发布版，不扩大到全部历史；live 草稿引用仍保护。
4. **人工审核恢复**取消旧申请后重提新 review_id；版本化源可同版本，Connector 重验当前 CAS/内容；旧观察值不可变。
5. **离线授权**格式、签名、信任来源、有效期、撤销与同 run 续签恢复固定按 02 §5.1；命令帮助可细化，但不能改变上述契约。

## 5. 代码/PR 基线（评审时不要误判）

| PR | 状态 | 评审结论 |
|---|---|---|
| #124 | 已合入 | 保留版本/发布底座；需补 CAS、Connector effective、读投影；不补 A09 |
| #125 | 已合入 | refs 改浮动（`target_version_id` 仅审计）、删除并集、private 同 owner、observations |
| #126 `e7ffc79` | 核验时开放；#128 为替代候选 | 两套安装器、重试重读 live、指针回滚、`mk1`、无冻结 plan；按 MP-03/04 重做 |
| #127 | 开放，**不要按现状合并** | `dependency_issues` 旧 DTO、只诊断 published/pin、无 view/四字段/匿名；保留 SQL 前置思路重做 |
| #128 `3467a39` | 核验时开放，尚未按 B/D 完成 | 保留 inline 负校验、锁前文件冻结、readiness SQL、过期回执说明；改固定 pin/mk1/live Connector、legacy 执行器、重解析重试及指针回滚。 |

以上为 [2026-10-26 核验快照](review/diff-verification-20261026.md)，不是当前远端实时状态；实施前复核 head 和合并状态。#126/#128 按替代关系选基线，不重复合入。

## 6. 验证状态

- 本目录内相对链接已检查；Markdown 表格/代码围栏一致；`plan.json` 为合法 JSON（schema 1.1）。
- **未运行**服务、MySQL、Fleet、Daemon、客户端 smoke；本方案不是实现验收。
- `03-github-issues-114-123-diff.md` 是历史比对，与 00 冲突处按 00 执行。


本轮修订验证：全部本地 Markdown 文件链接、代码围栏、plan JSON/8 issue 路径、任务 DAG、API 唯一所有权通过；原差异表 80 个编号仍各出现一次。`git diff --check` 通过。`make openapi-check` 通过（0 errors、10 个既有 warnings），生成物未改变。默认 `make openapi-diff` 对比 origin/main 59ca051 与较旧本地 HEAD 99cc261 报 9 errors/22 warnings（7 个路径缺失、2 个请求 oneOf 差异），属于基线差异，不能记为通过或本次接口删除；本次仅改文档。相对本地 HEAD 的生成物差异检查通过（No changes detected），不代替新契约实现验收。

## 7. 建议评审人

| 角色 | 重点 |
|---|---|
| Marketplace 后端 | 00/01/02、A01/A34/安装/锁/refs/迁移 |
| Admin/Web/CLI/Buddy | D4 诊断、A34 单次、CAS、同 key/新 key、旧入口迁移 |
| DBA/迁移 | CLI 冻结计划、数据安全回滚、map/checkpoint、独立可见 |
| 安全/隐私 | private 受众、匿名原因、A34 完整内容、Secret 边界、跨 Space |
| REL/运维 | 部署门禁、旧执行器不可达、四端证据、回滚演练 |

## 8. 评审意见怎么提

- 优先指到文件 + 章节，例如“02 §2.2.1 / A01 的 evaluated_view 语义”。
- 区分三类：**阻塞**（会改变授权/数据/安装结果）、**实现细节**（编码时确认）、**文案**。
- 若发现 00/01/02 冲突，先改 00（唯一决策源），再改 02（wire），最后改 01/issues；不要在代码里自行降级。
- 评审通过后，建议按 `README.md` 的 issue 顺序拆代码任务，先完成 MP-01 版本/effective/CAS 底座，再完成 MP-02 共享解析与依赖治理；随后 MP-03 实现安装/A34，MP-04 与 CL-01 可并行。

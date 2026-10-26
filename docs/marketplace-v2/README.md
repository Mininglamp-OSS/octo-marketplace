# Marketplace 技术方案与可执行开发 issues（B 模型）

> **执行基线更新（2026-10-26）：** 已重新查询 GitHub 并 fetch：当前 main 为 `eec5223`，已合入 #124/#125/#128；#126 已关闭，#127 仍开放。下文旧状态表保留核验历史，不作为本轮施工基线。后续工作从已合并代码增量改造，既有版本/release/refs/attempt/migration 代码复用；任务及 PR 追踪见 execution.md。

**状态：2026-10-26 最终决策已锁定。** 引用模型为 **B：Expert→Skill、Team→Expert 每次解析目标当前正式发布版**；不再使用精确版本 pin 作为公共读取/安装依据。A09 restore、同名共存、A29/A30 HTTP 迁移已明确不做或改为 CLI。完整决策见 [决策记录](decisions-20261026.md) 与 [规则裁定](00-decisions.md)。

A34 单请求完整内容读取仍为确认范围，按 B 返回“最新解析图 + 全部原始文件”；Admin ZIP、Fleet 现有幂等 POST、原权限审核及市场动作保持；同名/多版本共存明确拒绝，普通安装保留市场原名/原字节，不改 Fleet/Daemon。

> **评审入口：** 先读 [REVIEW.md](REVIEW.md)，里面列了评审重点、已确认决策、已知实现默认和“不要按现状合并”的 PR。决策记录见 [decisions-20261026.md](decisions-20261026.md)，决策前代码审计见 [review/](review/)。

## 阅读顺序

0. [REVIEW · 评审指南](REVIEW.md)：评审范围、重点、默认项、PR 基线。
1. [00 · 最终决策与规则裁定](00-decisions.md)：D1–D10、B 模型、C01–C08、A01–A09、迁移/审核/安装口径。
2. [02 · 跨服务 API 契约](02-api-contracts.md)：A01/A34/A31/A32、DTO、错误、幂等与恢复（B 语义，wire 唯一权威）。
3. [01 · 完整技术方案](01-technical-design.md)：领域、数据、状态、权限、读取、安装、迁移、回滚、验收（B 语义）。
4. [开发 issues 与依赖图](issues/README.md)：4 个 Marketplace 主干、3 个客户端接入、1 个联合切换。
5. [plan.json](plan.json)：8 个 issue 的硬依赖、仓库、API/表 owner。
6. [decisions-20261026.md](decisions-20261026.md)：用户决策记录与 D4/D7/D8/D9 的通俗解释。

历史比对：[GitHub #114–#123 与固定 pin 方案的差异记录](03-github-issues-114-123-diff.md) 保留为历史；其中与 B 冲突的段落不再代表当前方案。

## 方案权威

本轮用户决策 → [00-decisions](00-decisions.md) → [API 契约](02-api-contracts.md) → [技术方案](01-technical-design.md) → issue 执行说明。任何内部矛盾必须先修文档，不能在代码里自行降级或创造第二套规范。

## B 模型要点

- **最新目标版本** = 目标 `published_version_id`，且 `status=1`、未删除、`listing_state=published`；没有/不可用时不回退旧版。
- **Expert→Skill、Team→Expert 浮动**；Connector 按 ID 读 effective。
- **Team 的角色/Leader/顺序**仍由 Team 边决定；成员 Expert 发新版只更新内容和它引用的 Skill。
- **A01/A34/安装/审核**复用同一个最新解析服务；一次请求一个一致性快照。
- **审核**默认自动（Space 可关）；自动模式内部重试；人工模式窗口变化才 409；批准后继续浮动，依赖发新版不要求重新审核引用方。
- **代价**：同一个 Expert/Team 版本不同时间读取/安装可能不同；A34 bundle_digest 动态；审核不冻结批准后的依赖内容；安装只保证同 key 重试首次冻结 plan。

## 已确定不做

- A09 restore-to-draft。
- 同名/多版本共存、`mk1` 别名、frontmatter 改写。
- A29/A30 HTTP 迁移（改为 CLI + 离线授权）。
- “曾发布即禁止删除”的总禁令。
- 新生命周期状态、新审核角色、Fleet/Daemon 改造。

## 源码核验依据

源码按项目 AGENTS 约定读取；本轮以 `origin/main 59ca051`（#124/#125 已合入）、PR #126 `e7ffc79`、PR #127 `016baef`、替代候选 #128 `3467a39` 为基线。核验的是本地/远端 checkout，不推断未验证的运行时行为。

| 事实 | 主要证据 |
|---|---|
| 两安装 URL | Marketplace `internal/api/handler/plugin/handler.go`、`install.go`、`installations.go` |
| 当前 Approve/发布事务 | `internal/repository/plugin/review.go`、`version_review.go`、`version_management.go` |
| 删除保护现状 | `internal/repository/plugin/references.go:254`、`write.go:838-1019` |
| 旧图按 plugin_id/current | `internal/service/plugin/service.go`、`internal/repository/plugin/read.go` |
| Fleet 现有能力 | 方案已核验的 `pkg/capability/validation.go`、`143_skill_owner_unique` 等；本轮未重新拉取最新 Fleet 源码 |
| 锁/refs 现状 | `internal/repository/plugin/references.go`、`review.go` |

## 逐文档审查与编码就绪边界

本轮已完成 00/01/02、issues、plan.json 的 B 模型对齐；编码前必须联读 [00](00-decisions.md) 与 [02](02-api-contracts.md)。本目录运行验证只覆盖文档链接/JSON 样例/依赖 DAG；服务、MySQL、Fleet、Daemon、客户端 smoke 是实现 issue 的验收要求，不冒充已通过。

| Issue | B 模型下的直接编码依据 | 完成依赖 |
|---|---|---|
| [MP-01](issues/01-version-publication.md) | 版本/文件/current-published/release、CAS、读投影、A01 底座；无 A09 | 无 |
| [MP-02](issues/02-dependency-governance.md) | 浮动引用/最新解析、A01 四字段诊断、删除并集、Connector effective、A25 receipts、A03/A10/A11 | MP-01 |
| [MP-03](issues/03-installation-fleet.md) | A34、A31/A32 同一 Coordinator、同 key 原 plan、新 key 最新、状态机、无共存 | MP-02 |
| [MP-04](issues/04-migration.md) | CLI 两次 preview/冻结计划、可恢复 apply、数据安全回滚、独立可见；无 A29/A30 HTTP | MP-02 |
| [CL-01](issues/05-admin.md) | schema2 ZIP、A24 拒绝 Expert/Team、A25 Key/CAS/resolved、固定预览 | MP-02 |
| [CL-02](issues/06-web-cli.md) | 显式引用/审核/版本读取/A34 一次完整内容/mine 四字段诊断/CAS/新 key 重装 | MP-02、MP-03 |
| [CL-03](issues/07-buddy.md) | 单 A34、无依赖补取、本地边界、IPC 错误/pending | MP-03 |
| [REL-01](issues/08-cutover.md) | 真实四端/数据层/Fleet/Daemon、部署门禁、旧执行器不可达、数据安全回滚 | MP-03、MP-04、CL-01、CL-02、CL-03 |

编码时发现真实协议差异，先改 00/02，再改代码；禁止客户端猜字段或运行时降级。OpenAPI 生成及服务/页面/数据库 smoke 仍在各 issue 实现阶段执行，本轮只验证文档与契约样例。

## 五组评审修订状态

决策、API、技术方案、8 个任务及 plan.json 已同步本轮修订，状态为“文档修订完成，待开发与实现验收”。Admin 不用审核；通知仅审计展示；人工审核观察冲突走取消后新一轮提交；安装原 key 不因上游正常更新而改变；删除不扩大到历史关系超集。离线迁移授权以 02 §5.1 为准。

PR 基线是 2026-10-26 已核验快照，不代表实时状态；实施前重新核对 head。#128 为 #126 替代候选，复用其中安全修复，不能把两份实现重复合入；具体增量按 issues/README.md 分配。

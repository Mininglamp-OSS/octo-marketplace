# Marketplace v2 决策记录（2026-10-26）

> 本文记录用户已拍板的决策、落地口径，以及对 D4/D7/D8/D9 的通俗解释。用户在本次对话中再次明确确认：**Team→Expert 也跟随当前正式发布版；曾发布资源在没有保护引用时可删除**。分析背景见 [PR124–127 差距与决策](review/pr124-127-gap-and-decisions.md)。

## 1. 已拍板

| 决策 | 用户选择 | 落地口径 | 代码/文档动作 |
|---|---|---|---|
| **D1 删除生命周期** | **保留原有删除能力；按引用关系保护；不新增“曾发布即禁止删除”** | 删除前检查 live incoming（含草稿、不过滤 listing）∪ published refs ∪ Team→Expert→Skill 间接引用；没有保护引用的曾发布资源也可删 | 去掉 `rejectPublishedMutation` 的曾发布总禁令；`Delete/DeleteGraph` 改为并集保护；改测试；更新方案 G02/C03/API A19 以明确“无总禁令” |
| **D2 A09 restore** | **不要** | 不做 restore-to-draft；保留历史读取 A04–A08 | 从 MP-01 owns_api_ids/验收、API §3.1、plan.json、CL-02 删除 A09；技术设计 §3.1/§4.1 的 `restored_from_version_id`/恢复条目删除；代码无新增 |
| **D3 同名/多版本共存** | **不允许；同名冲突失败** | 普通安装保留市场原名/原字节；同名不同内容安装冲突，不生成别名、不改写 frontmatter | 删除 `mk1` 默认命名/`SKILL.md` 改写；迁移校验不依赖 `mk1`；更新测试；方案 C07/§6 从 “deferred” 改为 “本期不支持，冲突失败” |
| **D4 A01 诊断 DTO** | **已确认：B 模型 + `dependency_availability` 四字段**（见 §2/§9） | B：Expert→Skill、Team→Expert 都解析最新；诊断区分 Expert/Team 自己的 published/editing；无权依赖匿名 | 重做 #127 的 DTO/查询：四字段、固定 code、view、匿名聚合；target.version_id 用最新解析值 |
| **D5 生产迁移** | **本期要做；接受 CLI；回滚必须数据安全** | 本期用 CLI + 离线授权做生产迁移（不做 A29/A30 HTTP）；CLI 内保留两次 preview/冻结摘要；回滚不恢复 current/published 指针 | 改 `stockmigration`：加冻结 plan/digest、apply 只认冻结计划；删除指针回滚；#126 的 `Closes #119` 去掉；#121/#123 验收改为 CLI 口径 |
| **D6 private/下架目标** | **同 owner private 可作为依赖；下架 Skill 后引用它的专家不可见（PR127 口径）** | 保留 `private 来源 → 同 owner private 目标`；space/system 来源不得引用 private；下架依赖的 Expert 公共列表/详情隐藏，A34/安装按同一可用性 gate 失败；owner 在 mine 看到诊断 | 保留 `references.go:55`；删除方案“private 一律禁止”的表述并改为“同 owner private 允许，其余禁止”；给 A34/安装加 availability gate；补下架/私有负测试 |
| **D7 Connector effective** | **Admin 不审核；Tenant 沿用原审核策略** | Admin 直接写入 effective；需审核的 Tenant 编辑批准后产生新的 effective；Expert/Team 审核冻结 effective_revision/digest，变化 409 | 新增 `plugin_connector_effective` + observations + 读路径改造（§3） |
| **D8 CAS** | **Admin/Web/CLI/Bot 都带版本令牌，都改** | `edit_revision` + `expected_edit_revision`；更新必带，新建省略；缺失/过期 409；relations 缺失 400 | 迁移/模型/handler/service/repo/四端客户端/OpenAPI（§4） |
| **D9 安装引擎/重试** | **保留 A32 URL；允许按最新配置来 = 新 key 新安装** | 同 key 只重放原 plan；显式安装输入变化 409；上游正常发新版仍重放原 plan；想用最新内容则新 key 新安装；A32 接同一引擎，要求 Key/用户/UUID | A31/A32 共用 Coordinator/attempt；删市场 legacy 补偿；补状态机/UNKNOWN/仅首次计数（§5） |
| **D10 灰度/退役** | **旧市场执行器在新改造完成后不可达；回滚接受兼容二进制 + 新版本恢复** | 部署层隔离 + 全入口阻断；flag 只作额外手段；不恢复指针/legacy | REL-01 增加部署 gate 与拒绝证据；删除指针回滚；保留历史/对象/attempt |

## 2. D4 · A01 的 mine 诊断到底返回什么

**先回答你的问题：** 是的，本质就是“保存草稿”和“正式发布”的区别；但不是让 Web 自己判断，而是后端明确告诉它“这次检查的是哪个版本”。

**以下固定 pin 说明是决策前的分析背景，不代表最终规则。最终采用 §9 的 B 模型：Expert→Skill 和 Team→Expert 均在读取时解析当前正式发布版。**

**决策前讨论的问题：专家引用 Skill 是“固定版本 pin”，还是“每次读最新版”？**

- **当前方案和 #125 代码是固定 pin：** E v1 发布时，快照里写死 `target_version_id=S v1`。之后 S 发布 v2，E v1 仍然引用 S v1；公共市场展示的是 E v1 的发布快照，不会自动变成 S v2。
- 所以才会出现：S v1 下架、S v2 正常，E v1 的公共依赖仍然坏；你的草稿 E v2 改成 pin S v2 后，必须**正式发布 E v2**，公共市场才会恢复。
- **如果你要的是“公共市场每次读 S 的最新版”**，那就不是固定 pin，而是浮动引用：E 的发布快照不再存 `target_version_id`，公共列表/A34/安装每次解析最新 S；审核无法冻结依赖图，安装不可复现，A34 每次结果可能不同。这会改动 MP-01/02/03、A34、审核、迁移和全部客户端，**不是只改 D4 的字段**。

最终已选择公共市场跟随最新版；下文的固定 pin 场景仅用于解释旧实现与 DTO 差异。B 模型下的 D4 语义见 §9.2。

**如果是固定 pin，你要定的不是字段名，而是三件事：**

**场景：** 专家 E 有 published v1，同时你正在编辑 current v2。

- 公共市场看的是 **v1**；“我的发布”这一行可能展示你的 **v2 草稿**。
- 如果 S v1 下架，而你已在 v2 里把依赖换成 S v2：
  - 线上 v1 仍然坏，公共市场应该隐藏 E；
  - 你的草稿 v2 已经修好。
- 如果接口只返回“E 有依赖异常 / E 正常”，Web 无法知道是“线上 v1 坏”还是“草稿 v2 好”，就可能误报“E 已恢复”。

**所以你要定的不是字段名，而是三件事：**

1. 有 published 版本时，诊断 **published**，还是诊断 current 草稿？——建议 published。
2. 从来没有 published 时，诊断 **current 草稿**吗？——建议是，并标记 `editing`。
3. 你看不到某个依赖时，返回具体名字，还是匿名？——建议匿名。

**我建议的结构（就是方案 §2.2.1）：**

```json
"dependency_availability": {
  "status": "unavailable",
  "evaluated_view": "published",
  "evaluated_version_id": "E-v1",
  "reasons": [
    {"code": "dependency_delisted",
     "target": {"plugin_id":"S","plugin_type":"skill","plugin_name":"部署检查","version_id":"S-v1"}}
  ]
}
```

- `status`：这次诊断的依赖图是否可用（available/unavailable）。
- `evaluated_view`：检查的是 `published` 还是 `editing`（草稿）。
- `evaluated_version_id`：具体检查的版本 ID，Web/排障不用猜。
- `reasons`：固定 code；`target` 只有你有权看时才给；`via` 用于 Team 的间接路径。
- 无权/缺失不可确认：只返回 `{"code":"dependency_unavailable"}`，不带 target/via/数量。
- 公共列表不带该字段；只有 `mode=mine` 的 Expert/Team 必返；正常时 `status=available, reasons=[]`。

**三种典型返回：**

| 场景 | 返回 |
|---|---|
| E 已发布 v1，S v1 下架，草稿 v2 已修 | `status=unavailable, evaluated_view=published, evaluated_version_id=E-v1, reason=dependency_delisted target=S v1`；Web 显示“线上发布版异常”，不是“已恢复” |
| E 从未发布，current v1 依赖下架 S | `status=unavailable, evaluated_view=editing, evaluated_version_id=current v1, reason=dependency_delisted target=S v1`；Web 显示“草稿依赖异常（尚未发布）” |
| 你无权看 S | `status=unavailable, evaluated_view=published/editing, reasons=[{"code":"dependency_unavailable"}]`，无 target/via |

**PR #127 实际：**

```json
"dependency_issues": [
  {"target_plugin_id":"S","target_name":"部署检查","relation_type":"expert_skill","reason":"delisted","via":[]}
]
```

没有 `status/evaluated_view/evaluated_version_id`；只诊断 published 行；目标名称/ID 可能对无权用户可见；没有匿名合并；没有 `view` 参数。

**我的建议：采用上面的 `dependency_availability` 四字段结构，重做 #127 的 DTO/查询；保留它已经做好的 SQL 前置过滤/COUNT/facets。**

最终诊断口径见 §9.2；本节的 S v1 示例属于旧固定 pin 场景，不作为新版验收样例。

## 3. D7 · Connector 的“有效态”是什么

**已拍板：采用 A（effective 模型）；Admin 不用审核、直接生效，Tenant 沿用既有审核策略。**

**这意味着：**

- Tenant 需审核的 Connector 编辑只写 editing，不立即生效；Admin 写入直接生效并同步 effective。
- 需审核的 Connector 自己提审/批准后，才产生新的 `effective_revision/effective_digest`；旧 effective 保留。
- Expert/Team 提审时记录所有 Connector 的 `effective_revision/effective_digest`。
- 批准 Expert/Team 时比对；变了返回 409 `dependency_changed`；只改 editing 不误判。
- A34/安装读 effective（最新有效，包含 Admin 直接生效写入），不读 editing。
- 已安装实例不受影响；新安装用新的 effective。

**要改：** 新增 `plugin_connector_effective` 表/迁移；Admin 写入和审核事务；review observations；A01/A34/安装读 effective；回填现有 Connector 为 revision 1；测试/OpenAPI。

## 4. D8 · CAS 是什么

**已拍板：Admin/Web/CLI/Bot 都带版本令牌，都改。**

- 新增 `edit_revision`（从 1 开始）；更新必须 `expected_edit_revision`，新建省略；不匹配 409 `edit_conflict`。
- `relations` 必填数组；缺失/null 400；只有显式 `[]` 才清空。
- Skill parse-task import 更新也要 CAS。
- 四端客户端（Admin/Web/CLI/Bot）同步改；OpenAPI 更新；在 version-management flag 后灰度，旧客户端在升级前会收到 400（带明确 hint）。
- 为什么用 `edit_revision` 而不是 `current_version_id`：保存计数和版本 UUID 分离，客户端不用理解 release，方案也已定义。

## 5. D9 · 安装重试与“旧入口兼容”是什么意思

**两个安装入口：**

- **A31** `POST /plugins/{plugin_id}/installations`：新入口。
- **A32** `POST /plugins/install`：旧入口，现有 Web/CLI/Buddy 在用。

**“旧入口兼容”= A32 是否继续接受旧请求**（没有 Idempotency-Key、没有用户-only、没有 Workspace UUID）。

可选做法：

1. **保留 URL，但不保留旧行为**：A32 内部接同一个引擎，要求 Idempotency-Key/用户/UUID；旧客户端升级前会 400。推荐。
2. **完全保留旧行为**：A31 新引擎、A32 旧补偿；两套引擎，可能重复创建，不推荐。
3. **删除 A32**：等所有客户端迁移完再删。

**“允许按最新配置来”有两种解释：**

1. **同 key 重试时重新读最新配置，发不同请求。** 危险：上次可能是 UNKNOWN（Fleet 已创建也可能没创建）；同 key 不同请求可能冲突或重复创建，而且可能要把 Fleet 改成接受不同请求。**不推荐。**
2. **用户想改用最新内容时新建安装**：原 key 同输入继续首次计划；上游正常发新版不触发 409。显式改变安装输入才返回 409，并要求新 key。

**已确认的口径：保留 A32 URL；配置变了用新 key 新安装；同 key 只重放原 plan。**

- **A32 保留 URL，但不保留旧行为**：内部接同一个引擎，要求 Idempotency-Key/用户-only/Workspace UUID；旧客户端升级前会 400（带明确 hint）。
- **同 key 重试**：只重放首次冻结的 plan/请求，不重新读最新配置。
- **上游正常发新版**：原 key 继续原 plan。**用户改变安装输入**：409 `idempotency_input_mismatch`；想取最新内容则明确新 key 新安装。原节点下架/删除及权限失效仍执行资格校验，UNKNOWN 不被错误响应抹除。
- 这样“允许按最新配置来”落在**新意图**上，不会和上一次 UNKNOWN 结果打架，也不需要改 Fleet 协议。
- A31/A32 最终共用一个 InstallCoordinator + attempt/状态机；仅首次成功计数；A32 不再走 legacy create/bind/delete 补偿。

## 6. D3 的 Fleet 侧结论

- **Fleet 协议本身不需要改：** Fleet 的 Capability 接受不同 `Skill.Name`；两个不同安装名可以表达两份 Skill，专家分别绑定 Fleet 返回的 Skill ID；同一个 Definition 内同名才拒绝（方案已核验 Fleet `pkg/capability/validation.go`、`143_skill_owner_unique` 等）。
- **但“Daemon/Codex 真的能同时加载两份且不混版本”没有验证：** 需要真实 Fleet → PostgreSQL → Daemon 联调，检查目录/frontmatter/支持文件/共享扫描根；方案明确不允许为此改 Fleet/Daemon。
- **按你的条件：** 如果只看 Fleet 代码，答案是“不需要改”；但本期没有运行时证据，且方案不允许改 Daemon。因此**本期按“不允许共存、同名冲突失败”执行**；以后要做，先做真实联调，发现要改 Fleet/Daemon 就停。

## 7. 固定 pin 下，Skill 更新会不会自动传到 Expert？

> 本节解释 PR #125 的旧固定 pin 行为，是历史对照；最终 B 模型见 §9，不能按本节派开发任务。

**不会自动传。** 固定 pin 的含义就是：Expert 的已发布快照写死了它当时引用的 Skill 版本。

举例：Expert E v1 固定引用 Skill S v1。

| 事件 | E v1 的公共内容/安装结果 |
|---|---|
| S 发布 v2 | 仍是 S v1；E 不会自动变成 v2 |
| 有人编辑 S 的 current 草稿但没发布 | 完全不影响 E（E 只认已发布 pin） |
| S v1 被下架/删除 | E v1 变成坏依赖：公共市场隐藏，安装/A34 失败 |
| 你编辑 E 的草稿，把 pin 改成 S v2 | 只有草稿变化；公共 E v1 仍是 S v1 |
| 你发布 E v2（pin S v2）并通过审核 | 公共市场/新安装才开始用 S v2 |

**为什么方案选固定 pin：** 审核能冻结确定的依赖图；A34 每次返回同一版本；安装可复现；历史不被后续 Skill 更新偷偷改写。

**代价：** Skill 更新后，引用它的 Expert 需要**显式发新版本**才会跟进；S v1 下架后，旧 Expert 会变坏，直到 owner 发新版本。

**建议的产品补偿（不改固定 pin 模型）：**

1. Expert 编辑页/我的发布显示“依赖有更新”：S v1 → S v2，可一键“基于最新依赖创建新 Expert 草稿”。
2. S v1 下架时通知引用它的 Expert owner：需要重新 pin 并发布。
3. 不做“自动改已发布 Expert 的 pin”，否则等于绕过审核。

如果你要的是“Skill 一更新，所有引用它的 Expert 自动用新版”，那就不是固定 pin，而是浮动 latest（或 semver range）；这会重新引入审核漂移、安装不可复现、A34 结果变化，需要单独决策，不建议本期顺手改。

## 8. “自动跟随最新版”和 B（公共市场每次读最新）是同一种吗？

**不完全是。B 是“自动跟随”的一种实现，但“自动跟随”还可以做成保留固定 pin 的自动新版本。** 关键区别是：**谁在什么时候解析版本、Expert 要不要出新版本、审核能不能冻结。**

| 方案 | 版本怎么存 | 什么时候解析 | Expert 自动出新版本？ | 公共内容自动变？ | 审核/可复现 |
|---|---|---|---|---|---|
| **A 固定 pin（当前）** | Expert 快照存精确 `target_version_id` | 发布时 | 否，owner 手动改 pin 并发版 | 否，直到发新 Expert 版本 | 强 |
| **B 读时最新** | 不存 pin（或存 ID 不存版本） | 每次公共列表/详情/A34/安装 | 否 | **是，Skill 一发新版就变** | 弱：审核无法冻结依赖图，安装不可复现 |
| **C 发布时取最新，随后固定** | 存精确 pin | Expert 发布时校验/取最新 | 否，owner 发版时自动选最新 | 否，直到发新 Expert 版本 | 强 |
| **D 自动生成 Expert 新版本** | 存精确 pin | Skill 发新版时，为引用它的 Expert 自动生成**新草稿**（pin 改最新） | 是（草稿），owner 审核/发布 | 否，直到新 Expert 版本发布 | 强（仍走审核） |
| **E semver range** | 存范围（如 `^1.0.0`） | 读时或发布时解析范围内最新 | 取决于实现 | 可能是 | 中：范围可漂移 |

**结论：**

- **B = 读时最新**，公共列表/详情/A34/安装每次都用 Skill 最新版；Expert 版本号不变，但内容会变。它解决“Skill 更新自动传到 Expert”，代价是审核漂移、安装不可复现、A34 同请求不同结果。
- **D = 自动生成 Expert 新版本**：Skill 发新版时，系统找到引用它的 Expert，自动创建一份 pin 最新版的新草稿并通知 owner；owner 审核/发布后公共市场才变。它保留固定 pin 和审核，只是把“手动改 pin”自动化。
- **C = 发布时取最新**：只在 owner 发 Expert 新版本时自动选最新，之后仍固定；不会因为 Skill 更新自动变。
- **A = 当前方案**：完全手动。

**建议：** 如果你只是不想每次手动改 pin，选 **D**；如果你要“公共市场永远零维护跟随最新”，选 **B**，但要接受审核/复现性代价。**不要做成“公共列表显示最新、安装仍用旧 pin”的混合体**，那会让展示内容和实际安装不一致。

## 9. B 模型（用户已选）的范围与影响

**用户已选 B：公共市场/读取/安装每次解析最新版。** 这是引用模型变更，不是改 D4 字段。

### 9.1 已确认的范围

1. **所有依赖边都跟随最新（Connector 除外，按 ID 读 effective）：**
   - **Expert→Skill**：解析目标 Skill 当前正式发布版。
   - **Team→Expert**：解析成员 Expert 当前正式发布版；该 Expert 自己的 Skill 也解析最新。
   - **Expert→Connector**：按 ID 读 effective（D7），Connector 无版本。
   - Team 展示的成员与公开市场看到的 Expert 一致；Team 的 `member_key/role/is_leader/sort_order` 仍由 Team 边决定，不因 Expert 发新版改变。
   - 这意味着 Team 不需要发新版本就能跟随成员 Expert 更新；代价见 9.3。
2. **“最新”的定义（已确认）：** 目标资源的当前正式发布版（`plugins.published_version_id`），且 `status=1`、未删除、`listing_state=published`；目标没有正式发布版或最新版下架/删除时，依赖不可用，**不回退旧版**。
3. **审核窗口（自动/人工，沿用现有策略）：**
   - **默认自动审核：** 现有 `plugin_review_policies` 在没有 Space 单独配置时返回 `IsAutoApproveEnabled=true`（`internal/repository/plugin/review_policy.go:20`）；Space owner/admin 可以关闭。B 不新增审核策略。
   - **自动审核路径：** submit 后立即解析最新并批准；如果解析/批准之间有并发依赖变化，重新解析并重试一次；正常不把 409 暴露给用户。
   - **人工审核路径（Space 显式关闭自动审核）：** 提交时记录解析版本；批准时重新解析，变化 409 `dependency_changed`；由有权取消者取消旧申请，再由有权提交者重新提交并生成新的 review_id；审核人查看新申请后批准。
   - **批准后依赖继续跟随最新**；Skill/Expert 发新版**不需要**重新审核引用它的 Expert/Team。只有 Expert/Team 自身内容/关系变化才需要新版本/审核。

### 9.2 B 下 D4 的语义怎么变

D4 的 `dependency_availability` 仍然需要，但含义调整为：

- `evaluated_view/evaluated_version_id`：指 **Expert/Team 自己的** published/editing 版本（不是依赖的 pin 版本）。
- `reasons[].target.version_id`：指本次解析到的**最新目标版本**（Team 的 `via` 里成员 Expert 也是最新解析版本）。
- 旧场景“E v1 pin S v1，S v1 下架”在 B 下变成：E v1 解析 S 的最新版（如 S v2），S v1 下架不影响 E；只有**最新版**下架/删除才影响。
- 仍然需要 D4 的原因：Expert/Team 的**依赖集合**（引用哪些 Skill/Expert/ID）可以在草稿里变。
  例：E v1 引用 S，S 最新版被删；E v2 草稿移除 S 已修好但未发布 → 公共 E v1 仍隐藏，owner 的 mine 行展示草稿时不能误报“正常”。`evaluated_view=published/evaluated_version_id=E-v1` 仍然必要。
- Team 的 via 路径：成员 Expert 的最新解析版本 → 该 Expert 的最新 Skill/Connector；如果成员 Expert 最新版不可用，Team 直接不可用。

### 9.3 代码/方案影响

- **数据模型：** Expert→Skill 和 Team→Expert 的 `target_version_id` 都不再是解析依据（可保留为上次解析/审计值）；`plugin_version_relation_refs` 的 check 约束、`content_digest`（应排除浮动版本）要改。
- **A01：** 依赖可用性 SQL 按目标“当前正式发布版 + listing”判定；Team→Expert 解析最新 Expert，再解析该 Expert 的最新 Skill/Connector；不回退 pin。
- **A34/安装：** 每次解析最新；同一个 Expert/Team 版本不同时间安装可能不同内容；`bundle_digest` 动态；A34 仍 `no-store`。同 key 重试仍用首次冻结 plan（D9），新 key 新安装才用最新。
- **Team 语义：** Team 的成员集合、角色、Leader、顺序仍由 Team 边决定；成员 Expert 的**内容/依赖**跟随最新。成员 Expert 发新版不会改 Team 的角色，但会改 Team 展示的 Expert 内容和它引用的 Skill。
- **审核：** 提交/批准记录所有解析版本（Expert、Skill、Connector effective）；批准窗口变化 409；批准后浮动。Team 审核还要覆盖成员的解析版本。
- **迁移：** 旧 pin 作为历史/审计保留，不再作为公共/安装解析依据；迁移计划按 B 生成引用式资源。
- **可复现性：** 不再保证“同一个 Team 版本永远装同一组成员 Expert/Skill 内容”；这是 B 的明确代价，需要写进 API/客户端说明。
- **安全：** Skill/Expert 最新版可能影响所有引用它的 Expert/Team；用户已确认本期仅审计展示：记录变更并展示当前解析版本，不因依赖更新主动发送通知。
- **一致性：** 一次 A01/A34/安装请求要在同一个数据库一致性快照内解析完整最新图，保证单次响应或首次安装计划内部不混版；不同时间的列表查询与新安装可解析到不同版本。

### 9.4 和已拍板决策的关系

- **D6：** 下架/删除的判定对象变成“最新解析版本”；Team 的成员 Expert 最新版或其最新 Skill 不可用 → Team 隐藏/安装失败；不回退旧版。
- **D7：** Connector 仍按 ID + effective（最新有效，包含 Admin 直接生效写入），与 B 一致；Expert/Team 审核仍观察 Connector effective 变化。
- **D9：** 同 key 重试仍重放首次 plan；配置/依赖变了的新安装用新 key 才解析最新。
- **D4：** 结构保留，语义如上；不能因为 B 就删掉 `evaluated_view`。
- **D1/D5/D8/D10：** 不受 B 直接影响；但 D5 迁移生成的引用式资源要按 B 存 ID（不存权威 pin），D8 的 relations 校验要允许浮动版本。

## 10. 决策状态（已全部确认）

**已确认：**

1. **D1 删除：** 保留原删除能力；保护集合 = live incoming（含草稿）∪ published refs ∪ Team→Expert→Skill 间接引用；没有保护引用的曾发布资源也可删。只改删除，不改“已发布资源收窄 visibility”的现有行为。
2. **D2 A09：** 不做 restore-to-draft；从 MP-01/API/plan/CL-02 删除 A09。
3. **D3 共存：** 不允许同名/多版本共存；同名冲突失败；普通安装保留市场原名/原字节；不改 Fleet/Daemon。
4. **D4 A01：** `dependency_availability` 四字段；`evaluated_view/evaluated_version_id` 指 Expert/Team 自己的 published/editing；`target.version_id` 用本次解析到的最新版本；无权依赖匿名。
5. **D5 迁移：** 本期用 CLI + 离线授权做生产迁移，不做 A29/A30 HTTP；CLI 两次 preview/冻结摘要；回滚必须数据安全（不恢复 current/published 指针）。
6. **D6 private/下架：** 同 owner private→private 允许；space/system 不得引用 private；下架/删除的最新依赖导致 Expert/Team 公共隐藏且 A34/安装失败；owner 只能看诊断，不能安装坏图。
7. **D7 Connector：** Admin 不审核且直接生效，Tenant 沿用原审核策略；采用 effective 模型；审核观察 effective_revision/digest；变化 409 `dependency_changed`；安装/A34 读 effective。
8. **D8 CAS：** `edit_revision` + `expected_edit_revision`；更新必带、新建省略；缺失/过期 409；relations 缺失 400；Admin/Web/CLI/Bot 都改。
9. **D9 安装：** 保留 A32 URL；A31/A32 同一引擎；同 key 重放原 plan；配置变了用新 key 新安装；仅首次成功计数。
10. **D10 退役/回滚：** 新改造完成后旧市场执行器不可达；回滚接受兼容二进制 + 新版本恢复；不恢复指针/legacy。
11. **B 引用模型：** Expert→Skill、Team→Expert 都解析目标当前正式发布版；Connector 按 ID+effective；Team 的 `member_key/role/is_leader/sort_order` 仍由 Team 边决定；最新版不可用时不回退旧版。
12. **审核：** 沿用现有 Space 策略（默认自动，Space 可关）；自动模式内部重试，不把 409 变成日常步骤；人工模式窗口变化才 409；批准后依赖继续跟随最新，Skill/Expert 发新版不要求重新审核引用方。

**核心决策已确认，无剩余待拍板项。** 两个实现边界均已由用户明确确认，已写入 00/01/02：① Admin 系统 Connector 的既有直接生效写入保留（同步更新 effective；Tenant Connector 编辑必须审核）；② 用户已确认依赖更新仅审计展示，不主动发送依赖更新通知。若要把 Admin Connector 也纳入审核或新增主动通知，属于后续变更。

**文档落地状态（2026-10-26 15:15）：** `00-decisions.md`、`01-technical-design.md`、`02-api-contracts.md`、`README.md`、`issues/README.md`、8 个 issue、`plan.json` 已按 B/D1–D10 重写；`03-github-issues-114-123-diff.md` 已加历史说明。代码尚未修改，下一步按 00/02 拆任务。

## 本轮五组评审意见的落地记录

用户授权按五组意见修订方案及任务。人工审核采用取消旧申请后重新提交的新轮次；版本化来源可复用同一不可变版本，Connector 重新确认当前编辑修订。原 key 重试不因上游正常发布而改变或失败；不同请求可解析不同最新版。删除保护不采用全部历史版本超集。Admin 不用审核、通知仅审计展示均为用户明确确认。

迁移签名授权的字段、信任来源、有效期、撤销及续签恢复由工程方案补齐，唯一格式见 02 §5.1；它授权生产迁移计划执行，不新增 Admin 发布审核，也不表示本次实际执行已获批准。PR 基线统一引用 review 核验记录中的 2026-10-26 快照（含 #128）；文档修订完成与代码验收分开记录。

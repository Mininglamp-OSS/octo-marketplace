# CL-03：Buddy 单 A34 最新内容、本地安装与安全重试（B 模型）

> 决策依据：[00-decisions](../00-decisions.md) B 模型、D3（原名/原字节、无共存、不改写）、D6（坏图失败）、D7（Connector effective）、D9（同 key 原 plan、新 key 最新）。Buddy 只做客户端接入，不改 Fleet/Daemon，不新增市场管理入口。

## 目标（Goal）

- 保留 Buddy 现有市场列表/详情、技能/Connector 本地安装、专家本地准备和工作空间招募入口；统一消费最新发布图，工作空间安装仅在原完整参数可重建且未超出现有窗口时安全重试。
- **硬依赖：MP-03**（继承 MP-02/MP-01）。可并行协作：CL-01/CL-02 独立客户端，MP-04 提供历史资源可分发状态；不把旧包自动迁移塞入 Buddy。
- 唯一 wire 来源：[02 API 契约](../02-api-contracts.md) 的 A01/A02/A03 及 A05～A08（既有独立摘要/历史工具，A03 仅轻量图）、A34（最新完整内容读取，非 Fleet 查询）、A31（工作空间安装）与 §2/§6 DTO。URL 保留网关前缀 `/market/api/v1`。
- 边界已确认：本 issue 的市场招募只消费市场源定义；纯本地组合完全不调用 Marketplace。已有工作空间 Expert 追加本地 Skill 应直接推送内容到 Fleet 并绑定 Fleet `skill_id`，不先创建 private 市场资产，也不走 A34 获取用户本地文件。该运行态编辑不是市场安装失败后的 fallback。

## 背景（Background）

B 模型下 Expert→Skill、Team→Expert 每次读取解析最新当前发布版，Team 成员角色/Leader/顺序仍由 Team 边决定；Connector 读 effective。因此 Buddy 不能缓存“固定依赖图”，也不能再用“detail + download ZIP + 补 frontmatter”拼内容：一次 A34 返回本次最新完整图和原字节，下一次请求可能因依赖发新版而不同，`bundle_digest` 会变化。

D9 下 Buddy 的招募安装与 Web/CLI 共用同一 `InstallCoordinator`：同 key 只重放首次冻结 plan；显式安装输入变化 409；上游正常发新版仍重放原 plan，想取最新版则由用户以新 key 新安装；不得自动换 key、删除 pending、跨 URL 或退回本地安装。

D3 下 Buddy 不实现同名/多版本共存：不改写 frontmatter、不生成 `mk1`、不按名字猜版本；同名不同内容由安装阶段明确冲突失败。

## 承重行为（Load-bearing）

1. **公共列表。** 继续消费 A01 缺省公共模式；直接或间接依赖不可用的 Expert/Team 由服务端在 SQL COUNT/分页前按最新解析过滤，分类 `plugin_count` 与公共 tags 同口径；Buddy 不先拉全部再丢卡、不逐卡请求依赖或 A34 判断展示，不消费公共响应中本应省略的 owner 诊断字段。
2. **A34 唯一完整内容。** 完整 Expert/Team 内容仅一次 `GET /market/api/v1/plugins/{plugin_id}/distribution`，只传 `plugin_id` + 既有认证/Space 上下文；服务端解析最新完整图。消费 `schema_version/root/graph/assets/bundle_digest`，节点身份为资源+本次解析版本，Connector 为 ID+effective revision；每 `node_key` 恰一 asset。A03 仅用于独立轻量图展示，不作为完整内容入口。
3. **原字节。** 直接使用 asset 的文件原始 UTF-8 字节与 hash，不调用 `normalizeMarketplaceSkillMarkdown` 补 frontmatter/BOM 剥离，不下载 ZIP 补文件，不改目录名；`bundle_digest` 按 02 的 JCS/排序规则校验，不用 JS 默认 `JSON.stringify` 替代。
4. **失败关闭。** 缺节点/文件/权限/摘要/预算、MCP 冲突均整体失败；不把缺失解释为“无依赖”、不降级为旧附件/空配置/缓存内容、不用历史文件接口补齐。A34 查询本身不调用 Fleet、不创建安装记录/资源、不改发布状态。
5. **成员语义。** Team 成员角色/Leader/sort_order 只取 edge data；成员内容取本次 A34 最新解析 asset；成员 map/Skill map 保留 `node_key` 与版本/effective 身份，不按名字或单 `plugin_id` 合并。成员 Expert 发新版只更新内容，不改变 Team 角色。
6. **工作空间招募（D9）。** 继续 `POST /market/api/v1/plugins/{plugin_id}/installations`，头为 Space 上下文、`X-Workspace-ID`、`Idempotency-Key`，body 为 `runtime_id/resource_name?/custom_env?`；仍解析 `{agent_id}`/`{squad_id}`。不添加客户端 bindings、不调用旧 `/plugins/install` 补救、不尝试另一 mutation URL。
7. **同 key / 新 key。** 首次可能 POST 前持久化 key、用户/服务源/Space/project/plugin/workspace/runtime、名称/类型及时间等无 Secret 元数据；同意图并发共享一次请求和同一 key；同 key 重试只重放首次 plan/完整规范化请求，不重新解析最新。显式安装输入变了 409；上游正常发新版仍重放原 plan，想取最新版则提示用新 key 重新安装；未知旧结果先人工核对，不覆盖 pending。
8. **UNKNOWN 与截止。** 网络故障只允许在现有窗口内以原 key、原 plan、同一完整规范化请求和重新提供的相同 Secret 重发；不能从本地 pending 恢复 Secret。重启/重开/重试无法重建原参数时不得自动重发；超窗由 Marketplace 客户端阻止 POST。无本地记录、后续 4xx、当前资源名称均不能证明远端未提交。
9. **诚实状态。** 只有本次首次请求且服务端确认无提交的终态拒绝才允许结束对应 pending；已有 UNKNOWN 时后续 4xx 也不清除。500、超时、断连、非 JSON、缺结果 ID 全部不宣告成功；本地清理失败只提示核对。
10. **无共存改造。** 不实现 `mk1`、不重写 frontmatter、不改目录名、不按名称覆盖/新建共存规则；多版本运行时绑定与命名取舍不在本期范围。

## 非目标（Out of scope）

编辑器、Daemon 工作空间绑定系统、版本选择器、Secret 系统、运行中自动同步；A09 restore、A29/A30 HTTP、同名/多版本共存、frontmatter 改写、Fleet/Daemon 改造；把本地安装误删成“旧 HTTP fallback”，或工作空间安装失败后改走本地安装。

## 源码与所有权

- 仓库 `octo-buddy-client`：`electron/main/marketplace-client.ts`、`marketplace-handlers.ts`、`settings-marketplace-ipc.ts`；`marketplace-expert-recruit-service.ts`、`marketplace-expert-recruit.ts`、`marketplace-skill-{install,files,metadata}.ts`、`marketplace-mcp-install.ts`。
- 工作空间招募：`electron/main/{project-capability-recruit,project-remote-ipc,project-capability-identity-store,capability-install-response,auth-session}.ts`、`db/local-data-store` 现有 settings 接口；市场 bridge 为 `shared/{marketplace-contract,expert-marketplace-contract,capability-install-error}.ts` 及 `electron/preload/index.ts`。renderer 入口包括 `src/renderer/src/features/marketplace/useMarketplaceCatalog.ts`、`features/marketplace/CatalogFeatured.tsx`、`features/projects/api/capabilityApi.ts`、`features/projects/{useCapabilityRecruitment,capability-recruit-feedback,capability-identity}.ts`，不新增平行市场入口。
- 本 issue 拥有主进程 adapter、最小无凭据 pending 记录、IPC 类型与现有 UI 状态；不新增服务端表。Marketplace attempt/计划/状态机由 MP-03 所有；Fleet 只消费现有 F01，不改源码/API/数据库/cleanup/保留期限，不让 renderer 直接访问 Fleet。
- `electron/main/marketplace-client.ts::fetchPluginMarketplaceList` 继续消费 A01 缺省公共模式；公共可用性 predicate、COUNT/分页/分类/tags 口径归 MP-02，A01 owner 与 list/DTO 底座归 MP-01，Web mine 标记/tips 归 CL-02。本 issue 不新建 mine 管理入口或前端依赖检查器，原 8 issue 硬依赖不变。
- **已核源码现状：**`marketplace-skill-install.ts` 先 detail 再 download ZIP 并调用 `normalizeMarketplaceSkillMarkdown` 补 frontmatter；`marketplace-mcp-install.ts` 依赖 `detail.quick_start`；`marketplace-expert-recruit.ts` 只以 `plugin_id` 建成员 map 并从 raw 附件规范化，`marketplaceExpertIsCurrent` 只比较旧展示 version；这些调用/身份/内容处理都必须适配 A34，不能仅删除 BFS 后继续使用旧形状。
- `project-capability-recruit.ts` 已有窗口内 pending 和 single-flight，但 workspace 只校验 headerSafe，记录只有 key/target/name/type 且成功后直接删除，没有已知成功持久化；`capability-install-response.ts` 把多数有效 JSON 的 4xx/409 认作 definitive，`project-remote-ipc.ts` 只回 errorKind，renderer `capabilityApi.ts` 会丢失 status/definitive 及安全 retry details。本期在这些已有边界补齐。

## 实施步骤

1. 市场列表继续使用 A01 `GET /market/api/v1/plugins` 缺省公共模式：直接或间接依赖不可用的 Expert/Team 由服务端在 SQL COUNT/分页前按最新解析过滤，分类 `plugin_count` 与公共 tags 同口径；Buddy 不先拉全部再丢卡、不逐卡请求依赖或 A34 判断展示。市场列表/普通详情默认 published，名称、描述和成员数来自同一已发布根，依赖内容按本次最新解析。
2. 删除 `marketplace-client.ts` 的依赖 ID 扇出和 `marketplace-expert-recruit-service.ts` 的 ID-only BFS 内容补齐；不得先 A03 再按依赖请求 detail/versions/skill_md/download/files。直接消费 A34 的 `schema_version/root/graph/assets/bundle_digest`，节点/缓存/归一化身份为资源+本次解析版本，Connector 为 ID+effective revision，每 `node_key` 恰好一项 asset；A03 仅用于独立轻量图展示。按契约预算处理完整图，不截断或丢边。
3. 删除生产路径中“依赖请求失败 → null → filter 丢边”“MCP 获取失败 → 旧附件/空对象/缓存配置”的分支。404、403、其他 4xx、500、网络错误及无效 JSON 均阻断本次准备/安装；按固定 reason 分别提示修复输入/权限、稍后重试读取或结果未知，不能把所有 4xx 描述为可直接重试安装。不能把缺失解释为无依赖。测试 fixture 不进入生产 fallback。
4. 正式接入 A34 的完整图与全部 UTF-8 原始文件：Expert/Team 自身 `AGENTS.md`、Skill 的 `SKILL.md` 及全部支持文件和有效 Connector 公开声明/占位符 `mcp_config` 一次返回，不能仅消费链接/storage key。只传 `plugin_id` 及既有认证/Space 上下文，不传 workspace/runtime/custom_env/幂等 key；不新增历史根选择器。A34 不提供 `installation_name`、不重写 frontmatter 或生成安装副本。
5. 沿用现有权限、上下架与历史受众检查；缺依赖/文件/权限/摘要错误整体失败，`budget_exceeded=413`、文件完整性=500，`no-store`，不分页/裁剪/补 latest。Buddy 校验 bundle 及全部文件路径/UTF-8/hash 与预算、graph/assets 一一映射后才产生本地副作用；任何错误不得请求历史文件接口或缓存补齐，不跨意图复用 bundle。保留 staging/原子替换与仅针对本次创建物的回滚，不删除共享文件；本地加密凭据不进入市场恢复记录。
   - 主进程统一 A34 transport/校验，复用既有认证与服务源，不做 base path 探测。市场 `detail` 现有 action 需要完整 Expert/Team 内容时内部切为 A34；独立摘要/历史工具可保留 A02～A08 但不能给 A34 补内容。`marketplace-handlers`、本地专家 prepare、Skill install 和 MCP install 的依赖注入一起改为已校验 bundle，不把 ContentAsset 强制 cast 成旧 `{plugin,relations}` 或 `quick_start`。版本标签取 graph 节点 `version_label`、描述取其 Manifest，内容取对应 `asset.files`，成员 wiring 只取 edge.data，有效 `mcp_config` 只取 Connector asset；成员 map/Skill map/本地来源元数据均保留 `node_key` 及版本/effective 身份，不按名字或单 `plugin_id` 合并。
   - 删除市场 A34 入口对 `normalizeMarketplaceSkillMarkdown` 的补 frontmatter/BOM 剥离和 `downloadMarketplaceSkillArchive` 补 ZIP 调用；按文件原始 UTF-8 写入 staging 并比对原字节 hash，不能校验后再改写源字节。纯本地 `importLocalSkill`、既有本地凭据存储及独立历史 ZIP 工具不属于 HTTP fallback，不因此误删。
   - `normalizeMarketplaceExpertPackage` 及 `prepareMarketplaceExpertPackage/marketplaceExpertIsCurrent` 不再用 `current_version` 展示标签判定内容相同；明确消费 `selected_version_id` 及 `bundle_digest`，对同一根版本的本次最新依赖/有效 Connector 配置变化也能区分。只在显式准备/安装意图中应用读取到的内容，不新增运行中自动同步；已有本地 package 写入/staging 只在完整 bundle 校验后进入，角色/Leader/sort_order 不可丢失。不在此 adapter 中暗自按名字覆盖/新建共存规则。
6. 工作空间招募继续 `POST /market/api/v1/plugins/{plugin_id}/installations`，头为 Space 上下文、`X-Workspace-ID`、`Idempotency-Key`，body 为 `runtime_id/resource_name?/custom_env?`；仍解析 `{agent_id}`/`{squad_id}`。不添加客户端 bindings；不调用旧 `/plugins/install` 作补救，不尝试另一 mutation URL。
7. 扩展 `project-capability-recruit.ts` 的既有 pending 记录：首次可能 POST 前持久化 key、用户/服务源/Space/project/plugin/workspace/runtime、名称/类型及时间等无 Secret 元数据，仅用于防重复与人工核对；同意图并发共享一次请求和同一 key，不承诺仅凭 key 找回远端结果。参数改变必须显式区分新意图；存在未知旧结果时先人工核对，不能覆盖 pending。Secret/custom_env 全文、Definition、MCP 和 Token 不落 settings、Marketplace、日志或错误消息。
8. 网络故障后，仅在现有窗口内以原 key、原 plan 和同一完整规范化请求重发原 POST；所需运行时 Secret 由用户重新提供且必须相同，不从本地 pending 恢复 Secret。重启、再次打开招募或用户重试时若无法重建原参数，不得自动重发；超窗由 Marketplace 客户端阻止 POST，无需修改 Fleet。pending 不是长期远端恢复能力，也不要求 Fleet 延长记录保留或改变过期重占。
9. 已有本地 attempt 记录中的已知成功结果可直接回放原资源 ID；未知结果、超窗、参数不同或不可重建均保留未知并人工核对。无本地记录、后续 4xx、当前资源名称均不能证明远端未提交；不查远端 receipt，不自动换 key、不删记录、不切入口或 fallback 安装。**依赖目标发新版不改变原 key 的 plan；配置/期望依赖变化时提示新 key 新安装，由新 key 重新解析最新**；冻结节点被删除/下架返回 409 `dependency_changed`，不静默换版本。
10. 保留 `CapabilityInstallError` 的确定拒绝/未知结果区分。只有本次首次请求且服务端确认无提交的终态拒绝才允许结束对应 pending；此前已有未知结果时，即使后续返回 4xx，也不能据此清除旧记录。500、超时、断连、非 JSON、缺结果 ID 全部不宣告成功；本地清理 pending 失败只提示核对，不自动重新招募。
    - `validTarget` 统一 workspace/runtime UUID 校验，错误 reason 由 `capability-install-response.ts` 解读 02 安全 details，明确区分 `installation_in_progress`、`installation_result_unknown`、`installation_attempt_expired` 及 `idempotency_input_mismatch`；未知/进行中/过期/参数冲突不得仅因 HTTP 409 就标 `definitiveRejection` 并清 pending。只有既定错误语义明确证明首次未 dispatch 且没有先前 UNKNOWN 时才可清理。
    - pending 保留原始 `startedAt` 及服务端 `retry_until` 的更早安全截止，不因再次打开或重试延时；无 Secret 的已知成功记录与未决记录分开，验证 `agent_id/squad_id` 后先保存已知结果，再结束 pending，避免保存/清理失败或用户重开导致新 key。既有 v1 记录没有成功/UNKNOWN 证据，不能升级成“从未发送”，也不能因结构迁移失败删除；保留人工核对。
11. IPC/renderer 沿用现有市场和招募表面，补充“进行中/结果未知/本地已知成功/已过安全重试期”状态与已知原资源导航；账号/Space/project 切换不得把旧结果绑定到新上下文。`project-remote-ipc`→preload→`capabilityApi`→`useCapabilityRecruitment/capability-recruit-feedback` 全链传递仅安全的 `reason/attempt_id/retry_until/can_retry_same_key` 及本地分类，不只保留一段 message；不要让 renderer 把 unknown 包装成普通可重装失败。已知结果沿 `project-capability-identity-store` 记原来源，保存失败不重新 POST。按 remote-api-ipc-design 维持只读 marketplace:remoteRequest allowlist 与本地副作用语义化 IPC 的分离。更新既有使用说明及相邻测试，不添加编辑/绑定 UI。
    - `capability-recruit-feedback::recruitmentNeedsVerification/recruitmentSummary` 当前依赖目录同名匹配决定“待核查/未成功”，必须改为以上真实结果分类：所有已 dispatch 但未知的尝试，无论目录是否有匹配名称均保持待核查；目录资源名/来源匹配仅作为人工导航线索，不是提交证明或“未执行”依据。

## 逐项验收（Acceptance）

- [ ] 市场名称、详情、Skill 正文/完整文件与本次最新发布根一致；新草稿和 Connector 未批准编辑不泄露；依赖目标发新版后下一次读取/A34 展示最新内容，不缓存旧依赖。
- [ ] Skill/Connector 直接异常或成员 Expert 下架、删除、缺失时，A01 不返回受影响 Expert/Team；Team 沿最新成员 Expert 判定，成员最新版或其依赖不可用时 Team 不展示。列表、分页 total、分类数量及 tags 与 Web 公共市场同口径，Buddy 无逐依赖请求、A34 逐卡检查或分页后过滤。
- [ ] 来源未发布草稿新增异常依赖不影响合法发布版；仅草稿移除坏依赖不恢复线上，发布新修复版本或目标最新版恢复合法上架后，沿用已有列表刷新在下一次查询恢复，不新增推送/轮询。此过程不改来源 `listing_state` 或增加生命周期 UI。
- [ ] **B 跟随：** 成员 Expert/Skill 发新版后 Team 下一次 A34 返回新内容、`bundle_digest` 变化；Team 成员角色/Leader/顺序不变；不要求 Team 发新版。
- [ ] 同一 Skill 被两个成员引用时解析到同一最新版本并映射到同一 `node_key`；Team 成员角色/Leader/顺序完整，Connector 读取有效态。不生成 `mk1`、不改写 frontmatter、不按名字合并；共存不在本期验收。
- [ ] 依赖 404/403/其他 4xx/500/网络/JSON 错误、缺文件、二进制、重复 MCP server key 与超预算均失败关闭；准备失败无远端 mutation、无新增本地安装，不空配置降级、不回退旧依赖。
- [ ] 抓取一次 Expert/Team 完整内容准备的市场请求：仅一个 A34 GET 即取得准确最新目标、全部 `AGENTS.md`/`SKILL.md`/支持文件及有效 Connector，未发送依赖 detail/versions/skill_md/download/files 请求，未先 A03；原市场名称、frontmatter、正文及文件 hash 未变。
- [ ] A34 错误均阻止本地副作用，无客户端补取/缓存/latest fallback；既有权限/上下架检查不变，无下架豁免。A03 独立图展示不触发完整内容扇出。
- [ ] 工作空间双击/并发与窗口内断网重试复用原 key、原 plan、原完整规范化请求和重新提供的相同 Secret；原键改参数冲突，不跨 URL、本地链路或 legacy fallback。
- [ ] **D9：** 同 key 不因依赖发新版而重解析；配置/期望依赖变化时 UI 提示新 key 新安装，新 key 解析最新；冻结节点删除/下架 409 `dependency_changed`。
- [ ] Fleet 成功但响应丢失后，窗口内原 key/原请求重试返回原 agent/squad，不重增计数；重启后无法重建参数不自动重发，本地已知成功可直接回放。
- [ ] 未知不标成功；超出现有窗口由 Marketplace 客户端阻止 POST，保留未知供人工核对；不查远端结果、不要求过期 key 永久保留或改变 Fleet 过期重占，无自动新 key。
- [ ] 有过未知结果的尝试后续遇到 4xx 仍保留证据；响应缺 ID、本地 pending 清理失败均不会触发重复创建；上下文切换不串记录。
- [ ] settings、IPC 回包、日志、错误及 smoke 证据均无 Secret/Token/custom_env/MCP 原文，现有本地加密凭据存储不因恢复读取而解密。
- [ ] 所有市场完整内容调用链（详情、本地 Expert/Team 准备、Skill 安装、Connector 安装）使用已校验 A34 DTO，不 cast 旧形状、不补 frontmatter/BOM、不再下载 ZIP 补文件；明确区分本地导入和市场读取，现有纯本地路径不误删。
- [ ] workspace/runtime 均 UUID；进行中/未知/到期/参数冲突从 HTTP reason 经 IPC 至 renderer 保留语义，409 不自动清 pending。成功持久化先于结束 pending；旧 v1/损坏记录不被当未发送，不因 close/reopen 或结果记录失败换 key。

## 测试与可观测 smoke（Verification）

- 实施时在现有模块相邻测试覆盖最新图/同资源多成员引用、失败关闭、招募 single-flight、pending 重启/损坏/到期、错误分类和原请求安全重试；本设计阶段不执行检查。
- 实施时补 A01 公共列表请求证据：直接/间接异常来源不在结果，依赖按最新解析判定，COUNT/分页/分类/tags 与服务端一致；列表请求不触发 A34 或依赖扇出。异常来源的 mine 安全诊断由 MP-02/CL-02/REL-01 验收，本 issue 不重复查询原因。
- 单独抓取 A34 成功/缺文件/错 hash/无权/预算失败请求与本地写入、Fleet 调用记录：成功一次 GET 拿全且无 Fleet，失败无残缺内容或补取。再发布成员 Expert/Skill 新版，验证下一次 A34 跟随最新且 `bundle_digest` 变化；不运行同名共存或 frontmatter 改写用例。
- 对一次成功 POST 丢弃响应，在现有窗口内使用原 key、原 plan 和重新提供的相同 Secret 重发完整原请求，观察相同资源 ID 且 Fleet 仅一次 mutation；依赖目标发布新版不改变原 key 的 plan。另一组变更配置/期望依赖，确认同 key 409、新 key 新安装。重启后参数不可重建、4xx/500/超窗均记录脱敏方法、key 标识和未知状态，确认没有替代执行链或远端结果查询。

## 部署前置

- MP-03 的 A34 端点/完整内容组装、持久化计划、统一 Capability 引擎与原请求安全重试须实现后部署，MP-02 提供最新解析/MP-01 提供版本文件底座。Fleet 现有 F01 仅真实联调，无 Fleet 代码 PR 或改造；Buddy 仅在对应服务端就绪后发布，不可用明确停止，不启用旧格式或无幂等降级。不部署 A09/A29/A30。契约已确认，当前不是实现/运行已通过。

## 本轮评审修订验收

- [ ] 首次 v1 安装网络超时后目标发布 v2：同 key 原计划继续，新 key 才解析 v2；不把上游正常更新显示为必须重装的冲突。冻结节点失效仍按契约阻断并保留未知结果。
- [ ] 展示本次解析版本，正常刷新可显示后续新版；无列表与后续新安装跨请求同版本承诺，无依赖更新主动通知。

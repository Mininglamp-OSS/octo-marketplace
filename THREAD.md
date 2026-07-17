# DEV-29 Metrics Implementation — THREAD.md

## Overview

通用 metrics 基础能力 + Skill 市场计数方案 v1 实现跟踪。

## Stage 1: 基础框架 ✅

### DEV-30: DB migration + metrics 框架基础 ✅
- `resource_metrics` 表 migration
- Redis key 封装 (`internal/redis/client.go`)
- `POST /api/v1/metrics/track` 接口 (v1: event_type=view, resource_type=skill)
- Skill Resolver (基于 skill service 可见性判断)
- 单元测试覆盖

### DEV-32: Flush Worker ✅
- 定时 30s 从 Redis dirty set flush 到 DB
- 分布式锁 `SET metrics:flush:lock <instance_id> NX EX 120`
- SPOP + GetSet 原子取增量 → UPSERT resource_metrics
- DB 失败重试 3 次，最终失败 SADD 回 dirty set (best-effort)
- 优雅关闭: context cancel → 独立 context 释放锁
- 结构化日志: flush_runs, resources_processed, db_failures, duration, dirty_set_size

## Stage 2: 业务接入 ✅

### DEV-33: 后端 Skill 业务接入 ✅
- 下载接口成功后 fire-and-forget `TrackDownload("skill", id)`
- `GET /api/v1/skills` LEFT JOIN resource_metrics 返回 view_count/download_count
- Sort 参数: comprehensive (默认), latest, downloads, views
- 综合排序公式: `downloads*5 + views*1 + 20/POW(hours/24+2, 1.2)`
- Offset 分页 (comprehensive/downloads/views), cursor 分页 (latest)

### DEV-34: 前端 Skill UI 接入 ✅
- Skill 卡片展示 view_count / download_count
- 详情页渲染成功后 fire-and-forget `POST /api/v1/metrics/track`
- 排序下拉: 综合 / 最新 / 下载量 / 浏览量
- track 失败不影响页面，不弹 toast

## Stage 3: 集成联调 + 监控告警 + 冒烟验证 ✅

### DEV-35: 集成联调 ✅

#### 链路验证 (Integration Tests)
- ✅ View 链路: TrackView → Redis INCR+SADD → Flush → DB UpsertCounts
- ✅ Download 链路: TrackDownload → Redis INCR+SADD → Flush → DB UpsertCounts
- ✅ 混合 view+download 同一 skill 正确累加
- ✅ 多 resource 一轮 flush 全部处理
- ✅ 并发 100 goroutine track 正确累加

#### Redis 故障验证
- ✅ Redis 不可用时 TrackView 返回 nil (不阻断主流程)
- ✅ Redis 不可用时 TrackDownload 返回 nil
- ✅ Redis 不可用时 Flush Worker 跳过 (lock acquire 失败)

#### 多 Worker 锁验证
- ✅ 两个 worker 并发只有一个获得锁并 flush
- ✅ releaseLock 检查 value，不误删别人的锁

#### 综合排序验证
- ✅ brand new skill 有 recent_bonus (score > 0)
- ✅ downloads 权重 5x (10 downloads > 49 views)
- ✅ 新 skill (有一些互动) > 90 天 stale skill
- ✅ popular old skill 仍因总量大而排名高
- ✅ 相同互动量下新 skill > 旧 skill (time decay)

#### DB 失败处理
- ✅ DB 失败 3 次后 SADD 回 dirty set (best-effort 不丢失)
- ✅ DB 失败 2 次后第 3 次成功 → 正常写入
- ✅ zero delta 跳过 UPSERT (不浪费 DB 写)
- ✅ 非 skill type (如 mcp) 被 v1 跳过

#### Worker 生命周期
- ✅ context cancel → 优雅关闭 (< 2s)

### 监控指标 / 日志

项目使用 stdlib `log.Printf` 结构化日志，每轮 flush 输出:
- `result=success|partial_failure`
- `resources_processed=N`
- `db_failures=N`
- `duration=Xs`
- `dirty_set_size=N` (flush 开始时)

告警规则 (运维配置):
- `flush_db_fail` 5 分钟 > 10 次: 告警 (DB 可能不可用)
- `dirty set size` 连续 3 周期增长: 告警 (flush 跟不上写入)

## 已知限制 (v1)

- v1 不防刷 (无 UV 去重)
- v1 不做 UV 统计
- flush 周期内数据有 30-60s 延迟
- `download_count` 是下载 URL 生成次数 / 下载意图次数，不是 CDN 真实文件下载次数
- v1 不接入 MCP (后续加 resolver 即可)
- SPOP + GETSET 是 best-effort: DB 持续失败或进程崩溃可能丢当前批次

## 分支信息

- 后端 metrics 框架: `feat/DEV-29-metrics-foundation`
- 后端 Skill 业务接入: `feat/DEV-33-skill-metrics-integration`
- 后端集成联调: `feat/DEV-35-integration-monitoring`
- 前端 UI: `feat/DEV-29-skill-market-metrics-ui` (octo-web)

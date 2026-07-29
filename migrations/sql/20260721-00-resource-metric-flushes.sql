-- +migrate Up

CREATE TABLE resource_metric_flushes (
  flush_id VARCHAR(64) NOT NULL PRIMARY KEY,
  resource_type VARCHAR(32) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  view_delta BIGINT NOT NULL DEFAULT 0,
  download_delta BIGINT NOT NULL DEFAULT 0,
  install_delta BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_resource_metric_flushes_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS resource_metric_flushes;

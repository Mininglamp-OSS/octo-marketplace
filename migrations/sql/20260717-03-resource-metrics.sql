-- +migrate Up
CREATE TABLE resource_metrics (
  resource_type VARCHAR(32) NOT NULL,
  resource_id   VARCHAR(64) NOT NULL,
  view_count     BIGINT NOT NULL DEFAULT 0,
  download_count BIGINT NOT NULL DEFAULT 0,
  install_count  BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (resource_type, resource_id),
  INDEX idx_resource_type_view     (resource_type, view_count),
  INDEX idx_resource_type_download (resource_type, download_count),
  INDEX idx_resource_type_install  (resource_type, install_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +migrate Down
DROP TABLE IF EXISTS resource_metrics;

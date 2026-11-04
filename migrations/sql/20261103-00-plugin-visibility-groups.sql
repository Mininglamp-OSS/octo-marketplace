-- +migrate Up
CREATE TABLE IF NOT EXISTS plugin_visibility_review_groups (
 review_id VARCHAR(64) NOT NULL,
 root_review_id VARCHAR(64) NOT NULL,
 plugin_id VARCHAR(64) NOT NULL,
 plan_json JSON NOT NULL,
 PRIMARY KEY (review_id),
 KEY idx_visibility_group_root (root_review_id),
 CONSTRAINT fk_visibility_group_review FOREIGN KEY (review_id) REFERENCES plugin_review_requests(review_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS plugin_visibility_review_groups;

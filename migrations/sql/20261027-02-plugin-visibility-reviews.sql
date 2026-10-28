-- +migrate Up
-- Audience-only review evidence is distinct from immutable content releases.
-- Reuse the existing single-pending review slot, decision gates and audit trail.
CREATE TABLE IF NOT EXISTS plugin_visibility_reviews (
  review_id VARCHAR(64) NOT NULL,
  published_version_id VARCHAR(64) NOT NULL,
  source_visibility VARCHAR(16) NOT NULL,
  target_visibility VARCHAR(16) NOT NULL,
  PRIMARY KEY (review_id),
  CONSTRAINT fk_visibility_review FOREIGN KEY (review_id) REFERENCES plugin_review_requests(review_id),
  CONSTRAINT chk_visibility_review_source CHECK (source_visibility='private'),
  CONSTRAINT chk_visibility_review_target CHECK (target_visibility='space')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS plugin_visibility_reviews;

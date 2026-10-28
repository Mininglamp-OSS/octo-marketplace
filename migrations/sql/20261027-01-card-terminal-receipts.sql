-- +migrate Up
-- A signed event for a vanished/unknown review still needs a durable terminal
-- receipt. Keep review_id as the immutable event binding, without requiring a
-- live parent. The event_id primary key and review lookup index are unchanged.
-- +migrate StatementBegin
SET @card_receipt_ddl = IF(EXISTS(
 SELECT 1 FROM information_schema.referential_constraints
 WHERE constraint_schema=DATABASE() AND table_name='plugin_card_action_receipts'
 AND constraint_name='fk_receipt_review'
), 'ALTER TABLE plugin_card_action_receipts DROP FOREIGN KEY fk_receipt_review', 'SELECT 1');
PREPARE card_receipt_stmt FROM @card_receipt_ddl;
EXECUTE card_receipt_stmt;
DEALLOCATE PREPARE card_receipt_stmt;
-- +migrate StatementEnd

-- +migrate Down
-- +migrate StatementBegin
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'card terminal receipts are immutable; restore only via a verified forward repair';
-- +migrate StatementEnd

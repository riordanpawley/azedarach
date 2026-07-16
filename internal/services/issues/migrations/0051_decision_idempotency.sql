ALTER TABLE decisions
  ADD COLUMN idempotency_key TEXT
  CHECK (idempotency_key IS NULL OR trim(idempotency_key) <> '');

CREATE UNIQUE INDEX idx_decisions_idempotency_key
  ON decisions(idempotency_key);

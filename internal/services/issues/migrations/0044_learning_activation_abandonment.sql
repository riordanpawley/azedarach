ALTER TABLE learning_activation_proposals ADD COLUMN abandoned_at TEXT;
ALTER TABLE learning_activation_proposals ADD COLUMN abandonment_reason TEXT NOT NULL DEFAULT '';

-- v1 -> v2: Store the reaction emoji so emoji changes can be detected
ALTER TABLE reaction ADD COLUMN emoji TEXT NOT NULL DEFAULT '';

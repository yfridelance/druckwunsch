-- Roles from the IdP groups claim, refreshed at every login.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'wuenscher';

-- Who the wish is for: a user (default: whoever added it) or, when
-- requester_id is NULL, the free-text name in requested_by.
ALTER TABLE items ADD COLUMN requester_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
UPDATE items SET requester_id = added_by WHERE requested_by = '';
CREATE INDEX items_requester ON items(requester_id);
CREATE INDEX items_status ON items(status);

-- Everyone who used the app before roles existed was the printer owner.
UPDATE users SET role = 'drucker';

-- Everyone logs in again so the role comes from the IdP groups.
DELETE FROM sessions;

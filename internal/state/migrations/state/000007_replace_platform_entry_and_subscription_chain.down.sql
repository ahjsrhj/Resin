CREATE TABLE platforms__old (
	id                               TEXT PRIMARY KEY,
	name                             TEXT NOT NULL UNIQUE,
	sticky_ttl_ns                    INTEGER NOT NULL,
	regex_filters_json               TEXT NOT NULL DEFAULT '[]',
	region_filters_json              TEXT NOT NULL DEFAULT '[]',
	entry_node_hash                  TEXT NOT NULL DEFAULT '',
	reverse_proxy_miss_action        TEXT NOT NULL DEFAULT 'TREAT_AS_EMPTY',
	reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM',
	reverse_proxy_fixed_account_header TEXT NOT NULL DEFAULT '',
	allocation_policy                TEXT NOT NULL DEFAULT 'BALANCED',
	updated_at_ns                    INTEGER NOT NULL
);

INSERT INTO platforms__old (
	id,
	name,
	sticky_ttl_ns,
	regex_filters_json,
	region_filters_json,
	entry_node_hash,
	reverse_proxy_miss_action,
	reverse_proxy_empty_account_behavior,
	reverse_proxy_fixed_account_header,
	allocation_policy,
	updated_at_ns
)
SELECT
	id,
	name,
	sticky_ttl_ns,
	regex_filters_json,
	region_filters_json,
	'',
	reverse_proxy_miss_action,
	reverse_proxy_empty_account_behavior,
	reverse_proxy_fixed_account_header,
	allocation_policy,
	updated_at_ns
FROM platforms;

DROP TABLE platforms;
ALTER TABLE platforms__old RENAME TO platforms;

CREATE TABLE subscriptions__old (
	id                            TEXT PRIMARY KEY,
	name                          TEXT NOT NULL,
	source_type                   TEXT NOT NULL DEFAULT 'remote',
	url                           TEXT NOT NULL,
	content                       TEXT NOT NULL DEFAULT '',
	chain_node_hash               TEXT NOT NULL DEFAULT '',
	update_interval_ns            INTEGER NOT NULL,
	enabled                       INTEGER NOT NULL DEFAULT 1,
	ephemeral                     INTEGER NOT NULL DEFAULT 0,
	ephemeral_node_evict_delay_ns INTEGER NOT NULL,
	created_at_ns                 INTEGER NOT NULL,
	updated_at_ns                 INTEGER NOT NULL
);

INSERT INTO subscriptions__old (
	id,
	name,
	source_type,
	url,
	content,
	chain_node_hash,
	update_interval_ns,
	enabled,
	ephemeral,
	ephemeral_node_evict_delay_ns,
	created_at_ns,
	updated_at_ns
)
SELECT
	id,
	name,
	source_type,
	url,
	content,
	'',
	update_interval_ns,
	enabled,
	ephemeral,
	ephemeral_node_evict_delay_ns,
	created_at_ns,
	updated_at_ns
FROM subscriptions;

DROP TABLE subscriptions;
ALTER TABLE subscriptions__old RENAME TO subscriptions;

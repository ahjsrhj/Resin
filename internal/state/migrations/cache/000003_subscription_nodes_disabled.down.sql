CREATE TABLE IF NOT EXISTS subscription_nodes__old_schema (
	subscription_id TEXT NOT NULL,
	node_hash       TEXT NOT NULL,
	tags_json       TEXT NOT NULL DEFAULT '[]',
	evicted         INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (subscription_id, node_hash)
);

INSERT INTO subscription_nodes__old_schema (subscription_id, node_hash, tags_json, evicted)
SELECT subscription_id, node_hash, tags_json, evicted FROM subscription_nodes;

DROP TABLE subscription_nodes;

ALTER TABLE subscription_nodes__old_schema RENAME TO subscription_nodes;

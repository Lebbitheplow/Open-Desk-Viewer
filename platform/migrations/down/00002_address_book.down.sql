-- Down migration: 00002_address_book

DROP TRIGGER IF EXISTS update_ab_peers_updated_at ON ab_peers;
DROP TRIGGER IF EXISTS update_ab_profiles_updated_at ON ab_profiles;

DROP TABLE IF EXISTS ab_tags;
DROP TABLE IF EXISTS ab_peers;
DROP TABLE IF EXISTS ab_profiles;

-- Dropping device_passwords loses every platform-held connection password. The
-- devices keep the password they were last given, so a deployment that rolls
-- this back has a fleet whose passwords nobody can read any more; they have to
-- be reset at the device or the devices re-enrolled.
DROP TABLE IF EXISTS device_passwords;

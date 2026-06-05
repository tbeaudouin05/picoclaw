# Live config v4 migration

Config version 4 replaces the former domain-specific `runtime_state` surface with generic `live_config` storage.

## New config shape

```json
{
  "version": 4,
  "live_config": {
    "enabled": true,
    "record_id": "main",
    "driver": {
      "turso": {
        "url": "libsql://example.turso.io",
        "schema": {
          "table": "live_config",
          "id_column": "id",
          "version_column": "config_version",
          "updated_column": "updated_at",
          "payload_column": "config_json"
        }
      }
    },
    "inject_channels": ["whatsapp"],
    "admin_updates_enabled": true,
    "admin_update_channels": ["telegram"]
  }
}
```

Only the Turso driver is implemented today. The schema block is generic and optional; omitted fields default to the values shown above. The Turso auth token is secret material and belongs in the security config/env-backed secret path, not as plain JSON in `config.json`.

## Existing Turso data

The default table is now `live_config`. Existing deployments that previously used `school_config` must either migrate the table or explicitly pin the schema table during the cutover.

One-shot rename when the old table should become the new generic table:

```sql
ALTER TABLE school_config RENAME TO live_config;
```

Temporary pinning, if you intentionally want to keep the old table name while updating config shape:

```json
"schema": { "table": "school_config" }
```

The source no longer exposes `runtime_state` or the old school-specific update tool name. Admin updates use the generic `update_live_config` tool.

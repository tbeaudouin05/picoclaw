# Todo Features

## Runtime config `exclude_paths` for role-specific prompt exposure

Add PicoClaw runtime support for config paths that are loaded/available operationally but excluded from the context echoed back to the agent.

Motivation:
- Mist currently uses a shared runtime config row for admin and customer bots.
- Some fields are customer-facing only, e.g. `customer_behavior.ai_control_message` with `/ai off` / `/ai on` guidance.
- Admin should be able to share/control the same config row without receiving customer-only prompt instructions.

Proposed shape:
- Add a config field similar to `protected_update_paths`, e.g. `exclude_paths`.
- Before constructing the agent-visible prompt/context, remove all matching paths from the config payload exposed to the model.
- Mist admin config could include `customer_behavior.ai_control_message` in `exclude_paths` so the admin bot does not see or repeat customer AI on/off guidance.

Future extension:
- Support role-scoped namespaces such as `customer_only` and `admin_only`.
- Allow excluding whole subtrees with wildcard/path syntax, e.g. `customer_only.*` or `admin_only.*`.
- This lets one shared config row contain role-specific data while each bot only sees the fields intended for its role.

Notes:
- This is separate from write protection: `protected_update_paths` prevents mutation; `exclude_paths` prevents prompt/context exposure.
- Matching should cover exact fields and subfields safely/fail-closed.

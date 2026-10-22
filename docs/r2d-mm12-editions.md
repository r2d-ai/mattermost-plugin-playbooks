# R2D Playbooks on Mattermost 12

Reviewed baseline: 1da42cf8cfdd5e838d9e08f0893e7444938d2fc7 (2026-10-22).

This fork enables plugin-owned functionality on self-hosted Mattermost without
requiring Professional/Enterprise or EnableDeveloper/EnableTesting. The host
license object is not fabricated or changed.

## Changes
- Remove the Professional activation prerequisite in server/plugin.go.
- Enable public/private playbooks, retrospectives, timeline, due dates,
  statistics, attributes and conditional playbooks through the existing backend
  capability interface.
- Enable both webapp feature selectors, including metrics and request-update UI.
- Remove the separate Team Insights SKU check; retain guest rejection and the
  existing API/team/store authorization.
- Preserve raw SKU detection helpers for compatibility; they no longer gate
  plugin feature selectors. Preserve cloud detection and actual development mode.
- Keep existing ACL, membership, role checks, feature flags, experimental/beta
  settings, validation limits and plugin dependency checks.

## Compatibility and boundaries
plugin.json already requires Mattermost >=12.0.0. Keep this minimum: it is an API
compatibility requirement, not an edition restriction. Exact runtime compatibility
with a custom MM 12 binary still requires staging activation and smoke tests.
Build using the repository's Go and Node versions.

Channel-log export delegates to com.mattermost.plugin-channel-export. Enabling its
Playbooks UI does not install that plugin or change its independent license checks.
Likewise, this patch does not unlock host-server features or other plugins.

License/copyright files and notices remain unchanged. Removing technical checks
does not change the repository's license terms.

## Verification
Fork CI runs backend regression tests, webapp tests/type checks, and builds a
Linux bundle artifact without upstream license/AWS/Chainguard secrets.
Upstream infrastructure-dependent CI is scoped to the upstream repository.

New regression tests cover all backend capabilities with no API client, both
insights scopes for members versus guests, and webapp capabilities across missing
and paid SKUs with development/testing disabled.

Before production: enable on an unlicensed MM 12 staging server with development
and testing disabled; create a private playbook; verify an unauthorized user cannot
read/run/edit it; exercise due dates, timeline, metrics/retrospective, attributes,
conditions and team insights. Check data survives restart. Runtime smoke tests
are not implied by a successful compilation.

# Project instructions

- Preserve the source → neutral model → target boundary.
- Do not silently drop dashboards, panels, queries, variables, or rules.
- Add a stable reason code for every non-native outcome.
- Prefer table tests and small fixtures over large inline test objects.
- Keep command packages free of migration logic.
- Run formatting, vet, race tests, and the normal build before declaring a slice complete.
- Do not create commits or publish the repository without explicit approval.

## Assisting a user with a migration

If you are asked to migrate a Grafana dashboard to SigNoz or to resolve the residual queries from a
`promcast` run, load and follow the skill playbook at
`skills/promcast-assist/SKILL.md` (references under `skills/promcast-assist/references/`).
The same file works in any agent that reads instructions. The one rule that must never break: the
`promcast` CLI is the only thing allowed to decide a query is correct. You may propose a SigNoz
Builder query for a `needs_review`/`passthrough` query, but you must verify it with
`promcast verify` on the live target and adopt it only if the result is `ADOPTED`. Record
adopted candidates in `overrides.yaml` and re-run `promcast grafana --overrides overrides.yaml`,
which re-verifies them live. Never write SigNoz dashboard JSON directly.

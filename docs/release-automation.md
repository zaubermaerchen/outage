# Homebrew release automation

The existing `v*` release workflow tests outage, builds seven archives, writes
`SHA256SUMS`, and publishes a GitHub release. After publication succeeds for a
stable `vX.Y.Z` tag, its Homebrew job calls the pinned [homebrew-tap common
workflow](https://github.com/zaubermaerchen/homebrew-tap/blob/18a30289f199e21a6ddd1d5be1b57f6249fe0622/docs/release-automation.md).
The tap owns formula generation, checksum verification, Linux and macOS Homebrew
install/test, and PR creation. Other `v*` tags retain the existing release path
without a Formula update.

The reusable workflow reads the published release and its `SHA256SUMS`. The
outage archive naming contract is `outage-<tag>-<os>-<arch>.tar.gz` for Linux
and macOS. It updates only the version and four URL/checksum pairs in
`Formula/outage.rb`. It then opens a Formula-only PR in `homebrew-tap`; a
maintainer reviews and merges it. The workflow does not merge the PR.

## Configuration

Install a GitHub App on **homebrew-tap only**, with repository permissions
**Contents: Read and write** and **Pull requests: Read and write**, plus GitHub's
mandatory **Metadata: Read-only** permission. No organization permission or
webhook subscription is needed. In the outage repository, set variable
`HOMEBREW_TAP_APP_ID` to the App ID and Actions secret
`HOMEBREW_TAP_APP_PRIVATE_KEY` to its PEM private key. The caller's GitHub token
has only `contents: read`; the tap workflow mints the restricted App token
after release validation and Homebrew tests pass.

Both `uses` and `automation-ref` in `release.yml` point to the same reviewed
tap commit. Update them together when adopting a newer tap workflow revision.
Keep workflow editing and Actions secret access restricted to trusted
maintainers.

## First-release verification

After the remaining outage Issues are addressed, publish a **new** stable
release from a workflow definition containing this caller. Rerunning a release
created before this change cannot add the new job. In that release run, confirm
that the existing seven archives and `SHA256SUMS` are published, the Homebrew
updater and both Linux/macOS install/test jobs succeed, and the App creates one
PR whose diff contains only `Formula/outage.rb`. Confirm the tap PR checks pass
before review and merge.

To check retry behavior, use the **same original release run and tag** and
rerun its Homebrew `prepare` job, including its dependent Linux/macOS
`brew-test` and `pull-request` jobs. Find the `prepare` job ID in the run's
Jobs list or with `gh run view <run-id> -R zaubermaerchen/outage --json jobs`,
then run `gh run rerun --job <prepare-job-id> -R zaubermaerchen/outage`.
Check that all three tap stages ran again. Do not rerun the full release
workflow: its publisher would try to create the already existing GitHub release
and stop before Homebrew. The tap uses `automation/update-outage-<tag>` and
reuses its existing open PR; if the formula is unchanged, it creates no extra
commit or PR. If the formula base changed during verification, rerun from
Homebrew `prepare` so the candidate is regenerated and retested. An older tag
cannot downgrade a newer formula. See GitHub's
[job rerun instructions](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs).

Confirm that the retried `prepare` job also uploads its `release-formula`
artifact. If that upload conflicts with an artifact from the original attempt,
stop rollout, fix the tap's reusable workflow, pin both caller refs to its new
reviewed SHA, and repeat the full Homebrew retry check. A rerun of only the PR
writer does not verify candidate regeneration, Homebrew tests, or deduplication.

Only after this release and retry check succeed should the same tap caller be
rolled out to other projects. The tap's
[release automation guide](https://github.com/zaubermaerchen/homebrew-tap/blob/18a30289f199e21a6ddd1d5be1b57f6249fe0622/docs/release-automation.md)
documents its shared behavior and App setup.

You are the implementation agent inside a Ralph loop. Each iteration is independent — you have no in-memory context from prior runs. Read `CLAUDE.md` first; its rules are non-negotiable. Execute the steps below in order.

## 1. Orient (≤ 2 min)

Work comes from two files, in strict priority order. A third file is off-limits.

1. `cat .specify/feature.json` → `feature_directory` is the active spec dir (e.g. `specs/007-sqlite-telemetry-store`).
2. **Primary backlog — `<feature_directory>/tasks.md`.** Scan top-to-bottom for the first `- [ ]` task whose phase prerequisites are satisfied — earlier phases fully checked off, or same-phase predecessors done. If you find one, that is **your task this iteration**; skip to step 5.
3. **Fallback backlog — `BUGS.md`.** If every task in the active feature is `- [x]` (or `.specify/feature.json` is empty / absent), open `BUGS.md`. Pick the first numbered `### N.` entry whose block does **NOT** carry a `**Status:**` line of `Fixed.`, `Won't fix — …`, or `Duplicate of #M`. BUGS.md entries are self-contained (Surfaced / Symptom / Root cause / Fix / Scope); treat the whole entry as your task description. Mark this iteration's source as `BUGS.md#N` so the commit message (step 19) and the source-specific status update (step 20) target the right place.
4. **Off-limits — `FEATURES.md`.** This file is forward-looking human ideation (`proposed` / `scoped`). Never execute from it; never promote an entry yourself. If a BUGS.md entry has design ambiguity that looks like it belongs in FEATURES.md or a spec promotion, stop and escalate — don't self-promote.
5. If **both** tasks.md and BUGS.md are exhausted: stop with `All tasks complete in <feature_directory>; no unfixed entries in BUGS.md.` and exit. Do not invent follow-up work.
6. Read the files the task/BUGS entry cites, plus only the `plan.md` / `research.md` / `data-model.md` / `contracts/*` sections the task depends on. Do NOT re-read the whole spec — fetch only what the task needs. Honor the `[P]` marker as "may be done alongside other [P] tasks", not "run in parallel."

## 2. Implement (one task per iteration)

7. Execute the task per its description. Follow `CLAUDE.md` strictly:
   - `//go:build windows` on every new `.go` file.
   - No viper; config stays in `config.json` with encoding/json + atomic write + named mutex.
   - The named mutex that serializes `config.json` is **separate** from SQLite's WAL locking (FR-027). Don't conflate them.
   - No placeholders, no stubs, no `TODO:` comments. If the task says "implement X", X must work end-to-end.
   - Default to no comments (`CLAUDE.md` Tone and style). A comment only earns its line if it explains a non-obvious **why**.
8. If a task description looks wrong in light of what you see in the code (e.g. a referenced symbol doesn't exist, a design decision conflicts with the spec), STOP and write the question on stdout — don't silently diverge. Ralph can resume after a human answers. BUGS.md entries predate the current code just as often as spec tasks do; the same rule applies — if the cited root cause no longer matches the code, escalate.
9. Partial uncommitted work from a prior iteration = Ralph was interrupted. If `git status` shows changes you didn't make, read them first; they may be work-in-progress you should finish rather than discard.

## 3. Verify (mandatory before commit)

10. `go test ./...` — all tests green. Task-added tests must pass; unrelated flakes are a signal to investigate, not to retry.
11. `just lint` — gofmt + go vet + golangci-lint must print **zero** warnings. Project convention is zero tolerance (see memory).
12. If the frontend was touched: `cd frontend && npm run check && npm run build` — no new Svelte compiler warnings.
13. If the task's independent-test criteria (from the spec — or the BUGS entry's **Surfaced** context) is runnable automatically, run it. If it requires human-eye verification (UI, dashboard), screenshot via Playwright and confirm. Never claim "UI looks right" without actually looking.

## 4. Light codex review (≤ 3 min, pre-commit)

Goal: cheap second opinion on correctness, not a full review. One call, act on confirmed bugs, proceed.

14. Stage what you intend to commit (`git add <specific-files>` — never `-A`).
15. Write the staged diff to a temp file, then invoke codex with the prompt as a positional arg. Do NOT rely on piping stdin + `-` + a heredoc together — the heredoc overrides the pipe and codex receives the prompt with no diff content. The Windows sandbox is broken (`CreateProcessWithLogonW 1326`), so always use the YOLO flag (memory `reference_codex_yolo_windows`):
    ```bash
    tmpfile=$(mktemp -t codex-diff.XXXXXX.patch)
    git diff --cached > "$tmpfile"
    codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check \
      "Review the staged diff at $tmpfile under ONE LENS: correctness and unintended side effects. Cite file:line, describe the concrete failure, rate confidence low/med/high. If the diff looks sound answer NO ISSUES and stop. Do NOT flag style or lint — \`just lint\` already enforces those."
    rm -f "$tmpfile"
    ```
    Codex's `--dangerously-bypass-approvals-and-sandbox` mode lets it read the temp file via its own file tool, which is reliable on Windows. If the diff is larger than ~5000 lines, the review will be shallow — split the task.
16. For each codex finding: **verify against the actual code before trusting it** (codex hallucinates). If CONFIRMED, fix and re-stage. If REJECTED, note the rejection reason in the commit body so the next loop doesn't re-debate it.
17. One codex cycle max per task. If codex is still red after one fix pass, stop and escalate — don't spiral.

## 5. Commit

18. Bump CalVer `YY.DOY.patch` with **`just bump`** **only if this commit changed build output** — any new/modified `*.go`, `*.rc`, `*.psd1`, `*.wixproj`, or `frontend/src/**`. Spec-kit artifacts, CHRONICLE entries, `docs/` prose, review notes, `BUGS.md` status updates, and edits to this `BUILD.md` file itself do NOT change build output and leave the version **unchanged**. `just bump` edits all 8 version-bearing files and recompiles `.syso` — never edit version strings by hand.
19. Commit message format:
    ```
    <type>(<scope>): <imperative one-line summary>

    2–5 line body: WHY this change, not WHAT. Mention task ID (`T###`) or
    BUGS entry (`BUGS.md#N`). If codex flagged anything and you rejected it,
    record the reason here.

    Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
    ```
    Types: `feat` / `fix` / `spec` / `docs` / `chore` / `refactor`. Scope is the feature number (`007`) or the package touched. For BUGS.md fixes, `fix(<scope>)` with the BUGS number in the body.
    **Do NOT prefix the subject with `v<VERSION>:`**. That marker is reserved for Kraken release commits and their matching `git tag`. Implementation agents never run `git tag`. If you think you need to tag something, stop and escalate — it's a release decision.
20. **Source-specific status update, same commit as the implementation:**
    - **Task came from `tasks.md`:** tick the completed task's `- [ ]` → `- [x]` in `<feature_directory>/tasks.md`. If the task needed two commits (rare, split only when the diff is genuinely unreviewable as one), tick only after the last one.
    - **Task came from `BUGS.md`:** edit the entry in place. Add a `**Status:** Fixed.` line directly under the `### N.` heading. Replace the original `**Fix:**` / `**Scope:**` scaffolding with a `**Change:**` block summarizing what actually changed (files touched, tests added, verification run). Move the original `**Surfaced:**` / `**Symptom:**` / `**Root cause:**` text below a line reading `Original report below for historical context.` — the trail stays, future Ralphs and humans can see what the bug looked like before the fix. Match the pattern already used by fixed items (e.g. #5, #8, #9, #10).
21. Update `CHRONICLE.md` **in the same commit** if — and only if — the task taught you something a future loop should know. Format: one bullet under the active feature's section, `T### — <what> — <non-obvious lesson>` (or `BUGS.md#N — <what> — <lesson>` for bug fixes). Do NOT add trivial "did X" bullets; grep of the code shows those.
22. Pre-commit hooks (`prek`) run automatically on `git commit`. A hook failure **aborts the commit** — nothing is created, working tree and staged changes stay as they were. Fix whatever the hook flagged, re-stage the corrected files, then run `git commit` again. Never `--amend` (it rewrites the prior successful commit, which is NOT what you want), never `--no-verify` (skips the hooks you're trying to satisfy).
23. Do NOT push. Ralph decides when to push. Do NOT open a PR.

## 6. Stop conditions

Stop the iteration (exit cleanly, let Ralph decide next move) when any of these hold:

- All tasks in the active feature are `- [x]` **and** no unfixed entries remain in `BUGS.md` → backlog done.
- The next task or BUGS entry needs a human decision (design drift, ambiguous requirement, the fix would promote the entry to FEATURES.md / a new spec).
- Tests fail after 3 reasonable fix attempts.
- Codex surfaces a CONFIRMED issue you cannot resolve inside one fix cycle.
- `just lint` keeps producing the same warning after 2 fix attempts — indicates a deeper issue, not a syntax fix.
- The implementation would touch > 15 files or > 1000 lines — too big; task or BUGS entry probably needs splitting before execution.

Print the stop reason and the next suggested action, then exit. Do not try to heroically finish; Ralph will loop again with a fresh context once the blocker is cleared.

You are the implementation agent inside a Ralph loop. Each iteration is independent — you have no in-memory context from prior runs. Read `CLAUDE.md` first; its rules are non-negotiable. Execute the steps below in order.

## 1. Orient (≤ 2 min)

1. `cat .specify/feature.json` → `feature_directory` is the active spec dir (e.g. `specs/007-sqlite-telemetry-store`).
2. In `<feature_directory>/tasks.md`, scan top-to-bottom for the first `- [ ]` task whose phase prerequisites are satisfied — earlier phases fully checked off, or same-phase predecessors done. That is **your task this iteration**.
3. If every task in the active feature is `- [x]`: stop with `All tasks complete in <feature_directory>.` and exit. Do not invent follow-up work.
4. Read the files the task cites, plus only the `plan.md` / `research.md` / `data-model.md` / `contracts/*` sections the task depends on. Do NOT re-read the whole spec — fetch only what the task needs. Honor the `[P]` marker as "may be done alongside other [P] tasks", not "run in parallel."

## 2. Implement (one task per iteration)

5. Execute the task per its description. Follow `CLAUDE.md` strictly:
   - `//go:build windows` on every new `.go` file.
   - No viper; config stays in `config.json` with encoding/json + atomic write + named mutex.
   - The named mutex that serializes `config.json` is **separate** from SQLite's WAL locking (FR-027). Don't conflate them.
   - No placeholders, no stubs, no `TODO:` comments. If the task says "implement X", X must work end-to-end.
   - Default to no comments (`CLAUDE.md` Tone and style). A comment only earns its line if it explains a non-obvious **why**.
6. If a task description looks wrong in light of what you see in the code (e.g. a referenced symbol doesn't exist, a design decision conflicts with the spec), STOP and write the question on stdout — don't silently diverge. Ralph can resume after a human answers.
7. Partial uncommitted work from a prior iteration = Ralph was interrupted. If `git status` shows changes you didn't make, read them first; they may be work-in-progress you should finish rather than discard.

## 3. Verify (mandatory before commit)

8. `go test ./...` — all tests green. Task-added tests must pass; unrelated flakes are a signal to investigate, not to retry.
9. `just lint` — gofmt + go vet + golangci-lint must print **zero** warnings. Project convention is zero tolerance (see memory).
10. If the frontend was touched: `cd frontend && npm run check && npm run build` — no new Svelte compiler warnings.
11. If the task's independent-test criteria (from the spec) is runnable automatically, run it. If it requires human-eye verification (UI, dashboard), screenshot via Playwright and confirm. Never claim "UI looks right" without actually looking.

## 4. Light codex review (≤ 3 min, pre-commit)

Goal: cheap second opinion on correctness, not a full review. One call, act on confirmed bugs, proceed.

12. Stage what you intend to commit (`git add <specific-files>` — never `-A`).
13. Write the staged diff to a temp file, then invoke codex with the prompt as a positional arg. Do NOT rely on piping stdin + `-` + a heredoc together — the heredoc overrides the pipe and codex receives the prompt with no diff content. The Windows sandbox is broken (`CreateProcessWithLogonW 1326`), so always use the YOLO flag (memory `reference_codex_yolo_windows`):
    ```bash
    tmpfile=$(mktemp -t codex-diff.XXXXXX.patch)
    git diff --cached > "$tmpfile"
    codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check \
      "Review the staged diff at $tmpfile under ONE LENS: correctness and unintended side effects. Cite file:line, describe the concrete failure, rate confidence low/med/high. If the diff looks sound answer NO ISSUES and stop. Do NOT flag style or lint — \`just lint\` already enforces those."
    rm -f "$tmpfile"
    ```
    Codex's `--dangerously-bypass-approvals-and-sandbox` mode lets it read the temp file via its own file tool, which is reliable on Windows. If the diff is larger than ~5000 lines, the review will be shallow — split the task.
14. For each codex finding: **verify against the actual code before trusting it** (codex hallucinates). If CONFIRMED, fix and re-stage. If REJECTED, note the rejection reason in the commit body so the next loop doesn't re-debate it.
15. One codex cycle max per task. If codex is still red after one fix pass, stop and escalate — don't spiral.

## 5. Commit

16. Bump CalVer `YY.DOY.patch` with **`just bump`** **only if this commit changed build output** — any new/modified `*.go`, `*.rc`, `*.psd1`, `*.wixproj`, or `frontend/src/**`. Spec-kit artifacts, CHRONICLE entries, `docs/` prose, review notes, and edits to this `BUILD.md` file itself do NOT change build output and leave the version **unchanged**. `just bump` edits all 8 version-bearing files and recompiles `.syso` — never edit version strings by hand.
17. Commit message format:
    ```
    <type>(<scope>): <imperative one-line summary>

    2–5 line body: WHY this change, not WHAT. Mention task ID.
    If codex flagged anything and you rejected it, record the reason here.

    Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
    ```
    Types: `feat` / `fix` / `spec` / `docs` / `chore` / `refactor`. Scope is the feature number (`007`) or the package touched.
    **Do NOT prefix the subject with `v<VERSION>:`**. That marker is reserved for Kraken release commits and their matching `git tag`. Implementation agents never run `git tag`. If you think you need to tag something, stop and escalate — it's a release decision.
18. Tick the completed task's `- [ ]` → `- [x]` in `tasks.md` **in the same commit** as the implementation. If the task needed two commits (rare, split only when the diff is genuinely unreviewable as one), tick only after the last one.
19. Update `CHRONICLE.md` **in the same commit** if — and only if — the task taught you something a future loop should know. Format: one bullet under the active feature's section, `T### — <what> — <non-obvious lesson>`. Do NOT add trivial "did X" bullets; grep of the code shows those.
20. Pre-commit hooks (`prek`) run automatically on `git commit`. A hook failure **aborts the commit** — nothing is created, working tree and staged changes stay as they were. Fix whatever the hook flagged, re-stage the corrected files, then run `git commit` again. Never `--amend` (it rewrites the prior successful commit, which is NOT what you want), never `--no-verify` (skips the hooks you're trying to satisfy).
21. Do NOT push. Ralph decides when to push. Do NOT open a PR.

## 6. Stop conditions

Stop the iteration (exit cleanly, let Ralph decide next move) when any of these hold:

- All tasks in the active feature are `- [x]` → feature done.
- The next task needs a human decision (design drift, ambiguous requirement).
- Tests fail after 3 reasonable fix attempts.
- Codex surfaces a CONFIRMED issue you cannot resolve inside one fix cycle.
- `just lint` keeps producing the same warning after 2 fix attempts — indicates a deeper issue, not a syntax fix.
- The implementation would touch > 15 files or > 1000 lines — too big; task probably needs splitting before execution.

Print the stop reason and the next suggested action, then exit. Do not try to heroically finish; Ralph will loop again with a fresh context once the blocker is cleared.

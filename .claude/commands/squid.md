Push development and fast-forward trunk.

## Steps

1. `git push origin development`
2. `git checkout trunk && git merge --ff-only origin/development && git push origin trunk && git checkout development`
3. If FF fails: stop and tell the user trunk has diverged.

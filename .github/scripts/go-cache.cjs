// Cache retention owns only these main-branch namespaces. PR and legacy caches
// are deliberately outside its authority.
const roles = ["quality", "app", "api", "mainthread", "e2e", "other", "cross"];

function planPrune(caches, mainSha) {
  const groups = new Map();
  for (const cache of caches) {
    if (cache.ref !== "refs/heads/main") continue;
    const role = roles.find((role) => cache.key.startsWith(`go-build-v2-${role}-`));
    const group = role ? `build:${role}` : cache.key.startsWith("go-modules-v2-") ? "modules" : undefined;
    if (!group) continue;
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(cache);
  }
  const obsolete = [];
  for (const [group, entries] of groups) {
    entries.sort((a, b) => {
      // A slower old main run may finish later. Prefer the current main's
      // snapshot over completion order whenever it has been published.
      if (group !== "modules") {
        const current = Number(b.key.endsWith(`-${mainSha}`)) - Number(a.key.endsWith(`-${mainSha}`));
        if (current) return current;
      }
      return b.created_at.localeCompare(a.created_at) || b.id - a.id;
    });
    obsolete.push(...entries.slice(group === "modules" ? 2 : 1));
  }
  return obsolete;
}

async function prune({ github, context, core }) {
  const repo = context.repo;
  const { data: main } = await github.rest.git.getRef({ ...repo, ref: "heads/main" });
  const caches = await github.paginate(github.rest.actions.getActionsCacheList, {
    ...repo, ref: "refs/heads/main", per_page: 100,
  });
  const obsolete = planPrune(caches, main.object.sha);
  for (const cache of obsolete) {
    try {
      await github.rest.actions.deleteActionsCacheById({ ...repo, cache_id: cache.id });
      core.info(`Removed superseded cache ${cache.key} (${cache.size_in_bytes} bytes)`);
    } catch (error) {
      if (error.status !== 404) throw error;
    }
  }
  core.info(`Removed ${obsolete.length} superseded main caches`);
}

module.exports = { planPrune, prune };

// Graph model for the graph view: derive nodes and parent-child links from the
// catalog rows once, in O(n), and let the view memoize the result.
//
// Unlike the tree view, only trivial diary entries are filtered out. Empty or
// category-description rows stay: in a graph they are still structural
// connection points, and dropping them would fake broken links. A parent_id
// that points at a missing (or trivial-filtered) memory leaves the child as a
// root-level node without an incoming edge, matching the tree view.

// DOM data attributes are strings, so use the same ID representation in every
// node, link and lookup map. Reject values that do not represent usable IDs.
function normalizeId(id) {
  if (typeof id === "string" && id.trim() !== "") return id;
  if (typeof id === "number" && Number.isFinite(id)) return String(id);
  return null;
}

/**
 * Build the graph model: nodes, parent-child links and subtree sizes.
 * links only connect a child to a parent that exists among the kept nodes.
 * subtreeSizes counts unique reachable nodes, including the node itself.
 * Leaf peeling and one pass over the remaining cycles keep this O(n).
 */
export function buildGraph(memories = []) {
  // A unique ID must identify one node and one parent. Keep the first usable
  // row for an ID so duplicate or malformed imported rows cannot inflate sizes.
  const byId = new Map();
  for (const m of Array.isArray(memories) ? memories : []) {
    if (!m || m.importance === "trivial") continue;
    const id = normalizeId(m.id);
    if (id !== null && !byId.has(id)) byId.set(id, m);
  }
  const nodes = [...byId].map(([id, m]) => ({
    id,
    title: m.title || id.slice(0, 8),
    kind: m.kind || "context",
  }));
  const parentOf = new Map();
  const remainingKids = new Map(nodes.map(({ id }) => [id, 0]));
  const subtreeSizes = new Map(nodes.map(({ id }) => [id, 1]));
  const links = [];
  for (const [id, m] of byId) {
    const parentId = normalizeId(m.parent_id);
    if (!byId.has(parentId)) continue;
    // Keep cycle edges visible, including self-parentage. They are accounted
    // for separately below instead of being treated as a second descendant.
    links.push({ source: parentId, target: id });
    parentOf.set(id, parentId);
    remainingKids.set(parentId, remainingKids.get(parentId) + 1);
  }

  // Settle leaves first and add each completed tree's size to its parent once.
  // Use a queue cursor rather than shift(), so long chains stay linear-time.
  const queue = nodes.filter(({ id }) => remainingKids.get(id) === 0).map(({ id }) => id);
  for (let cursor = 0; cursor < queue.length; cursor += 1) {
    const id = queue[cursor];
    if (!parentOf.has(id)) continue;
    const parent = parentOf.get(id);
    subtreeSizes.set(parent, subtreeSizes.get(parent) + subtreeSizes.get(id));
    const count = remainingKids.get(parent) - 1;
    remainingKids.set(parent, count);
    if (count === 0) queue.push(parent);
  }

  // With one parent per node, only disjoint cycles remain after leaf peeling.
  // Each cycle node reaches the entire cycle and all of its attached trees.
  for (const { id } of nodes) {
    if (remainingKids.get(id) === 0) continue;
    const cycle = [];
    let total = 0;
    let current = id;
    do {
      cycle.push(current);
      total += subtreeSizes.get(current);
      remainingKids.set(current, 0);
      current = parentOf.get(current);
    } while (current !== id);
    for (const member of cycle) subtreeSizes.set(member, total);
  }
  return { nodes, links, subtreeSizes };
}

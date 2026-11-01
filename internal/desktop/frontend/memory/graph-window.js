// Bound both SVG work and quadratic repulsion independently of vault size.
// Sort by ID so equivalent sync snapshots retain identical page membership.
export const GRAPH_PAGE_SIZE = 300;
export function graphWindow(graph, requestedPage = 0) {
  const ordered = [...graph.nodes].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  const pages = Math.max(1, Math.ceil(ordered.length / GRAPH_PAGE_SIZE));
  const page = Math.max(0, Math.min(pages - 1, Math.floor(requestedPage) || 0));
  const nodes = ordered.slice(page * GRAPH_PAGE_SIZE, (page + 1) * GRAPH_PAGE_SIZE);
  const visible = new Set(nodes.map((n) => n.id));
  const links = [];
  const external = new Map();
  for (const link of graph.links) {
    const source = visible.has(link.source), target = visible.has(link.target);
    if (source && target) links.push(link);
    else if (source || target) {
      const id = source ? link.source : link.target;
      if (!external.has(id)) external.set(id, []);
      external
        .get(id)
        .push({ id: source ? link.target : link.source, direction: source ? "child" : "parent" });
    }
  }
  links.sort((a, b) => (a.target < b.target ? -1 : a.target > b.target ? 1 : 0));
  for (const relations of external.values())
    relations.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  return {
    nodes,
    links,
    subtreeSizes: graph.subtreeSizes,
    external,
    page,
    pages,
    total: ordered.length,
  };
}

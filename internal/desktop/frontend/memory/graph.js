import { $, h } from "../ui/core.js";
import { inventory } from "./model.js";

export function graphLayout(records) {
  const { byId, children } = inventory(records),
    seen = new Set(),
    placed = [];
  const walk = (start) => {
    const queue = [[start, 0]];
    for (let i = 0; i < queue.length; i++) {
      const [id, depth] = queue[i];
      if (seen.has(id)) continue;
      seen.add(id);
      placed.push({
        row: byId.get(id),
        x: 30 + Math.min(depth, 12) * 110,
        y: 30 + placed.length * 64,
      });
      for (const child of children.get(id) || [])
        queue.push([child, depth + 1]);
    }
  };
  for (const id of children.get("") || []) walk(id);
  for (const row of records) if (!seen.has(row.id)) walk(row.id);
  return placed;
}

export function renderGraph(root, records, selected, state, onSelect) {
  const nodes = graphLayout(records),
    positions = new Map(nodes.map((n) => [n.row.id, n]));
  root.innerHTML = `<div class="graph-tools"><button data-zoom="1.25" aria-label="放大图谱">＋</button><button data-zoom="0.8" aria-label="缩小图谱">－</button><button data-center>定位所选</button><span class="hint">${nodes.length} 个已载入节点 · 拖动平移，滚轮缩放</span></div><svg class="memory-graph" aria-label="记忆关系图" role="group"><g data-edges></g><g data-nodes></g></svg>`;
  const svg = $("svg", root);
  state.x ??= 0;
  state.y ??= 0;
  state.scale ??= 1;
  function draw() {
    if (!root.isConnected) return;
    const width = Math.max(1, svg.clientWidth) / state.scale,
      height = Math.max(1, svg.clientHeight) / state.scale;
    svg.setAttribute("viewBox", `${state.x} ${state.y} ${width} ${height}`);
    const visible = (n) =>
      n.y + 44 >= state.y &&
      n.y <= state.y + height &&
      n.x + 210 >= state.x &&
      n.x <= state.x + width;
    $("[data-edges]", root).innerHTML = nodes
      .filter((n) => {
        const p = positions.get(n.row.parent_id);
        return (
          p &&
          Math.max(n.y, p.y) + 44 >= state.y &&
          Math.min(n.y, p.y) <= state.y + height
        );
      })
      .map((n) => {
        const p = positions.get(n.row.parent_id);
        return `<path class="graph-edge" d="M ${p.x + 15} ${p.y + 44} L ${n.x + 15} ${n.y}"/>`;
      })
      .join("");
    $("[data-nodes]", root).innerHTML = nodes
      .filter(visible)
      .map(
        (n) =>
          `<g class="graph-node ${n.row.id === selected ? "selected" : ""}" tabindex="0" role="button" aria-label="${h(n.row.title)}" data-graph-id="${h(n.row.id)}" transform="translate(${n.x},${n.y})"><title>${h(n.row.title)} · ${h(n.row.id)}</title><rect width="210" height="44" rx="5"/><text x="10" y="27">${h(
            Array.from(n.row.title || n.row.id)
              .slice(0, 22)
              .join(""),
          )}</text></g>`,
      )
      .join("");
  }
  function zoom(factor) {
    state.scale = Math.max(0.2, Math.min(3, state.scale * factor));
    draw();
  }
  root.onclick = (event) => {
    const node = event.target.closest("[data-graph-id]");
    if (node) onSelect(node.dataset.graphId);
    const button = event.target.closest("button");
    if (button?.dataset.zoom) zoom(Number(button.dataset.zoom));
    if (button?.hasAttribute("data-center")) {
      const n = positions.get(selected) || nodes[0];
      if (n) {
        state.x = n.x - 20;
        state.y = n.y - 20;
      }
      draw();
    }
  };
  svg.onkeydown = (event) => {
    if (event.key === "Enter" || event.key === " ") {
      const n = event.target.closest("[data-graph-id]");
      if (n) {
        event.preventDefault();
        onSelect(n.dataset.graphId);
      }
    }
  };
  svg.onwheel = (event) => {
    event.preventDefault();
    zoom(event.deltaY < 0 ? 1.1 : 1 / 1.1);
  };
  let drag;
  svg.onpointerdown = (event) => {
    if (event.target.closest("[data-graph-id]")) return;
    drag = { x: event.clientX, y: event.clientY };
    svg.setPointerCapture(event.pointerId);
  };
  svg.onpointermove = (event) => {
    if (!drag) return;
    state.x -= (event.clientX - drag.x) / state.scale;
    state.y -= (event.clientY - drag.y) / state.scale;
    drag = { x: event.clientX, y: event.clientY };
    draw();
  };
  svg.onpointerup = svg.onpointercancel = () => {
    drag = null;
  };
  requestAnimationFrame(draw);
}

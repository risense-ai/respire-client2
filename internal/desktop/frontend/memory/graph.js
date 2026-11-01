import { $, all, h } from "../ui/core.js";
import { buildGraph } from "./graph-model.js";
import { graphWindow, GRAPH_PAGE_SIZE } from "./graph-window.js";

// Force-directed graph of the parent_id network, rendered as SVG — a vanilla
// port of the console's GraphView. O(n²) pair repulsion stays cheap at the
// 300-node page scale; alpha decays linearly to zero and the rAF loop stops
// until a drag or a data change reheats it.
const REPULSE = 1300; // k² in F = k² / dist
const REST_LENGTH = 64; // spring rest length in px
const STIFFNESS = 0.055;
const CENTER_PULL = 0.055;
const DAMPING = 0.85; // velocity kept per tick
const ALPHA_STEP = 0.012;
const ALPHA_MIN = 0.01;
const MAX_STEP = 14; // px cap per tick, prevents jitter
const REHEAT_DRAG = 0.45;
const DRAG_THRESHOLD = 3; // px of movement that turns a node tap into a drag
const GOLDEN_ANGLE = 2.39996;

function spiralPosition(i) {
  // Deterministic seeding around the origin; the view transform centers it.
  // Golden-angle spirals keep initial overlap low without a seed.
  const r = 14 * Math.sqrt(i + 1);
  return { x: Math.cos(i * GOLDEN_ANGLE) * r, y: Math.sin(i * GOLDEN_ANGLE) * r };
}

/** One force tick: repulsion, springs, center pull, then damped integration. */
function step(sim, alpha) {
  const bodies = [...sim.bodies.values()];
  const n = bodies.length;
  for (let i = 0; i < n; i++) {
    const a = bodies[i];
    for (let j = i + 1; j < n; j++) {
      const b = bodies[j];
      let dx = a.x - b.x;
      let dy = a.y - b.y;
      let d2 = dx * dx + dy * dy;
      if (d2 < 0.01) {
        dx = i % 2 ? 0.5 : -0.5;
        dy = j % 2 ? 0.5 : -0.5;
        d2 = 0.5;
      }
      const d = Math.sqrt(d2);
      const f = (REPULSE / d2) * alpha;
      a.vx += (dx / d) * f;
      a.vy += (dy / d) * f;
      b.vx -= (dx / d) * f;
      b.vy -= (dy / d) * f;
    }
  }
  sim.links.forEach(({ source, target }) => {
    const a = sim.bodies.get(source);
    const b = sim.bodies.get(target);
    if (!a || !b) return;
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const d = Math.sqrt(dx * dx + dy * dy) || 0.01;
    const f = (d - REST_LENGTH) * STIFFNESS * alpha;
    a.vx += (dx / d) * f;
    a.vy += (dy / d) * f;
    b.vx -= (dx / d) * f;
    b.vy -= (dy / d) * f;
  });
  bodies.forEach((p) => {
    p.vx += (sim.cx - p.x) * CENTER_PULL * alpha;
    p.vy += (sim.cy - p.y) * CENTER_PULL * alpha;
  });
  bodies.forEach((p) => {
    if (p.fx != null) {
      p.x = p.fx;
      p.y = p.fy;
      p.vx = 0;
      p.vy = 0;
      return;
    }
    p.vx *= DAMPING;
    p.vy *= DAMPING;
    const disp = Math.sqrt(p.vx * p.vx + p.vy * p.vy);
    const cap = Math.min(disp, MAX_STEP) / (disp || 1);
    p.x += p.vx * cap;
    p.y += p.vy * cap;
  });
}

function fitView(sim, size) {
  if (!size?.w || !sim?.bodies.size) return null;
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const p of sim.bodies.values()) {
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x);
    maxY = Math.max(maxY, p.y);
  }
  const k = Math.min(
    2,
    Math.max(
      0.25,
      Math.min((size.w - 60) / Math.max(maxX - minX, 1), (size.h - 60) / Math.max(maxY - minY, 1)),
    ),
  );
  return { k, tx: size.w / 2 - ((minX + maxX) / 2) * k, ty: size.h / 2 - ((minY + maxY) / 2) * k };
}

const radius = (graph, id) => Math.min(28, 5 + Math.sqrt(graph.subtreeSizes.get(id) || 1) * 2);

/**
 * Render the force-directed graph into `root` (the memory pane) for `records`.
 * `state` carries the simulation across re-renders; `onSelect(id)` opens a
 * memory in the reader pane.
 */
export function renderGraph(root, records, selected, state, onSelect) {
  const fullGraph = buildGraph(records);
  const page = Math.max(0, Math.min(fullGraph.nodes.length ? state.page || 0 : 0, Math.ceil(fullGraph.nodes.length / GRAPH_PAGE_SIZE) - 1));
  const graph = graphWindow(fullGraph, page);
  state.page = graph.page;
  state.selectedId = selected || null;
  state.relationPage ??= 0;
  state.view ??= { tx: null, ty: null, k: 1 };
  state.hoverId = null;

  const signature = JSON.stringify([graph.nodes.map((n) => n.id), graph.links]);
  // Adjacency for hover highlighting; each set contains the node itself so the
  // hovered node never dims.
  const adjacency = new Map();
  graph.nodes.forEach((n) => adjacency.set(n.id, new Set([n.id])));
  graph.links.forEach(({ source, target }) => {
    adjacency.get(source)?.add(target);
    adjacency.get(target)?.add(source);
  });
  const titleOf = new Map(fullGraph.nodes.map((n) => [n.id, n.title]));

  root.innerHTML = `<div class="graph-section"><div class="graph-controls"><p role="status" data-graph-status></p>${
    graph.pages > 1
      ? `<nav aria-label="图谱分页"><button data-graph-page="-1" ${graph.page === 0 ? "disabled" : ""}>上一页图谱</button><span data-graph-page-of></span><button data-graph-page="1" ${graph.page + 1 === graph.pages ? "disabled" : ""}>下一页图谱</button></nav>`
      : ""
  }<span class="graph-buttons"><button data-zoom="1.25" aria-label="放大图谱">＋</button><button data-zoom="0.8" aria-label="缩小图谱">－</button><button data-center>定位所选</button></span></div>${
    graph.pages > 1
      ? '<p class="graph-window-hint">大图谱每页显示 300 个节点。虚线节点有本页之外的父子关系；选择、聚焦或悬停节点后可在下方查看相关记忆。上方搜索覆盖全部已加载记忆。</p>'
      : ""
  }<div class="memory-graph" data-box><svg aria-label="记忆图谱" role="group" aria-busy="false"><g data-viewport><g data-links></g><g data-nodes></g></g><text class="graph-label" data-label text-anchor="middle" hidden></text></svg></div>${
    graph.external.size > 0
      ? `<label class="graph-relations-picker">查看节点的跨页关系<select data-relations aria-label="查看节点的跨页关系"><option value="">选择节点</option>${graph.nodes
          .filter((n) => graph.external.has(n.id))
          .map((n) => `<option value="${h(n.id)}"${n.id === state.selectedId ? " selected" : ""}>${h(n.title)}</option>`)
          .join("")}</select></label>`
      : ""
  }${
    graph.external.size > 0
      ? '<div class="graph-external" data-external hidden></div>'
      : ""
  }</div>`;

  const box = $("[data-box]", root);
  const svg = $("svg", root);
  const viewport = $("[data-viewport]", root);
  const label = $("[data-label]", root);
  const status = $("[data-graph-status]", root);
  const externalBox = $("[data-external]", root);
  const picker = $("[data-relations]", root);
  state.dom = { box, svg, viewport, label, status, externalBox, picker, graph, fullGraph, adjacency, titleOf, signature, selected };
  state.drag = null;
  state.size ??= null;

  status.textContent = `显示第 ${graph.page * GRAPH_PAGE_SIZE + 1}–${graph.page * GRAPH_PAGE_SIZE + graph.nodes.length} 条，共 ${graph.total} 条记忆`;
  const pageOf = $("[data-graph-page-of]", root);
  if (pageOf) pageOf.textContent = `${graph.page + 1} / ${graph.pages}`;

  // (Re)build simulation bodies when the graph content changes, keeping
  // positions and pins of known nodes; reheat and resume the layout.
  // Content-identical rebuilds keep the settled simulation untouched.
  if (state.sim?.sig !== signature) {
    const prev = state.sim?.bodies || new Map();
    const bodies = new Map();
    graph.nodes.forEach((n, i) => {
      const old = prev.get(n.id);
      if (old) {
        bodies.set(n.id, old);
        return;
      }
      const p = spiralPosition(i);
      bodies.set(n.id, { id: n.id, x: p.x, y: p.y, vx: 0, vy: 0, fx: null, fy: null });
    });
    state.sim = { bodies, links: graph.links, cx: 0, cy: 0, sig: signature };
    state.alpha = 1;
    state.view = { tx: null, ty: null, k: 1 };
    state.userMoved = false;
  }
  wake(state);

  function center() {
    return { x: (state.size?.w || 800) / 2, y: (state.size?.h || 480) / 2 };
  }
  function toGraph(clientX, clientY) {
    const rect = svg.getBoundingClientRect();
    const c = center();
    const tx = state.view.tx == null ? c.x : state.view.tx;
    const ty = state.view.ty == null ? c.y : state.view.ty;
    return { x: (clientX - rect.left - tx) / state.view.k, y: (clientY - rect.top - ty) / state.view.k };
  }
  function render() {
    if (!root.isConnected) return;
    const c = center();
    const tx = state.view.tx == null ? c.x : state.view.tx;
    const ty = state.view.ty == null ? c.y : state.view.ty;
    viewport.setAttribute("transform", `translate(${tx},${ty}) scale(${state.view.k})`);
    svg.setAttribute("aria-busy", String((state.alpha || 0) > ALPHA_MIN));
    const near = state.hoverId ? adjacency.get(state.hoverId) : null;
    const dim = (id) => (near && !near.has(id) ? " dimmed" : "");
    $("[data-links]", root).innerHTML = graph.links
      .map(({ source, target }) => {
        const a = state.sim.bodies.get(source);
        const b = state.sim.bodies.get(target);
        if (!a || !b) return "";
        return `<line class="graph-link${near && !near.has(source) && !near.has(target) ? " dimmed" : ""}" x1="${a.x}" y1="${a.y}" x2="${b.x}" y2="${b.y}"/>`;
      })
      .join("");
    $("[data-nodes]", root).innerHTML = graph.nodes
      .map((n) => {
        const p = state.sim.bodies.get(n.id);
        if (!p) return "";
        const outside = graph.external.get(n.id);
        return `<circle class="graph-node${n.id === selected ? " selected" : ""}${dim(n.id)}" data-node="${h(n.id)}" data-kind="${h(n.kind)}"${outside ? ' data-external="true"' : ""} tabindex="0" role="button" aria-label="${h(n.title)}${outside ? `（本页之外 ${outside.length} 条关系）` : ""}" cx="${p.x}" cy="${p.y}" r="${radius(graph, n.id)}"></circle>`;
      })
      .join("");
    const hovered = state.hoverId ? graph.nodes.find((n) => n.id === state.hoverId) : null;
    if (hovered) {
      const p = state.sim.bodies.get(hovered.id);
      if (p) {
        label.textContent = hovered.title;
        label.setAttribute("x", String(p.x));
        label.setAttribute("y", String(p.y - radius(graph, hovered.id) - 7));
        label.hidden = false;
      }
    } else label.hidden = true;
  }
  function zoom(factor, anchor) {
    const rect = svg.getBoundingClientRect();
    const c = center();
    const px = anchor ? anchor.x - rect.left : rect.width / 2;
    const py = anchor ? anchor.y - rect.top : rect.height / 2;
    const tx0 = state.view.tx == null ? c.x : state.view.tx;
    const ty0 = state.view.ty == null ? c.y : state.view.ty;
    const k = Math.min(4, Math.max(0.25, state.view.k * factor));
    state.view = { k, tx: px - (px - tx0) * (k / state.view.k), ty: py - (py - ty0) * (k / state.view.k) };
    state.userMoved = true;
    render();
  }

  state.render = render;
  state.wake = () => wake(state);
  state.centerOnSelected = () => {
    const id = selected || state.selectedId;
    const p = id && state.sim.bodies.get(id);
    if (p) {
      const c = center();
      state.view = { k: state.view.k, tx: c.x - p.x * state.view.k, ty: c.y - p.y * state.view.k };
      state.userMoved = true;
      render();
    }
  };

  function wake(state) {
    if (state.raf || !state.sim) return;
    state.raf = requestAnimationFrame(() => tick(state));
  }
  function tick(state) {
    state.raf = 0;
    if (!state.dom || state.dom.viewport !== viewport || !state.dom.viewport.isConnected)
      return; // view replaced or disposed: stop this loop
    step(state.sim, state.alpha);
    state.alpha = Math.max(0, state.alpha - ALPHA_STEP);
    render();
    if (state.alpha > ALPHA_MIN) {
      state.raf = requestAnimationFrame(() => tick(state));
    } else if (!state.userMoved && state.sim.bodies.size) {
      // Settled: fit the whole graph once, until a pan or zoom takes over.
      const fitted = fitView(state.sim, state.size);
      if (fitted) {
        state.view = fitted;
        render();
      }
    }
  }

  // Pointer interactions: tap a node to open it, drag a node to pin it,
  // drag the background to pan, wheel to zoom around the cursor.
  svg.addEventListener("pointerdown", (event) => {
    if (event.button !== 0 || state.drag) return;
    const id = event.target.getAttribute?.("data-node");
    try {
      svg.setPointerCapture(event.pointerId);
    } catch {
      // A synthetic or already-released pointer cannot be captured; the tap
      // still works because pointerup targets the same element.
    }
    if (id) state.drag = { type: "node", pointerId: event.pointerId, id, sx: event.clientX, sy: event.clientY, moved: false };
    else {
      const c = center();
      state.drag = {
        type: "pan",
        pointerId: event.pointerId,
        sx: event.clientX,
        sy: event.clientY,
        tx0: state.view.tx == null ? c.x : state.view.tx,
        ty0: state.view.ty == null ? c.y : state.view.ty,
      };
    }
  });
  svg.addEventListener("pointermove", (event) => {
    const d = state.drag;
    if (!d || d.pointerId !== event.pointerId) return;
    if (d.type === "node") {
      if (Math.abs(event.clientX - d.sx) + Math.abs(event.clientY - d.sy) > DRAG_THRESHOLD) d.moved = true;
      if (!d.moved) return;
      state.userMoved = true;
      const p = toGraph(event.clientX, event.clientY);
      const b = state.sim.bodies.get(d.id);
      if (!b) return;
      b.fx = p.x;
      b.fy = p.y;
      state.alpha = Math.max(state.alpha, REHEAT_DRAG);
      wake(state);
    } else {
      state.userMoved = true;
      state.view = { k: state.view.k, tx: d.tx0 + (event.clientX - d.sx), ty: d.ty0 + (event.clientY - d.sy) };
      render();
    }
  });
  svg.addEventListener("pointerup", (event) => {
    const d = state.drag;
    if (!d || d.pointerId !== event.pointerId) return;
    state.drag = null;
    if (svg.hasPointerCapture(event.pointerId)) svg.releasePointerCapture(event.pointerId);
    if (d.type === "node" && !d.moved) onSelect(d.id);
    // fx/fy stay: a dragged node keeps its place, Obsidian-style.
  });
  svg.addEventListener("pointercancel", (event) => {
    if (state.drag?.pointerId === event.pointerId) state.drag = null;
  });
  svg.addEventListener("pointerleave", () => {
    state.hoverId = null;
    render();
  });
  svg.addEventListener(
    "wheel",
    (event) => {
      event.preventDefault();
      zoom(Math.exp(-event.deltaY * 0.0016), { x: event.clientX, y: event.clientY });
    },
    { passive: false },
  );
  svg.addEventListener("pointerover", (event) => {
    const id = event.target.getAttribute?.("data-node");
    if (id && id !== state.hoverId) {
      state.hoverId = id;
      state.selectedId = id;
      if (picker) picker.value = graph.external.has(id) ? id : picker.value;
      renderExternal();
      render();
    }
  });
  svg.addEventListener("focusin", (event) => {
    const id = event.target.getAttribute?.("data-node");
    if (id) {
      state.hoverId = id;
      state.selectedId = id;
      renderExternal();
      render();
    }
  });
  svg.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const id = event.target.getAttribute?.("data-node");
    if (id) {
      event.preventDefault();
      onSelect(id);
    }
  });

  function renderExternal() {
    if (!externalBox) return;
    const id = state.selectedId;
    const relations = graph.external.get(id);
    if (!relations) {
      externalBox.hidden = true;
      externalBox.innerHTML = "";
      return;
    }
    const pages = Math.max(1, Math.ceil(relations.length / 50));
    const current = Math.min(state.relationPage, pages - 1);
    externalBox.hidden = false;
    externalBox.innerHTML = `<details open><summary>本页之外 ${relations.length} 条关系：${h(titleOf.get(id) || id)}</summary><ul>${relations
      .slice(current * 50, (current + 1) * 50)
      .map(
        ({ id: other, direction }) =>
          `<li>${direction === "parent" ? "上级" : "下级"}：<button data-open="${h(other)}">${h(titleOf.get(other) || other)}</button></li>`,
      )
      .join("")}</ul>${
      relations.length > 50
        ? `<nav aria-label="关系分页"><button data-rel-page="-1" ${current === 0 ? "disabled" : ""}>上一页</button><span>${current + 1} / ${pages}</span><button data-rel-page="1" ${(current + 1) * 50 >= relations.length ? "disabled" : ""}>下一页</button></nav>`
        : ""
    }</details>`;
  }
  if (picker) {
    picker.addEventListener("change", () => {
      state.selectedId = picker.value || null;
      state.relationPage = 0;
      state.hoverId = null;
      renderExternal();
      render();
    });
  }
  if (externalBox) {
    externalBox.addEventListener("click", (event) => {
      const button = event.target.closest("button");
      if (!button) return;
      if (button.dataset.open) onSelect(button.dataset.open);
      if (button.dataset.relPage) {
        state.relationPage = Math.max(0, state.relationPage + Number(button.dataset.relPage));
        renderExternal();
      }
    });
  }
  renderExternal();
  all("[data-zoom]", root).forEach((button) =>
    button.addEventListener("click", () => zoom(Number(button.dataset.zoom))),
  );
  $("[data-center]", root)?.addEventListener("click", () => state.centerOnSelected());
  all("[data-graph-page]", root).forEach((button) =>
    button.addEventListener("click", () => {
      state.page = graph.page + Number(button.dataset.graphPage);
      state.view = { tx: null, ty: null, k: 1 };
      state.userMoved = false;
      state.hoverId = null;
      state.selectedId = null;
      state.relationPage = 0;
      state.alpha = 1;
      state.drag = null;
      // Recreate the whole view so the page window and sim rebuild cleanly.
      renderGraph(root, records, selected, state, onSelect);
    }),
  );

  const observer = new ResizeObserver((entries) => {
    const rect = entries[0].contentRect;
    const next = state.size && state.size.w === rect.width && state.size.h === rect.height ? state.size : { w: rect.width, h: rect.height };
    state.size = next;
    if (!state.userMoved && (state.alpha || 0) <= ALPHA_MIN) {
      const fitted = fitView(state.sim, state.size);
      if (fitted) {
        state.view = fitted;
        render();
      }
    }
  });
  observer.observe(box);
  requestAnimationFrame(render);
}

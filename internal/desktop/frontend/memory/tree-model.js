// Memory tree model: index the data once and load visible children on demand.
//
// buildIndex constructs the parent-to-child index in O(n), without building a
// complete node tree. childrenOf(index, id) loads only the direct children when
// a node is expanded. This avoids reconstructing the entire tree for each row.
//
// Hierarchy: root, then top-level memories. Trivial (diary) entries stay out of
// the tree; the dedicated diary page groups them by day, matching the console.

export const ROOT_ID = "tree-root";
export const DIARY_ID = "diary-book";
const DAY_PREFIX = "diary-day-";

// Use local dates; slicing UTC created_at would assign early morning entries to
// the previous day.
function localDayOf(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso).slice(0, 10);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

// Diary titles contain the authoritative local date assigned by the CLI.
function activityDayOf(title) {
  return /^活动轨迹 (\d{4}-\d{2}-\d{2})$/.exec(title || "")?.[1] || "";
}

// Built-in taxonomy categories (crates/app/src/taxonomy.rs). Empty ones stay
// out of the tree, matching the dashboard; categories with children still show.
const BUILTIN_CATEGORIES = new Set([
  "家庭亲友", "健康医疗", "财务理财", "饮食烹饪", "住房家居", "出行交通",
  "购物消费", "职业工作", "任务计划", "编程开发", "学习成长", "兴趣娱乐",
  "电子游戏", "运动健身", "社交网络", "旅行游历", "宠物植物", "情感心境",
  "习惯养成", "灵感创意", "技能库", "经验库", "踩坑录",
]);

/** Build parent-to-child and diary-date-to-entry indexes in O(n). */
export function buildIndex(memories = []) {
  const ids = new Set(memories.map((m) => m.id));
  const byParent = new Map(); // parentId (root is '') to memory rows
  const diaryByDay = new Map(); // 'YYYY-MM-DD' to trivial entries
  const contentless = new Set(); // IDs of entries known to have no title or body
  const categoryRoot = new Set(); // Roots whose content is a short category note
  memories.forEach((m) => {
    const row = {
      id: m.id,
      memoryId: m.id,
      title: m.title || String(m.id || "").slice(0, 8),
      kind: m.kind || "context",
      importance: m.importance,
    };
    // The local catalog strips content, so only a present-but-empty body
    // proves the entry is contentless; undefined means "unknown".
    const hasBody = Boolean(
      (m.title || "").trim() ||
        (m.content !== undefined ? (m.content || "").trim() : "known"),
    );
    if (!hasBody) contentless.add(m.id);
    // Recognize category roots by a title-prefixed, single-paragraph note.
    const t = (m.title || "").trim();
    const c = (m.content || "").trim();
    if (t && c && c.startsWith(t + "：") && c.split("\n\n").length <= 2 && c.length < 260)
      categoryRoot.add(m.id);
    if (m.importance === "trivial") {
      const day =
        activityDayOf(m.title) || localDayOf(m.created_at || m.updated_at) || "unknown-date";
      if (!diaryByDay.has(day)) diaryByDay.set(day, []);
      diaryByDay.get(day).push(row);
      return;
    }
    const pid = m.parent_id && ids.has(m.parent_id) ? m.parent_id : "";
    if (!byParent.has(pid)) byParent.set(pid, []);
    byParent.get(pid).push(row);
  });
  return {
    byParent,
    diaryByDay,
    ids,
    contentless,
    categoryRoot,
    days: [...diaryByDay.keys()].sort().reverse(),
  };
}

/** Load direct children on demand, without descendants; kids is the direct child count. */
export function childrenOf(index, nodeId) {
  const { byParent, diaryByDay, days, contentless, categoryRoot } = index;
  // Hide empty leaf entries and category roots that contain only a note.
  // Keep every node that has children.
  // forceIds keeps search matches and their ancestors visible despite filtering.
  const forceIds = index.forceIds || new Set();
  const visible = (r) => {
    if (forceIds.has(r.id)) return true;
    const kids = (byParent.get(r.id) || []).length;
    if (kids > 0) return true;
    if (contentless.has(r.id)) return false;
    if (categoryRoot.has(r.id)) return false;
    if (BUILTIN_CATEGORIES.has(r.title)) return false;
    return true;
  };
  if (!nodeId || nodeId === ROOT_ID) {
    const rows = (byParent.get("") || [])
      .filter(visible)
      .map((r) => ({ ...r, kids: (byParent.get(r.id) || []).length }));
    // Diary entries live on their own page rather than a tree placeholder.
    return rows;
  }
  if (nodeId === DIARY_ID) {
    return days.map((d) => ({
      id: DAY_PREFIX + d,
      kind: "folder",
      title: d,
      kids: (diaryByDay.get(d) || []).length,
    }));
  }
  if (nodeId.startsWith(DAY_PREFIX)) {
    const day = nodeId.slice(DAY_PREFIX.length);
    return (diaryByDay.get(day) || []).map((r) => ({ ...r, kids: 0 }));
  }
  return (byParent.get(nodeId) || [])
    .filter(visible)
    .map((r) => ({ ...r, kids: (byParent.get(r.id) || []).length }));
}

/**
 * Flatten visible rows from the root, descending only into expanded nodes.
 * Each visible level costs O(children); return [{row, depth, kids, open}] for
 * list rendering. allOpen keeps every branch descended while a filter runs.
 */
export function visibleRows(index, expanded, allOpen = false) {
  const out = [];
  const open = (id) => allOpen || expanded.has(id);
  const walk = (parentId, depth) => {
    if (depth > 24) return; // Bound pathological tree depth
    childrenOf(index, parentId).forEach((row) => {
      const hasKids = row.kind === "folder" || row.kids > 0;
      out.push({ row, depth, kids: row.kids || 0, open: open(row.id), hasKids });
      if (hasKids && open(row.id)) walk(row.id, depth + 1);
    });
  };
  walk(ROOT_ID, 0);
  return out;
}

/** Search matches plus their ancestor chain, kept visible during filtering. */
export function searchForceIds(memories, needle) {
  const keep = new Set();
  if (!needle) return keep;
  const q = needle.toLowerCase();
  const byId = new Map(memories.map((m) => [m.id, m]));
  memories.forEach((m) => {
    const hay = [m.title, m.content, m.kind, m.project, (m.tags || []).join(" ")]
      .filter((part) => part !== undefined)
      .join(" ")
      .toLowerCase();
    if (!hay.includes(q)) return;
    keep.add(m.id);
    let cur = m;
    let guard = 0;
    while (cur && cur.parent_id && guard++ < 64) {
      keep.add(cur.parent_id);
      cur = byId.get(cur.parent_id);
    }
  });
  return keep;
}

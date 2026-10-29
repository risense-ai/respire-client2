import { input, select } from "../ui/core.js";
import { kindNames } from "./model.js";

export function filterControls() {
  return `<details class="panel memory-filters"><summary>目录筛选与排序（已载入记忆）</summary><form class="panel-body form-grid" data-filters>${
    select("kind", "类型", [["", "全部类型"], ...Object.entries(kindNames)], "")
  }${select("importance", "存储区域", [["", "全部区域"], ["important", "主区"], ["trivial", "日记"]], "")}${input("project", "项目（精确匹配）")}${input("tag", "标签（精确匹配）")}${select("order", "排序", [["updated", "最近更新"], ["title", "标题"], ["recall", "召回次数"]], "updated")}<button type="reset">清除筛选</button></form></details>`;
}

export function filterRecords(records, values) {
  return records.filter((row) =>
    (!values.kind || String(row.kind).toLowerCase() === values.kind) &&
    (!values.importance || row.importance === values.importance) &&
    (!values.project || row.project === values.project.trim()) &&
    (!values.tag || (row.tags || []).includes(values.tag.trim()))
  ).sort((a, b) => values.order === "title"
    ? String(a.title || "").localeCompare(String(b.title || ""))
    : values.order === "recall"
      ? Number(b.recall_count || 0) - Number(a.recall_count || 0)
      : String(b.updated_at || b.created_at || "").localeCompare(String(a.updated_at || a.created_at || "")));
}

import { apiFetch, can, formatBytes } from "./shell.js";

const pollIntervalMilliseconds = 3000;

const errorElement = document.getElementById("archive-error");
const disabledElement = document.getElementById("archive-disabled");
const sectionsElement = document.getElementById("archive-sections");
const pageElement = sectionsElement.parentElement;
const archiveList = document.getElementById("archive-list");
const archivesEmpty = document.getElementById("archives-empty");
const restoreForm = document.getElementById("restore-form");
const fromInput = document.getElementById("restore-from");
const toInput = document.getElementById("restore-to");
const projectInput = document.getElementById("restore-project");
const domainInput = document.getElementById("restore-domain");
const startButton = document.getElementById("start-restore");
const estimateElement = document.getElementById("restore-estimate");
const restoreErrorElement = document.getElementById("restore-error");
const restoresErrorElement = document.getElementById("restores-error");
const restoreList = document.getElementById("restore-list");
const restoresEmpty = document.getElementById("restores-empty");

let pollTimer;

function setStartEnabled(enabled) {
  if (startButton) {
    startButton.disabled = !enabled;
  }
}

function showError(element, message) {
  element.textContent = message;
  element.hidden = false;
}

function restoreRange() {
  const toExclusive = new Date(toInput.value + "T00:00:00Z");
  toExclusive.setUTCDate(toExclusive.getUTCDate() + 1);
  return {
    from: fromInput.value + "T00:00:00Z",
    to: toExclusive.toISOString(),
    project: projectInput.value.trim(),
    domain: domainInput.value.trim(),
  };
}

function restoreLabel(restore) {
  let label = `${restore.from.slice(0, 10)} to ${new Date(Date.parse(restore.to) - 1).toISOString().slice(0, 10)} UTC`;
  if (restore.project) {
    label += `, project ${restore.project}`;
  }
  if (restore.domain) {
    label += `, domain ${restore.domain}`;
  }
  return label;
}

function renderArchive(archive) {
  const item = document.createElement("li");
  const text = document.createElement("span");
  let summary = `${archive.day} · ${archive.row_count.toLocaleString()} rows`;
  if (archive.partial) {
    summary += " · partial: failures only";
  }
  text.textContent = summary;
  const download = document.createElement("a");
  download.className = "action-button";
  download.href = "/ui/api/archives/" + encodeURIComponent(archive.day) + "/download";
  download.textContent = "Download";
  item.append(text, download);
  return item;
}

function restoreStatusText(restore) {
  if (restore.status === "running") {
    return "restoring…";
  }
  if (restore.status === "ready") {
    return `${restore.row_count.toLocaleString()} rows, ready`;
  }
  if (restore.status === "failed") {
    return `failed: ${restore.error}`;
  }
  return "expired (the restored rows were removed after 7 days)";
}

async function deleteRestore(restore) {
  if (!confirm(`Delete the restore for ${restoreLabel(restore)}? Its restored rows are dropped.`)) {
    return;
  }
  try {
    await apiFetch("/ui/api/restores/" + encodeURIComponent(restore.restore_id), { method: "DELETE", body: {} });
  } catch (error) {
    showError(restoresErrorElement, error.message);
  }
  loadRestores();
}

function renderRestore(restore) {
  const item = document.createElement("li");
  const text = document.createElement("span");
  text.textContent = `${restoreLabel(restore)} · ${restoreStatusText(restore)}`;
  const actions = document.createElement("span");
  if (restore.status === "ready" && can("view_requests")) {
    const browse = document.createElement("a");
    browse.className = "action-button";
    browse.href = "/requests?" + new URLSearchParams({ restore_id: restore.restore_id, from: restore.from, to: restore.to });
    browse.textContent = "Browse";
    actions.append(browse, " ");
  }
  if (can("manage_restores")) {
    const deleteButton = document.createElement("button");
    deleteButton.type = "button";
    deleteButton.className = "action-button";
    deleteButton.textContent = "Delete";
    deleteButton.addEventListener("click", () => deleteRestore(restore));
    actions.append(deleteButton);
  }
  item.append(text, actions);
  return item;
}

async function loadRestores() {
  clearTimeout(pollTimer);
  restoresErrorElement.hidden = true;
  let result;
  try {
    result = await apiFetch("/ui/api/restores");
  } catch (error) {
    showError(restoresErrorElement, error.message);
    return;
  }
  const visibleRestores = result.restores.filter((restore) => restore.status !== "deleted");
  restoreList.replaceChildren(...visibleRestores.map(renderRestore));
  restoresEmpty.hidden = visibleRestores.length > 0;
  if (visibleRestores.some((restore) => restore.status === "running")) {
    pollTimer = setTimeout(loadRestores, pollIntervalMilliseconds);
  }
}

function estimateText(estimate) {
  let text = `About ${estimate.estimated_rows.toLocaleString()} rows (${formatBytes(estimate.estimated_bytes)}) from ${estimate.archived_days.length} archived days. ${formatBytes(estimate.used_bytes)} of ${formatBytes(estimate.budget_bytes)} disk budget in use.`;
  if (estimate.excluded_days.length > 0) {
    text += ` Not archived, so excluded: ${estimate.excluded_days.join(", ")}.`;
  }
  if (!estimate.allowed) {
    text += ` Can't restore: ${estimate.reason}.`;
  }
  return text;
}

if (restoreForm) {
  restoreForm.addEventListener("input", () => {
    setStartEnabled(false);
    estimateElement.hidden = true;
    restoreErrorElement.hidden = true;
  });

  restoreForm.addEventListener("submit", async (submitEvent) => {
    submitEvent.preventDefault();
    setStartEnabled(false);
    restoreErrorElement.hidden = true;
    let estimate;
    try {
      estimate = await apiFetch("/ui/api/restores/estimate?" + new URLSearchParams(restoreRange()));
    } catch (error) {
      showError(restoreErrorElement, error.message);
      return;
    }
    estimateElement.textContent = estimateText(estimate);
    estimateElement.hidden = false;
    setStartEnabled(estimate.allowed);
  });

  if (startButton) {
    startButton.addEventListener("click", async () => {
      setStartEnabled(false);
      restoreErrorElement.hidden = true;
      try {
        await apiFetch("/ui/api/restores", { method: "POST", body: restoreRange() });
        estimateElement.hidden = true;
      } catch (error) {
        showError(restoreErrorElement, error.message);
      }
      loadRestores();
    });
  }
}

async function load() {
  let archives;
  if (!archiveList) {
    sectionsElement.hidden = false;
    if (restoreList) {
      loadRestores();
    }
    return;
  }
  pageElement.setAttribute("aria-busy", "true");
  try {
    archives = await apiFetch("/ui/api/archives");
  } catch (error) {
    showError(errorElement, error.message);
    pageElement.removeAttribute("aria-busy");
    return;
  }
  pageElement.removeAttribute("aria-busy");
  if (!archives.archiving_enabled) {
    disabledElement.hidden = false;
    return;
  }
  sectionsElement.hidden = false;
  archiveList.replaceChildren(...archives.archives.map(renderArchive));
  archivesEmpty.hidden = archives.archives.length > 0;
  loadRestores();
}

load();

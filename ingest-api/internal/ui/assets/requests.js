import { apiFetch, networkErrorMessage } from "./shell.js";
import { addQueryTerm, attachFacetAutocomplete, canFilter, formatQueryTerm } from "./requests-query.js";
import { renderTape } from "./requests-tape.js";
import { showRequestDetail } from "./requests-detail.js";
import { attachPullToRefresh } from "./gestures.js";

const presetSeconds = { "15m": 900, "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "14d": 1209600, "30d": 2592000, "60d": 5184000 };
const defaultPreset = "1h";
const pathTailLength = 24;

const queryForm = document.getElementById("query-form");
const exportMenu = document.querySelector(".export-menu");
const queryInput = document.getElementById("query-input");
const sourceSelect = document.getElementById("source-select");
const rangeSelect = document.getElementById("range-select");
const customOption = rangeSelect.querySelector('option[value="custom"]');
const queryError = document.getElementById("query-error");
const tapeChart = document.getElementById("tape-chart");
const tapeAxis = document.getElementById("tape-axis");
const tapeMessage = document.getElementById("tape-message");
const retentionNote = document.getElementById("retention-note");
const explorerBody = document.getElementById("explorer-body");
const resultsRows = document.getElementById("results-rows");
const resultsEmpty = document.getElementById("results-empty");
const resultsSection = resultsRows.closest("section");
const tapeSection = tapeChart.parentElement;
const loadOlderButton = document.getElementById("load-older");
const detailPane = document.getElementById("detail-pane");
const querySuggestions = document.getElementById("query-suggestions");

let queryText = "";
let restoreId = "";
let restoresById = new Map();
let restoresLoaded = false;
let rangeSelection = { preset: defaultPreset };
let resolvedRange = null;
let nextCursor = null;
let rows = [];
let searchSequence = 0;
let selectedRowElement = null;

const timeFormatter = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

function readUrlState() {
  const urlParameters = new URLSearchParams(location.search);
  queryText = urlParameters.get("q") || "";
  restoreId = urlParameters.get("restore_id") || "";
  const fromDate = new Date(urlParameters.get("from"));
  const toDate = new Date(urlParameters.get("to"));
  const requestedPreset = urlParameters.get("range");
  if (urlParameters.has("from") && urlParameters.has("to") && !Number.isNaN(fromDate.getTime()) && !Number.isNaN(toDate.getTime()) && fromDate < toDate) {
    rangeSelection = { from: fromDate, to: toDate };
  } else if (Object.hasOwn(presetSeconds, requestedPreset)) {
    rangeSelection = { preset: requestedPreset };
  } else {
    rangeSelection = { preset: defaultPreset };
  }
  queryInput.value = queryText;
  syncRangeSelect();
  syncSourceSelect();
}

function sourceParameters() {
  return restoreId ? { restore_id: restoreId } : {};
}

function restoreLabel(restore) {
  let label = `Restore: ${new Date(restore.from).toLocaleString()} – ${new Date(restore.to).toLocaleString()}`;
  if (restore.project) {
    label += `, ${restore.project}`;
  }
  if (restore.domain) {
    label += `, ${restore.domain}`;
  }
  return label;
}

function syncSourceSelect() {
  if (restoreId && !Array.from(sourceSelect.options).some((option) => option.value === restoreId)) {
    const restoreOption = document.createElement("option");
    restoreOption.value = restoreId;
    const knownRestore = restoresById.get(restoreId);
    restoreOption.textContent = knownRestore ? restoreLabel(knownRestore) : restoresLoaded ? "Restore (not available)" : "Restore";
    sourceSelect.append(restoreOption);
  }
  sourceSelect.value = restoreId;
  sourceSelect.hidden = sourceSelect.options.length <= 1;
}

async function loadRestores() {
  let restoreList;
  try {
    restoreList = (await apiFetch("/ui/api/restores")).restores;
  } catch {
    return;
  }
  restoresLoaded = true;
  restoresById = new Map(restoreList.filter((restore) => restore.status === "ready").map((restore) => [restore.restore_id, restore]));
  sourceSelect.replaceChildren(new Option("Live", ""));
  for (const restore of restoresById.values()) {
    sourceSelect.append(new Option(restoreLabel(restore), restore.restore_id));
  }
  syncSourceSelect();
}

function syncRangeSelect() {
  if (rangeSelection.preset) {
    customOption.hidden = true;
    rangeSelect.value = rangeSelection.preset;
    return;
  }
  customOption.textContent = `Custom range: ${rangeSelection.from.toLocaleString()} – ${rangeSelection.to.toLocaleString()}`;
  customOption.hidden = false;
  rangeSelect.value = "custom";
}

function writeUrlState(push) {
  const urlParameters = new URLSearchParams();
  if (queryText) {
    urlParameters.set("q", queryText);
  }
  if (restoreId) {
    urlParameters.set("restore_id", restoreId);
  }
  if (rangeSelection.preset) {
    urlParameters.set("range", rangeSelection.preset);
  } else {
    urlParameters.set("from", rangeSelection.from.toISOString());
    urlParameters.set("to", rangeSelection.to.toISOString());
  }
  const newSearch = "?" + urlParameters.toString();
  if (push && newSearch !== location.search) {
    history.pushState(null, "", location.pathname + newSearch);
  } else {
    history.replaceState(null, "", location.pathname + newSearch);
  }
}

function resolveRange() {
  if (rangeSelection.preset) {
    const toDate = new Date();
    return { from: new Date(toDate.getTime() - presetSeconds[rangeSelection.preset] * 1000), to: toDate };
  }
  return { from: rangeSelection.from, to: rangeSelection.to };
}

function rangeParameters() {
  return { from: resolvedRange.from.toISOString(), to: resolvedRange.to.toISOString() };
}

function showQueryError(message) {
  queryError.textContent = message;
  queryError.hidden = false;
}

function updateResultsControls() {
  resultsEmpty.hidden = rows.length > 0;
  loadOlderButton.hidden = nextCursor === null;
  loadOlderButton.disabled = nextCursor === null;
}

function createFilterCell(fieldName, value, content) {
  const cell = document.createElement("td");
  if (canFilter(fieldName, value)) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "filter-value";
    button.dataset.termField = fieldName;
    button.dataset.termValue = value;
    if (content) {
      button.append(content, value);
    } else {
      button.textContent = value;
    }
    cell.append(button);
  } else {
    cell.append(content || "", value);
  }
  return cell;
}

function createPathCell(requestUrl) {
  let displayPath = requestUrl;
  try {
    const parsedUrl = new URL(requestUrl);
    displayPath = parsedUrl.pathname + parsedUrl.search;
  } catch {}
  const cell = document.createElement("td");
  const path = document.createElement("span");
  path.className = "path";
  path.title = requestUrl;
  const splitIndex = Math.max(displayPath.length - pathTailLength, 0);
  const head = document.createElement("span");
  head.className = "path-head";
  head.textContent = displayPath.slice(0, splitIndex);
  const tail = document.createElement("span");
  tail.className = "path-tail";
  tail.textContent = displayPath.slice(splitIndex);
  path.append(head, tail);
  cell.append(path);
  return cell;
}

function appendRow(summary) {
  const rowElement = document.createElement("tr");
  rowElement.dataset.rowIndex = String(rows.length);
  rows.push(summary);

  const timeCell = document.createElement("td");
  const openButton = document.createElement("button");
  openButton.type = "button";
  openButton.className = "row-open";
  const timeText = timeFormatter.format(new Date(summary.ts));
  openButton.textContent = timeText;
  openButton.title = summary.ts;
  openButton.setAttribute("aria-label", `Open request ${timeText} ${summary.method} ${summary.domain}`);
  timeCell.append(openButton);

  const swatch = document.createElement("span");
  swatch.className = summary.status === 0 ? "status-swatch status-0" : `status-swatch status-${Math.floor(summary.status / 100)}xx`;

  rowElement.append(
    timeCell,
    createFilterCell("status", String(summary.status), swatch),
    createFilterCell("method", summary.method),
    createFilterCell("domain", summary.domain),
    createPathCell(summary.url),
    createFilterCell("scraper", summary.scraper),
    createFilterCell("proxy", summary.proxy),
  );
  resultsRows.append(rowElement);
}

function clearRows() {
  rows = [];
  selectedRowElement = null;
  resultsRows.replaceChildren();
}

function resetDetailSelection() {
  detailPane.hidden = true;
  detailPane.replaceChildren();
  explorerBody.classList.remove("pane-open");
  selectedRowElement = null;
}

function closeDetail() {
  const closedRowElement = selectedRowElement;
  resetDetailSelection();
  if (closedRowElement) {
    closedRowElement.removeAttribute("aria-selected");
    closedRowElement.classList.remove("selected");
    closedRowElement.querySelector(".row-open").focus();
  }
}

function applyTerm(term) {
  queryText = addQueryTerm(queryText, term);
  queryInput.value = queryText;
  runSearch({ push: true });
}

function applyRange(fromDate, toDate) {
  rangeSelection = { from: fromDate, to: toDate };
  syncRangeSelect();
  runSearch({ push: true });
}

async function runSearch({ push }) {
  resolvedRange = resolveRange();
  writeUrlState(push);
  queryError.hidden = true;
  tapeMessage.hidden = true;
  retentionNote.hidden = true;
  searchSequence += 1;
  const thisSearch = searchSequence;
  resultsSection.setAttribute("aria-busy", "true");
  tapeSection.setAttribute("aria-busy", "true");
  loadOlderButton.disabled = true;
  resetDetailSelection();

  const parameters = { q: queryText, ...rangeParameters(), ...sourceParameters() };
  const [searchOutcome, tapeOutcome] = await Promise.allSettled([
    apiFetch("/ui/api/requests?" + new URLSearchParams(parameters)),
    apiFetch("/ui/api/tape?" + new URLSearchParams(parameters)),
  ]);
  if (thisSearch !== searchSequence) {
    return;
  }
  resultsSection.removeAttribute("aria-busy");
  tapeSection.removeAttribute("aria-busy");

  clearRows();
  if (searchOutcome.status === "rejected") {
    showQueryError(searchOutcome.reason.message);
    nextCursor = null;
    resultsEmpty.hidden = true;
    loadOlderButton.hidden = true;
    loadOlderButton.disabled = true;
  } else {
    for (const summary of searchOutcome.value.rows) {
      appendRow(summary);
    }
    nextCursor = searchOutcome.value.next_cursor;
    if (searchOutcome.value.note) {
      retentionNote.textContent = searchOutcome.value.note;
      retentionNote.hidden = false;
    }
    updateResultsControls();
  }

  if (tapeOutcome.status === "rejected") {
    tapeChart.replaceChildren();
    tapeAxis.replaceChildren();
    if (searchOutcome.status === "fulfilled") {
      tapeMessage.textContent = tapeOutcome.reason.message;
      tapeMessage.hidden = false;
    }
    return;
  }
  renderTape(tapeChart, tapeAxis, tapeOutcome.value, resolvedRange, {
    onRangeSelected: applyRange,
    onStatusClassSelected: (statusClass, excluded) => applyTerm(formatQueryTerm("status", statusClass, excluded)),
  });
  if (tapeOutcome.value.approximate) {
    tapeMessage.textContent = "Past 14 days the tape uses hourly totals, which only apply project, scraper, domain, proxy, tls and status filters.";
    tapeMessage.hidden = false;
  }
}

async function loadOlder() {
  loadOlderButton.disabled = true;
  const thisSearch = searchSequence;
  try {
    const page = await apiFetch("/ui/api/requests?" + new URLSearchParams({ q: queryText, ...rangeParameters(), ...sourceParameters(), cursor: nextCursor }));
    if (thisSearch !== searchSequence) {
      return;
    }
    for (const summary of page.rows) {
      appendRow(summary);
    }
    nextCursor = page.next_cursor;
    updateResultsControls();
  } catch (error) {
    if (thisSearch === searchSequence) {
      showQueryError(error.message);
    }
  } finally {
    if (thisSearch === searchSequence) {
      loadOlderButton.disabled = nextCursor === null;
    }
  }
}

function openRow(rowElement) {
  if (selectedRowElement) {
    selectedRowElement.removeAttribute("aria-selected");
    selectedRowElement.classList.remove("selected");
  }
  selectedRowElement = rowElement;
  rowElement.setAttribute("aria-selected", "true");
  rowElement.classList.add("selected");
  showRequestDetail(detailPane, rows[Number(rowElement.dataset.rowIndex)], sourceParameters(), {
    onFilter: applyTerm,
    onFindSimilar: (terms) => {
      queryText = terms.reduce(addQueryTerm, queryText);
      queryInput.value = queryText;
      runSearch({ push: true });
    },
    onClose: closeDetail,
  });
}

resultsRows.addEventListener("click", (clickEvent) => {
  const filterButton = clickEvent.target.closest(".filter-value");
  if (filterButton) {
    applyTerm(formatQueryTerm(filterButton.dataset.termField, filterButton.dataset.termValue, clickEvent.shiftKey));
    return;
  }
  const rowElement = clickEvent.target.closest("tr");
  if (rowElement) {
    openRow(rowElement);
  }
});

queryForm.addEventListener("submit", (submitEvent) => {
  submitEvent.preventDefault();
  queryText = queryInput.value.trim();
  runSearch({ push: true });
});

rangeSelect.addEventListener("change", () => {
  if (Object.hasOwn(presetSeconds, rangeSelect.value)) {
    rangeSelection = { preset: rangeSelect.value };
    syncRangeSelect();
    queryText = queryInput.value.trim();
    runSearch({ push: true });
  }
});

sourceSelect.addEventListener("change", () => {
  restoreId = sourceSelect.value;
  const chosenRestore = restoresById.get(restoreId);
  rangeSelection = chosenRestore ? { from: new Date(chosenRestore.from), to: new Date(chosenRestore.to) } : { preset: defaultPreset };
  syncRangeSelect();
  queryText = queryInput.value.trim();
  runSearch({ push: true });
});

async function downloadExport(formatName) {
  const exportParameters = new URLSearchParams({ ...rangeParameters(), ...sourceParameters(), format: formatName });
  if (queryText) {
    exportParameters.set("q", queryText);
  }
  const exportButtons = exportMenu.querySelectorAll("[data-export-format]");
  exportMenu.open = false;
  queryError.hidden = true;
  exportButtons.forEach((exportButton) => {
    exportButton.disabled = true;
  });
  try {
    let exportResponse;
    try {
      exportResponse = await fetch("/ui/api/requests/export?" + exportParameters);
    } catch {
      showQueryError(networkErrorMessage);
      return;
    }
    if (exportResponse.status === 401) {
      location.assign("/login?next=" + encodeURIComponent(location.pathname + location.search));
      return;
    }
    if (!exportResponse.ok) {
      let errorMessage = "";
      try {
        const errorBody = await exportResponse.json();
        errorMessage = errorBody && errorBody.error;
      } catch {}
      showQueryError(errorMessage || `Request failed with status ${exportResponse.status}.`);
      return;
    }
    let fileBlob;
    try {
      fileBlob = await exportResponse.blob();
    } catch {
      showQueryError("The export stopped before it finished. Try a narrower time range or query.");
      return;
    }
    const fileNameMatch = /filename="([^"]+)"/.exec(exportResponse.headers.get("Content-Disposition") || "");
    const objectUrl = URL.createObjectURL(fileBlob);
    const downloadLink = document.createElement("a");
    downloadLink.href = objectUrl;
    downloadLink.download = fileNameMatch ? fileNameMatch[1] : "scrapelog-requests." + formatName;
    downloadLink.click();
    URL.revokeObjectURL(objectUrl);
  } finally {
    exportButtons.forEach((exportButton) => {
      exportButton.disabled = false;
    });
  }
}

exportMenu.addEventListener("click", (clickEvent) => {
  const formatName = clickEvent.target.closest("[data-export-format]")?.dataset.exportFormat;
  if (formatName) {
    downloadExport(formatName);
  }
});

loadOlderButton.addEventListener("click", loadOlder);

window.addEventListener("popstate", () => {
  readUrlState();
  runSearch({ push: false });
});

attachFacetAutocomplete(queryInput, querySuggestions, () => resolvedRange || resolveRange(), sourceParameters);

readUrlState();
loadRestores();
attachPullToRefresh(() => runSearch({ push: false }), [tapeSection, detailPane, querySuggestions]);
runSearch({ push: false });

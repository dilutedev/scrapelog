import { apiFetch, formatBytes } from "./shell.js";
import { canFilter, formatQueryTerm } from "./requests-query.js";

const bodyDisplayCharacterLimit = 262144;
const bodyDisplayLineLimit = 5000;

let activeCallbacks = null;
let activeRequestToken = "";

function decodeRequestBody(detail) {
  if (detail.request_body_encoding !== "base64") {
    return { text: detail.request_body, binary: false };
  }
  try {
    const binaryString = atob(detail.request_body);
    const bytes = Uint8Array.from(binaryString, (character) => character.charCodeAt(0));
    return { text: new TextDecoder("utf-8", { fatal: true }).decode(bytes), binary: false };
  } catch {
    return { text: detail.request_body, binary: true };
  }
}

function shellQuote(text) {
  return "'" + text.replaceAll("'", "'\\''") + "'";
}

export function buildCurlCommand(detail) {
  const curlParts = [`curl -X ${shellQuote(detail.method)} ${shellQuote(detail.url)}`];
  const headerNames = Object.keys(detail.request_headers || {}).sort();
  for (const headerName of headerNames) {
    if (headerName.toLowerCase() === "content-length") {
      continue;
    }
    curlParts.push(`-H ${shellQuote(headerName + ": " + detail.request_headers[headerName])}`);
  }
  if (!detail.request_body) {
    return curlParts.join(" \\\n  ");
  }
  const decodedBody = decodeRequestBody(detail);
  if (!decodedBody.binary) {
    curlParts.push(`--data-raw ${shellQuote(decodedBody.text)}`);
    return curlParts.join(" \\\n  ");
  }
  curlParts.push("--data-binary @-");
  return `printf '%s' ${shellQuote(detail.request_body)} | base64 -d | ` + curlParts.join(" \\\n  ");
}

function createElementWithText(tagName, text, className) {
  const element = document.createElement(tagName);
  element.textContent = text;
  if (className) {
    element.className = className;
  }
  return element;
}

function createButton(label, className) {
  const button = createElementWithText("button", label, className);
  button.type = "button";
  return button;
}

function createValueNode(fieldName, value) {
  if (!canFilter(fieldName, value)) {
    return createElementWithText("span", value);
  }
  const button = createButton(value, "filter-value");
  button.dataset.termField = fieldName;
  button.dataset.termValue = value;
  return button;
}

function createStatusValue(status) {
  const wrapper = document.createElement("span");
  const swatch = document.createElement("span");
  swatch.className = status === 0 ? "status-swatch status-0" : `status-swatch status-${Math.floor(status / 100)}xx`;
  wrapper.append(swatch, createValueNode("status", String(status)));
  return wrapper;
}

function createKeyValueTable(entries) {
  const table = document.createElement("table");
  table.className = "kv-table";
  const tableBody = document.createElement("tbody");
  for (const [label, valueNode] of entries) {
    const row = document.createElement("tr");
    const labelCell = createElementWithText("th", label);
    labelCell.scope = "row";
    const valueCell = document.createElement("td");
    valueCell.append(valueNode);
    row.append(labelCell, valueCell);
    tableBody.append(row);
  }
  table.append(tableBody);
  return table;
}

function createMapSection(title, valuesByName, fieldPrefix) {
  const section = document.createElement("section");
  section.append(createElementWithText("h3", title));
  const names = Object.keys(valuesByName || {}).sort();
  if (names.length === 0) {
    section.append(createElementWithText("p", "None", "note"));
    return section;
  }
  section.append(createKeyValueTable(names.map((name) => [name, createValueNode(fieldPrefix + name, valuesByName[name])])));
  return section;
}

function renderBody(container, text) {
  let displayText = text;
  const trimmedText = text.trim();
  if (trimmedText.startsWith("{") || trimmedText.startsWith("[")) {
    try {
      displayText = JSON.stringify(JSON.parse(trimmedText), null, 2);
    } catch {}
  }
  const cutText = displayText.slice(0, bodyDisplayCharacterLimit);
  const allLines = cutText.split("\n");
  const keptLines = allLines.slice(0, bodyDisplayLineLimit);
  const lineList = document.createElement("ol");
  lineList.className = "body-lines";
  for (const line of keptLines) {
    lineList.append(createElementWithText("li", line));
  }
  container.replaceChildren(lineList);
  if (cutText.length < displayText.length || keptLines.length < allLines.length) {
    const originalByteLength = new TextEncoder().encode(text).length;
    container.append(createElementWithText("p", `Showing the first ${keptLines.length} lines of a ${formatBytes(originalByteLength)} body.`, "note"));
  }
}

function attachPaneListeners(paneElement) {
  paneElement.dataset.listenersAttached = "true";
  paneElement.addEventListener("click", (clickEvent) => {
    const filterButton = clickEvent.target.closest(".filter-value");
    if (filterButton) {
      activeCallbacks.onFilter(formatQueryTerm(filterButton.dataset.termField, filterButton.dataset.termValue, clickEvent.shiftKey));
    }
  });
  paneElement.addEventListener("keydown", (keyEvent) => {
    if (keyEvent.key === "Escape") {
      activeCallbacks.onClose();
    }
  });
}

export function showRequestDetail(paneElement, rowSummary, sourceParameters, callbacks) {
  const keyParameters = new URLSearchParams({ project: rowSummary.project, domain: rowSummary.domain, ts: rowSummary.ts, request_id: rowSummary.request_id, ...sourceParameters });
  const requestToken = keyParameters.toString();
  activeCallbacks = callbacks;
  activeRequestToken = requestToken;
  if (!paneElement.dataset.listenersAttached) {
    attachPaneListeners(paneElement);
  }
  paneElement.replaceChildren(createElementWithText("p", "Loading request…"));
  paneElement.hidden = false;
  document.getElementById("explorer-body").classList.add("pane-open");

  const isCurrent = () => activeRequestToken === requestToken;
  apiFetch("/ui/api/request?" + keyParameters).then(
    (detail) => {
      if (isCurrent()) {
        renderDetail(paneElement, detail, keyParameters, isCurrent, callbacks);
      }
    },
    (error) => {
      if (isCurrent()) {
        paneElement.replaceChildren(createElementWithText("p", error.message, "query-error"));
      }
    },
  );
}

function renderDetail(paneElement, detail, keyParameters, isCurrent, callbacks) {
  const headerRow = document.createElement("div");
  headerRow.className = "detail-header";
  const backButton = createButton("Back to results", "back-button");
  backButton.addEventListener("click", () => callbacks.onClose());
  const closeButton = createButton("Close", "close-button");
  closeButton.addEventListener("click", () => callbacks.onClose());
  headerRow.append(backButton, closeButton);

  const heading = createElementWithText("h2", `${detail.method} ${detail.url}`, "detail-title");
  heading.tabIndex = -1;

  const outcome = document.createElement("dl");
  outcome.className = "outcome";
  const outcomeEntries = [
    ["Status", createStatusValue(detail.status)],
    ["Latency", document.createTextNode(`${detail.latency_ms} ms`)],
    ["Response size", document.createTextNode(formatBytes(detail.response_size))],
  ];
  if (detail.error) {
    outcomeEntries.push(["Error", document.createTextNode(detail.error)]);
  }
  for (const [label, valueNode] of outcomeEntries) {
    const group = document.createElement("div");
    const description = document.createElement("dd");
    description.append(valueNode);
    group.append(createElementWithText("dt", label), description);
    outcome.append(group);
  }

  const timeNode = createElementWithText("span", new Date(detail.ts).toLocaleString());
  timeNode.title = detail.ts;
  const requestSection = document.createElement("section");
  requestSection.append(createElementWithText("h3", "Request"));
  requestSection.append(createKeyValueTable([
    ["Time", timeNode],
    ["Project", createValueNode("project", detail.project)],
    ["Scraper", createValueNode("scraper", detail.scraper)],
    ["Proxy", createValueNode("proxy", detail.proxy)],
    ["Request ID", createValueNode("id", detail.request_id)],
    ["HTTP version", createValueNode("http", detail.http_version)],
    ["TLS profile", createValueNode("tls", detail.tls_profile)],
    ["JA3", createValueNode("ja3", detail.tls_ja3)],
    ["JA4", createValueNode("ja4", detail.tls_ja4)],
  ]));

  const sections = [
    headerRow,
    heading,
    outcome,
    requestSection,
    createMapSection("Response headers", detail.response_headers, "res."),
    createMapSection("Request headers", detail.request_headers, "req."),
    createMapSection("Tags", detail.tags, "tag."),
  ];

  if (detail.request_body) {
    const requestBodySection = document.createElement("section");
    requestBodySection.append(createElementWithText("h3", "Request body"));
    const decodedBody = decodeRequestBody(detail);
    if (decodedBody.binary) {
      const byteLength = Math.floor((detail.request_body.length * 3) / 4);
      requestBodySection.append(createElementWithText("p", `Binary request body, ${formatBytes(byteLength)} (base64)`, "note"));
    }
    const requestBodyViewer = document.createElement("div");
    renderBody(requestBodyViewer, decodedBody.text);
    requestBodySection.append(requestBodyViewer);
    sections.push(requestBodySection);
  }

  const responseBodySection = document.createElement("section");
  responseBodySection.append(createElementWithText("h3", "Response body"));
  const responseBodyViewer = document.createElement("div");
  responseBodySection.append(responseBodyViewer);
  sections.push(responseBodySection);

  const actions = document.createElement("div");
  actions.className = "detail-actions";
  const curlButton = createButton("Copy as curl", "action-button");
  const copyStatus = document.createElement("span");
  copyStatus.setAttribute("role", "status");
  const curlFallback = document.createElement("textarea");
  curlFallback.readOnly = true;
  curlFallback.hidden = true;
  curlFallback.className = "curl-fallback";
  curlButton.addEventListener("click", async () => {
    const curlCommand = buildCurlCommand(detail);
    try {
      await navigator.clipboard.writeText(curlCommand);
      copyStatus.textContent = "Copied";
    } catch {
      curlFallback.value = curlCommand;
      curlFallback.hidden = false;
      curlFallback.select();
      copyStatus.textContent = "Press Ctrl+C to copy.";
    }
  });
  const similarButton = createButton("Find similar", "action-button");
  similarButton.addEventListener("click", () => {
    const candidateTerms = [["domain", detail.domain], ["status", String(detail.status)], ["tls", detail.tls_profile]];
    const terms = candidateTerms
      .filter(([fieldName, value]) => canFilter(fieldName, value))
      .map(([fieldName, value]) => formatQueryTerm(fieldName, value, false));
    callbacks.onFindSimilar(terms);
  });
  actions.append(curlButton, similarButton, copyStatus);
  sections.push(actions, curlFallback);

  paneElement.replaceChildren(...sections);
  heading.focus();

  if (!detail.has_body) {
    responseBodyViewer.append(createElementWithText("p", "No response body is stored for this request.", "note"));
    return;
  }
  responseBodyViewer.append(createElementWithText("p", "Loading body…", "note"));
  apiFetch("/ui/api/request/body?" + keyParameters).then(
    (bodyText) => {
      if (isCurrent()) {
        renderBody(responseBodyViewer, bodyText);
      }
    },
    (error) => {
      if (isCurrent()) {
        responseBodyViewer.replaceChildren(createElementWithText("p", error.message, "note"));
      }
    },
  );
}

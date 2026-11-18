import { apiFetch, formatBytes } from "./shell.js";
import uPlot from "./vendor/uplot/uPlot.esm.js";

const presetSeconds = { "24h": 86400, "7d": 604800, "30d": 2592000, "90d": 7776000 };
const presetLabels = { "24h": "24 hours", "7d": "7 days", "30d": "30 days", "90d": "90 days" };
const seriesColors = ["#4C6A85", "#8A6F4D", "#5E8C87", "#9A5E7A", "#6B7A3A", "#B07C6A", "#5D5A8C", "#7F8C99"];
const otherSeriesColor = "#868D87";
const splitLabels = { proxy: "Proxy", project: "Project", scraper: "Scraper", domain: "Domain" };
const stepValues = ["hour", "day"];
const defaultState = { split: "proxy", project: "", range: "7d", step: "hour" };
const axisStroke = "#5B6770";
const gridStroke = "#DCE1DB";
const desktopChartHeight = 320;
const mobileChartHeight = 220;
const desktopYAxisSize = 80;
const mobileYAxisSize = 72;

const splitSelect = document.getElementById("split-select");
const projectSelect = document.getElementById("project-select");
const rangeSelect = document.getElementById("range-select");
const stepSelect = document.getElementById("step-select");
const headline = document.getElementById("bandwidth-headline");
const errorMessage = document.getElementById("bandwidth-error");
const chartContainer = document.getElementById("bandwidth-chart");
const emptyMessage = document.getElementById("bandwidth-empty");
const filterNote = document.getElementById("chart-filter-note");
const groupColumnHeader = document.getElementById("group-column-header");
const tableBody = document.getElementById("bandwidth-rows");

let state = { ...defaultState };
let loadSequence = 0;
let facetSequence = 0;
let loadedResponse = null;
let filteredGroupIndex = null;
let chart = null;

function readUrlState() {
  const urlParameters = new URLSearchParams(location.search);
  const requestedSplit = urlParameters.get("split");
  const requestedRange = urlParameters.get("range");
  const requestedStep = urlParameters.get("step");
  state = {
    split: Object.hasOwn(splitLabels, requestedSplit) ? requestedSplit : defaultState.split,
    project: urlParameters.get("project") || "",
    range: Object.hasOwn(presetSeconds, requestedRange) ? requestedRange : defaultState.range,
    step: stepValues.includes(requestedStep) ? requestedStep : defaultState.step,
  };
  splitSelect.value = state.split;
  rangeSelect.value = state.range;
  stepSelect.value = state.step;
}

function writeUrlState() {
  const urlParameters = new URLSearchParams();
  for (const [name, value] of Object.entries(state)) {
    if (value !== defaultState[name]) {
      urlParameters.set(name, value);
    }
  }
  const queryString = urlParameters.toString();
  history.replaceState(null, "", location.pathname + (queryString ? "?" + queryString : ""));
}

function displayKey(key) {
  return key === "" ? "(none)" : key;
}

function colorForGroup(groupIndex) {
  return groupIndex >= seriesColors.length ? otherSeriesColor : seriesColors[groupIndex];
}

function destroyChart() {
  if (chart) {
    chart.destroy();
    chart = null;
  }
}

function chartHeight() {
  return matchMedia("(max-width: 759px)").matches ? mobileChartHeight : desktopChartHeight;
}

function yAxisSize() {
  return matchMedia("(max-width: 759px)").matches ? mobileYAxisSize : desktopYAxisSize;
}

function renderChart() {
  destroyChart();
  const { series, groups } = loadedResponse;
  if (groups.length === 0) {
    chartContainer.hidden = true;
    emptyMessage.hidden = false;
    return;
  }
  chartContainer.hidden = false;
  emptyMessage.hidden = true;
  const timestamps = series[0].points.map((point) => point[0]);
  const visibleIndexes = filteredGroupIndex === null ? groups.map((group, groupIndex) => groupIndex) : [filteredGroupIndex];
  const cumulative = new Array(timestamps.length).fill(0);
  const plotted = visibleIndexes.map((groupIndex) => {
    const ownBytes = series[groupIndex].points.map((point) => point[1]);
    const values = filteredGroupIndex === null ? ownBytes.map((bytes, pointIndex) => (cumulative[pointIndex] += bytes)) : ownBytes;
    return { groupIndex, ownBytes, values };
  });
  plotted.reverse();
  const seriesOptions = [{}].concat(
    plotted.map(({ groupIndex, ownBytes }) => {
      const color = colorForGroup(groupIndex);
      return {
        label: displayKey(groups[groupIndex].key),
        stroke: color,
        fill: color,
        width: 1,
        points: { show: false },
        value: (plotInstance, rawValue, seriesIndex, pointIndex) => (pointIndex === null ? "--" : formatBytes(ownBytes[pointIndex])),
      };
    }),
  );
  chart = new uPlot(
    {
      width: chartContainer.clientWidth,
      height: chartHeight(),
      cursor: { drag: { x: false, y: false } },
      scales: { y: { range: (plotInstance, minimum, maximum) => [0, maximum > 0 ? maximum * 1.05 : 1] } },
      axes: [
        { stroke: axisStroke, grid: { stroke: gridStroke, width: 1 }, ticks: { stroke: gridStroke } },
        { stroke: axisStroke, size: yAxisSize(), grid: { stroke: gridStroke, width: 1 }, ticks: { stroke: gridStroke }, values: (plotInstance, ticks) => ticks.map((tick) => formatBytes(tick)) },
      ],
      series: seriesOptions,
    },
    [timestamps].concat(plotted.map((entry) => entry.values)),
    chartContainer,
  );
}

function renderTable() {
  const { groups, total_bytes: totalBytes } = loadedResponse;
  groupColumnHeader.textContent = splitLabels[state.split];
  tableBody.replaceChildren();
  groups.forEach((group, groupIndex) => {
    const row = document.createElement("tr");

    const keyCell = document.createElement("td");
    const toggleButton = document.createElement("button");
    toggleButton.type = "button";
    toggleButton.className = "group-toggle";
    toggleButton.setAttribute("aria-pressed", String(filteredGroupIndex === groupIndex));
    const swatch = document.createElement("span");
    swatch.className = "series-swatch";
    swatch.style.backgroundColor = colorForGroup(groupIndex);
    const keyText = document.createElement("span");
    keyText.textContent = displayKey(group.key);
    toggleButton.append(swatch, keyText);
    toggleButton.addEventListener("click", () => {
      filteredGroupIndex = filteredGroupIndex === groupIndex ? null : groupIndex;
      renderChart();
      renderTable();
      renderFilterNote();
      tableBody.querySelectorAll(".group-toggle")[groupIndex].focus();
    });
    keyCell.append(toggleButton);

    const bytesCell = document.createElement("td");
    bytesCell.textContent = formatBytes(group.bytes);

    const sharePercent = totalBytes > 0 ? (group.bytes / totalBytes) * 100 : 0;
    const shareCell = document.createElement("td");
    const shareBar = document.createElement("span");
    shareBar.className = "share-bar";
    shareBar.style.width = `${sharePercent}%`;
    const shareText = document.createElement("span");
    shareText.textContent = `${sharePercent.toFixed(1)}%`;
    shareCell.append(shareBar, shareText);

    const responsesCell = document.createElement("td");
    responsesCell.textContent = group.responses.toLocaleString();

    row.append(keyCell, bytesCell, shareCell, responsesCell);
    tableBody.append(row);
  });
}

function renderFilterNote() {
  if (filteredGroupIndex === null) {
    filterNote.hidden = true;
    return;
  }
  filterNote.textContent = `Showing only ${displayKey(loadedResponse.groups[filteredGroupIndex].key)}. Click the row again to show all.`;
  filterNote.hidden = false;
}

function renderHeadline() {
  const projectSuffix = state.project ? ` for project ${state.project}` : "";
  headline.textContent = `${formatBytes(loadedResponse.total_bytes)} of responses in the last ${presetLabels[state.range]}${projectSuffix}`;
}

function renderAll() {
  renderHeadline();
  renderChart();
  renderTable();
  renderFilterNote();
}

function rangeParameters() {
  const rangeEnd = new Date();
  const rangeStart = new Date(rangeEnd.getTime() - presetSeconds[state.range] * 1000);
  return { from: rangeStart.toISOString(), to: rangeEnd.toISOString() };
}

async function loadProjects(range) {
  const sequence = ++facetSequence;
  let facetResponse;
  try {
    facetResponse = await apiFetch("/ui/api/facets?" + new URLSearchParams({ field: "project", ...range }));
  } catch {
    return;
  }
  if (sequence !== facetSequence) {
    return;
  }
  const projectNames = facetResponse.values.map((facetValue) => facetValue.value);
  if (state.project && !projectNames.includes(state.project)) {
    projectNames.push(state.project);
  }
  projectSelect.replaceChildren(new Option("All projects", ""));
  for (const projectName of projectNames) {
    projectSelect.append(new Option(projectName, projectName));
  }
  projectSelect.value = state.project;
}

async function load() {
  const sequence = ++loadSequence;
  filteredGroupIndex = null;
  const range = rangeParameters();
  const parameters = new URLSearchParams({ ...range, step: state.step, split: state.split });
  if (state.project) {
    parameters.set("project", state.project);
  }
  loadProjects(range);
  let response;
  try {
    response = await apiFetch("/ui/api/bandwidth?" + parameters);
  } catch (error) {
    if (sequence === loadSequence) {
      errorMessage.textContent = error.message;
      errorMessage.hidden = false;
    }
    return;
  }
  if (sequence !== loadSequence) {
    return;
  }
  errorMessage.hidden = true;
  loadedResponse = response;
  renderAll();
}

function applyControlChange() {
  state = { split: splitSelect.value, project: projectSelect.value, range: rangeSelect.value, step: stepSelect.value };
  writeUrlState();
  load();
}

for (const control of [splitSelect, projectSelect, rangeSelect, stepSelect]) {
  control.addEventListener("change", applyControlChange);
}

new ResizeObserver(() => {
  if (!chart) {
    return;
  }
  if (chart.height !== chartHeight()) {
    renderChart();
    return;
  }
  chart.setSize({ width: chartContainer.clientWidth, height: chartHeight() });
}).observe(chartContainer);

window.addEventListener("popstate", () => {
  readUrlState();
  load();
});

readUrlState();
load();

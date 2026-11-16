const svgNamespace = "http://www.w3.org/2000/svg";
const statusClasses = [
  { key: "s2xx", label: "2xx" },
  { key: "s3xx", label: "3xx" },
  { key: "s4xx", label: "4xx" },
  { key: "s5xx", label: "5xx" },
  { key: "s0", label: "0" },
];
const axisLabelCount = 5;
const oneDayMilliseconds = 86400000;

function createSvgElement(tagName, attributes) {
  const element = document.createElementNS(svgNamespace, tagName);
  for (const [attributeName, attributeValue] of Object.entries(attributes)) {
    element.setAttribute(attributeName, String(attributeValue));
  }
  return element;
}

function bucketTotal(bucket) {
  return statusClasses.reduce((total, statusClass) => total + bucket[statusClass.key], 0);
}

function renderAxis(axisElement, range) {
  const spanMilliseconds = range.to - range.from;
  const options = spanMilliseconds <= oneDayMilliseconds
    ? { hour: "2-digit", minute: "2-digit", hour12: false }
    : { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false };
  const formatter = new Intl.DateTimeFormat(undefined, options);
  axisElement.replaceChildren();
  for (let labelIndex = 0; labelIndex < axisLabelCount; labelIndex += 1) {
    const labelTime = new Date(range.from.getTime() + (spanMilliseconds * labelIndex) / (axisLabelCount - 1));
    const label = document.createElement("span");
    label.textContent = formatter.format(labelTime);
    axisElement.append(label);
  }
}

export function renderTape(chartElement, axisElement, tapeResponse, range, callbacks) {
  const buckets = tapeResponse.buckets;
  const bucketCount = buckets.length;
  const totals = buckets.map(bucketTotal);
  const maxTotal = Math.max(0, ...totals);
  const requestCount = totals.reduce((sum, total) => sum + total, 0);
  const timeFormatter = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false });

  const svg = createSvgElement("svg", {
    viewBox: `0 0 ${Math.max(bucketCount, 1)} 100`,
    preserveAspectRatio: "none",
    class: "tape-svg",
    role: "img",
    "aria-label": `Status share for ${requestCount} requests from ${range.from.toLocaleString()} to ${range.to.toLocaleString()}`,
  });

  buckets.forEach((bucket, bucketIndex) => {
    const total = totals[bucketIndex];
    const bucketGroup = createSvgElement("g", {});
    const countSummary = statusClasses.filter((statusClass) => bucket[statusClass.key] > 0).map((statusClass) => `${statusClass.label} ${bucket[statusClass.key]}`);
    const title = document.createElementNS(svgNamespace, "title");
    title.textContent = [timeFormatter.format(new Date(bucket.t)), `${total} requests`, ...countSummary].join(" · ");
    bucketGroup.append(title);
    let cumulativeShare = 0;
    for (const statusClass of statusClasses) {
      const classCount = bucket[statusClass.key];
      if (classCount === 0) {
        continue;
      }
      const classShare = (classCount / total) * 100;
      bucketGroup.append(createSvgElement("rect", {
        x: bucketIndex,
        width: 1,
        y: 100 - cumulativeShare - classShare,
        height: classShare,
        class: `band status-${statusClass.label}`,
        "data-status-class": statusClass.label,
        "data-bucket-index": bucketIndex,
      }));
      cumulativeShare += classShare;
    }
    svg.append(bucketGroup);
  });

  if (maxTotal > 0) {
    const points = totals.map((total, bucketIndex) => `${bucketIndex + 0.5},${100 - (100 * total) / maxTotal}`).join(" ");
    svg.append(createSvgElement("polyline", { class: "tape-volume", points }));
  }

  const selection = createSvgElement("rect", { class: "tape-selection", x: 0, y: 0, width: 0, height: 100 });
  selection.setAttribute("visibility", "hidden");
  svg.append(selection);

  let dragStartIndex = null;
  let pressedStatusClass = null;

  function bucketIndexAt(pointerEvent) {
    const bounds = svg.getBoundingClientRect();
    const rawIndex = Math.floor(((pointerEvent.clientX - bounds.left) / bounds.width) * bucketCount);
    return Math.min(Math.max(rawIndex, 0), bucketCount - 1);
  }

  svg.addEventListener("pointerdown", (pointerEvent) => {
    if (bucketCount === 0) {
      return;
    }
    pressedStatusClass = (pointerEvent.target.dataset && pointerEvent.target.dataset.statusClass) || null;
    svg.setPointerCapture(pointerEvent.pointerId);
    dragStartIndex = bucketIndexAt(pointerEvent);
  });

  svg.addEventListener("pointermove", (pointerEvent) => {
    if (dragStartIndex === null) {
      return;
    }
    const currentIndex = bucketIndexAt(pointerEvent);
    selection.setAttribute("x", Math.min(dragStartIndex, currentIndex));
    selection.setAttribute("width", Math.abs(currentIndex - dragStartIndex) + 1);
    selection.setAttribute("visibility", currentIndex === dragStartIndex ? "hidden" : "visible");
  });

  svg.addEventListener("pointerup", (pointerEvent) => {
    if (dragStartIndex === null) {
      return;
    }
    const endIndex = bucketIndexAt(pointerEvent);
    const startIndex = dragStartIndex;
    dragStartIndex = null;
    selection.setAttribute("visibility", "hidden");
    if (endIndex !== startIndex) {
      const firstIndex = Math.min(startIndex, endIndex);
      const lastIndex = Math.max(startIndex, endIndex);
      const fromDate = new Date(buckets[firstIndex].t);
      const toDate = new Date(Math.min(new Date(buckets[lastIndex].t).getTime() + tapeResponse.step_seconds * 1000, range.to.getTime()));
      callbacks.onRangeSelected(fromDate, toDate);
      return;
    }
    if (pressedStatusClass) {
      callbacks.onStatusClassSelected(pressedStatusClass, pointerEvent.shiftKey);
    }
  });

  svg.addEventListener("pointercancel", () => {
    dragStartIndex = null;
    selection.setAttribute("visibility", "hidden");
  });

  chartElement.replaceChildren(svg);
  renderAxis(axisElement, range);
}

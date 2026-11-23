import { apiFetch, can } from "./shell.js";
import { canFilter, formatQueryTerm } from "./requests-query.js";
import { channelLabels, describeRule } from "./alert-rules.js";
import { attachPullToRefresh, trackHorizontalSwipe } from "./gestures.js";
import { confirmSheet } from "./sheet.js";

const eventLimit = 200;
const recentAlertCount = 50;
const timeFormatter = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
const dateFormatter = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });

const errorElement = document.getElementById("alerts-error");
const ruleList = document.getElementById("rule-list");
const rulesEmpty = document.getElementById("rules-empty");
const eventList = document.getElementById("event-list");
const eventsEmpty = document.getElementById("events-empty");
const pageElement = ruleList.parentElement;
const ruleByItem = new WeakMap();
let openRuleItem = null;
let ignoreClicksUntil = 0;

function startOfToday() {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return today;
}

function pluralize(count, singular, plural) {
  return count === 1 ? singular : plural;
}

function firedTodayText(todayEvents, truncated) {
  if (todayEvents.length === 0) {
    return "";
  }
  const newest = todayEvents.reduce((latest, candidate) => (Date.parse(candidate.fired_at) > Date.parse(latest.fired_at) ? candidate : latest));
  const lastAt = timeFormatter.format(new Date(newest.fired_at));
  let countText;
  if (truncated) {
    countText = `at least ${todayEvents.length} ${pluralize(todayEvents.length, "time", "times")}`;
  } else if (todayEvents.length === 1) {
    countText = "once";
  } else {
    countText = `${todayEvents.length} times`;
  }
  return ` Fired ${countText} today, last at ${lastAt}.`;
}

function renderRule(rule, todayEvents, truncated) {
  const item = document.createElement("li");
  const heading = document.createElement("span");
  heading.className = "rule-title";
  const marker = document.createElement("span");
  marker.className = "rule-marker";
  marker.setAttribute("aria-hidden", "true");
  marker.textContent = rule.enabled ? "●" : "○";
  heading.append(marker, " ");
  if (can("manage_alerts")) {
    const link = document.createElement("a");
    link.href = "/alerts/" + encodeURIComponent(rule.rule_id);
    link.textContent = rule.name;
    heading.append(link);
  } else {
    heading.append(rule.name);
  }
  const pausedLabel = document.createElement("span");
  pausedLabel.className = "rule-paused";
  pausedLabel.textContent = " (paused)";
  pausedLabel.hidden = rule.enabled;
  heading.append(pausedLabel);
  const channel = document.createElement("span");
  channel.className = "rule-channel";
  channel.textContent = channelLabels[rule.channel] || rule.channel;
  const sentence = document.createElement("p");
  sentence.className = "rule-sentence";
  sentence.textContent = describeRule(rule) + firedTodayText(todayEvents, truncated);
  const content = document.createElement("div");
  content.className = "rule-content";
  content.append(heading, channel, sentence);
  item.append(content);
  ruleByItem.set(item, rule);
  if (can("manage_alerts")) {
    item.append(buildRuleActions(item, rule));
    applyRuleState(item, rule);
    attachSwipe(item, content);
  }
  return item;
}

function applyRuleState(item, rule) {
  ruleByItem.set(item, rule);
  item.querySelector(".rule-marker").textContent = rule.enabled ? "●" : "○";
  item.querySelector(".rule-paused").hidden = rule.enabled;
  const pauseButton = item.querySelector(".rule-pause");
  pauseButton.textContent = rule.enabled ? "Pause" : "Resume";
  pauseButton.setAttribute("aria-label", `${rule.enabled ? "Pause" : "Resume"} rule ${rule.name}`);
}

function buildRuleActions(item, rule) {
  const actions = document.createElement("div");
  actions.className = "rule-actions";
  const pauseButton = document.createElement("button");
  pauseButton.type = "button";
  pauseButton.className = "rule-pause";
  pauseButton.addEventListener("click", () => toggleRuleEnabled(item));
  const deleteButton = document.createElement("button");
  deleteButton.type = "button";
  deleteButton.className = "rule-delete danger-button";
  deleteButton.textContent = "Delete";
  deleteButton.setAttribute("aria-label", `Delete rule ${rule.name}`);
  deleteButton.addEventListener("click", () => deleteRuleItem(item));
  actions.append(pauseButton, deleteButton);
  return actions;
}

function ruleUpdateBody(rule, enabled) {
  return {
    name: rule.name,
    project: rule.project,
    conditions: rule.conditions,
    channel: rule.channel,
    discord_webhook_url: "",
    telegram_bot_token: "",
    telegram_chat_id: rule.telegram_chat_id,
    window_seconds: rule.window_seconds,
    enabled,
  };
}

function ruleUrl(rule) {
  return "/ui/api/alert-rules/" + encodeURIComponent(rule.rule_id);
}

function setRuleBusy(item, busy) {
  item.classList.toggle("rule-busy", busy);
  for (const button of item.querySelectorAll(".rule-actions button")) {
    button.disabled = busy;
  }
}

function showActionError(message) {
  errorElement.textContent = message;
  errorElement.hidden = false;
  errorElement.scrollIntoView({ block: "nearest" });
}

function closeRuleItem(item) {
  item.classList.remove("swipe-open");
  if (openRuleItem === item) {
    openRuleItem = null;
  }
}

async function toggleRuleEnabled(item) {
  const rule = ruleByItem.get(item);
  errorElement.hidden = true;
  setRuleBusy(item, true);
  try {
    const updatedRule = await apiFetch(ruleUrl(rule), { method: "PUT", body: ruleUpdateBody(rule, !rule.enabled) });
    applyRuleState(item, updatedRule);
  } catch (error) {
    showActionError(error.message);
  }
  setRuleBusy(item, false);
  closeRuleItem(item);
}

async function deleteRuleItem(item) {
  const rule = ruleByItem.get(item);
  const confirmed = await confirmSheet({ message: `Delete rule "${rule.name}"? This can't be undone.`, confirmLabel: "Delete rule" });
  if (!confirmed) {
    return;
  }
  errorElement.hidden = true;
  setRuleBusy(item, true);
  try {
    await apiFetch(ruleUrl(rule), { method: "DELETE", body: {} });
    closeRuleItem(item);
    item.remove();
    rulesEmpty.hidden = ruleList.children.length > 0;
  } catch (error) {
    showActionError(error.message);
    setRuleBusy(item, false);
    closeRuleItem(item);
  }
}

function attachSwipe(item, content) {
  const actions = item.querySelector(".rule-actions");
  let revealWidth = 0;
  let startOffset = 0;

  function settle(shouldOpen) {
    item.classList.remove("swipe-dragging");
    item.style.removeProperty("--rule-swipe-x");
    revealWidth = 0;
    ignoreClicksUntil = performance.now() + 400;
    if (shouldOpen) {
      item.classList.add("swipe-open");
      openRuleItem = item;
    } else {
      closeRuleItem(item);
    }
  }

  trackHorizontalSwipe(content, {
    canStart: () => actions.offsetWidth > 0 && !item.classList.contains("rule-busy"),
    onMove: (distance) => {
      if (revealWidth === 0) {
        revealWidth = actions.offsetWidth;
        startOffset = openRuleItem === item ? -revealWidth : 0;
        if (openRuleItem !== null && openRuleItem !== item) {
          closeRuleItem(openRuleItem);
        }
        item.classList.add("swipe-dragging");
      }
      const offset = Math.min(0, Math.max(-revealWidth, startOffset + distance));
      item.style.setProperty("--rule-swipe-x", `${offset}px`);
    },
    onRelease: (distance) => settle(startOffset + distance < -revealWidth / 2),
    onCancel: () => settle(openRuleItem === item),
  });
}

document.addEventListener("click", (clickEvent) => {
  if (performance.now() < ignoreClicksUntil) {
    clickEvent.preventDefault();
    clickEvent.stopPropagation();
    return;
  }
  if (openRuleItem === null || openRuleItem.querySelector(".rule-actions").contains(clickEvent.target) || clickEvent.target.closest("dialog") !== null) {
    return;
  }
  closeRuleItem(openRuleItem);
  if (ruleList.contains(clickEvent.target)) {
    clickEvent.preventDefault();
    clickEvent.stopPropagation();
  }
}, true);

function renderSample(sample) {
  const line = document.createElement("p");
  line.className = "sample-line";
  let text = `${sample.status} ${sample.method} ${sample.url} (${sample.latency_ms} ms)`;
  if (sample.body_snippet) {
    text += ` body: "${sample.body_snippet}"`;
  }
  if (!can("view_requests") || !canFilter("id", sample.request_id)) {
    line.append(`${text} (request id: ${sample.request_id})`);
    return line;
  }
  line.append(text + " ");
  const link = document.createElement("a");
  const sampleTime = Date.parse(sample.ts);
  link.href = "/requests?" + new URLSearchParams({
    q: formatQueryTerm("id", sample.request_id, false),
    from: new Date(sampleTime - 1000).toISOString(),
    to: new Date(sampleTime + 1000).toISOString(),
  });
  link.textContent = "Open";
  line.append(link);
  return line;
}

function renderEvent(alertEvent, todayStart) {
  const item = document.createElement("li");
  const firedAt = new Date(alertEvent.fired_at);
  let timeText = timeFormatter.format(firedAt);
  if (firedAt < todayStart) {
    timeText = `${dateFormatter.format(firedAt)} ${timeText}`;
  }
  let summary = `${timeText} ${alertEvent.rule_name} ${alertEvent.match_count} ${pluralize(alertEvent.match_count, "match", "matches")}`;
  if (alertEvent.dropped_count > 0) {
    summary += `, ${alertEvent.dropped_count} dropped`;
  }
  if (alertEvent.delivery === "sent") {
    summary += ", sent";
  } else {
    summary += alertEvent.delivery_error ? `, failed: ${alertEvent.delivery_error}` : ", failed";
  }
  const summaryLine = document.createElement("p");
  summaryLine.className = "event-summary";
  summaryLine.textContent = summary;
  item.append(summaryLine);
  if (alertEvent.samples.length > 0) {
    const details = document.createElement("details");
    const detailsSummary = document.createElement("summary");
    detailsSummary.textContent = "Samples";
    details.append(detailsSummary, ...alertEvent.samples.map(renderSample));
    item.append(details);
  }
  return item;
}

async function load() {
  let rules;
  let events;
  errorElement.hidden = true;
  pageElement.setAttribute("aria-busy", "true");
  try {
    [rules, events] = await Promise.all([
      apiFetch("/ui/api/alert-rules"),
      apiFetch("/ui/api/alert-events?limit=" + eventLimit),
    ]);
  } catch (error) {
    errorElement.textContent = error.message;
    errorElement.hidden = false;
    pageElement.removeAttribute("aria-busy");
    return;
  }
  pageElement.removeAttribute("aria-busy");
  const todayStart = startOfToday();
  const truncated = events.length === eventLimit && new Date(events[eventLimit - 1].fired_at) >= todayStart;
  ruleList.replaceChildren();
  openRuleItem = null;
  eventList.replaceChildren();
  rulesEmpty.hidden = rules.length > 0;
  for (const rule of rules) {
    const todayEvents = events.filter((alertEvent) => alertEvent.rule_id === rule.rule_id && new Date(alertEvent.fired_at) >= todayStart);
    ruleList.append(renderRule(rule, todayEvents, truncated));
  }
  const recentEvents = events.slice(0, recentAlertCount);
  eventsEmpty.hidden = recentEvents.length > 0;
  for (const alertEvent of recentEvents) {
    eventList.append(renderEvent(alertEvent, todayStart));
  }
}

load();
attachPullToRefresh(load);

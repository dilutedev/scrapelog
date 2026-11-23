import { apiFetch, can } from "./shell.js";
import { canFilter, formatQueryTerm } from "./requests-query.js";
import { channelLabels, describeRule } from "./alert-rules.js";
import { attachPullToRefresh } from "./gestures.js";

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
  if (!rule.enabled) {
    heading.append(" (paused)");
  }
  const channel = document.createElement("span");
  channel.className = "rule-channel";
  channel.textContent = channelLabels[rule.channel] || rule.channel;
  const sentence = document.createElement("p");
  sentence.className = "rule-sentence";
  sentence.textContent = describeRule(rule) + firedTodayText(todayEvents, truncated);
  item.append(heading, channel, sentence);
  return item;
}

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

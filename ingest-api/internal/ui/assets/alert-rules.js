const numericOperators = ["equals", "not_equals", "between", "greater_than", "less_than", "class"];
const measurementOperators = ["equals", "between", "greater_than", "less_than"];
const exactOperators = ["equals", "not_equals"];
const textOperators = ["contains", "not_contains", "regex"];
const keyedOperators = ["equals", "contains", "not_contains", "regex", "exists", "missing"];

export const fieldDefinitions = [
  { value: "status", label: "Status", keyed: false, numeric: true, operators: numericOperators },
  { value: "latency_ms", label: "Latency (ms)", keyed: false, numeric: true, operators: measurementOperators },
  { value: "response_size", label: "Response size (bytes)", keyed: false, numeric: true, operators: measurementOperators },
  { value: "method", label: "Method", keyed: false, numeric: false, operators: exactOperators },
  { value: "domain", label: "Domain", keyed: false, numeric: false, operators: exactOperators },
  { value: "scraper", label: "Scraper", keyed: false, numeric: false, operators: exactOperators },
  { value: "proxy", label: "Proxy", keyed: false, numeric: false, operators: exactOperators },
  { value: "tls_profile", label: "TLS profile", keyed: false, numeric: false, operators: exactOperators },
  { value: "url", label: "URL", keyed: false, numeric: false, operators: textOperators },
  { value: "error", label: "Error", keyed: false, numeric: false, operators: textOperators },
  { value: "response_header", label: "Response header", keyed: true, numeric: false, operators: keyedOperators },
  { value: "request_header", label: "Request header", keyed: true, numeric: false, operators: keyedOperators },
  { value: "tag", label: "Tag", keyed: true, numeric: false, operators: keyedOperators },
  { value: "body", label: "Body", keyed: false, numeric: false, operators: textOperators },
];

export const operatorLabels = {
  equals: "is",
  not_equals: "is not",
  between: "is between",
  greater_than: "is greater than",
  less_than: "is less than",
  class: "is in class",
  contains: "contains",
  not_contains: "does not contain",
  regex: "matches regex",
  exists: "is present",
  missing: "is missing",
};

export const valuePlaceholders = { between: "400..499", class: "4xx", regex: "(?i)captcha" };

export const channelLabels = { discord: "Discord", telegram: "Telegram" };

export function isExistenceOperator(operator) {
  return operator === "exists" || operator === "missing";
}

function describeValue(condition, definition) {
  const value = condition.value === undefined ? "" : String(condition.value);
  if (condition.operator === "regex") {
    return `/${value}/`;
  }
  if (definition && definition.numeric) {
    if (condition.operator === "between") {
      const bounds = value.split("..");
      if (bounds.length === 2) {
        return `${bounds[0]} and ${bounds[1]}`;
      }
    }
    return value;
  }
  return `"${value}"`;
}

function describeCondition(condition) {
  const definition = fieldDefinitions.find((candidate) => candidate.value === condition.field);
  const parts = [definition ? definition.label.toLowerCase() : condition.field || ""];
  if (definition && definition.keyed && condition.name) {
    parts.push(`"${condition.name}"`);
  }
  if (condition.operator) {
    parts.push(operatorLabels[condition.operator] || condition.operator);
  }
  if (condition.operator && !isExistenceOperator(condition.operator)) {
    parts.push(describeValue(condition, definition));
  }
  return parts.filter((part) => part !== "").join(" ");
}

export function describeRule(rule) {
  if (!rule.conditions || rule.conditions.length === 0) {
    return "Add a condition to describe when this rule fires.";
  }
  const scope = rule.project ? ` in project ${rule.project}` : " in all projects";
  return `When ${rule.conditions.map(describeCondition).join(" and ")}${scope}.`;
}

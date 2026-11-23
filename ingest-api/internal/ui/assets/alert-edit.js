import { apiFetch } from "./shell.js";
import { channelLabels, describeRule, fieldDefinitions, isExistenceOperator, operatorLabels, valuePlaceholders } from "./alert-rules.js";

const maximumConditions = 20;
const rulesPath = "/ui/api/alert-rules";
const projectsPath = "/ui/api/projects";

const titleElement = document.getElementById("editor-title");
const missingElement = document.getElementById("editor-missing");
const form = document.getElementById("rule-form");
const nameInput = document.getElementById("rule-name");
const projectInput = document.getElementById("rule-project");
const conditionRows = document.getElementById("condition-rows");
const addConditionButton = document.getElementById("add-condition");
const channelRadios = Array.from(document.querySelectorAll('input[name="channel"]'));
const discordFields = document.getElementById("discord-fields");
const discordInput = document.getElementById("discord-webhook");
const discordSavedTag = document.getElementById("discord-saved");
const telegramFields = document.getElementById("telegram-fields");
const telegramTokenInput = document.getElementById("telegram-token");
const telegramSavedTag = document.getElementById("telegram-saved");
const telegramChatInput = document.getElementById("telegram-chat");
const windowInput = document.getElementById("rule-window");
const enabledCheckbox = document.getElementById("rule-enabled");
const previewElement = document.getElementById("rule-preview");
const statusElement = document.getElementById("editor-status");
const sendTestButton = document.getElementById("send-test");
const saveButton = document.getElementById("save-rule");
const deleteButton = document.getElementById("delete-rule");
const testNote = document.getElementById("test-note");

let ruleId = null;
let savedRule = null;

function findDefinition(fieldValue) {
  return fieldDefinitions.find((definition) => definition.value === fieldValue);
}

function checkedChannel() {
  return channelRadios.find((radio) => radio.checked).value;
}

function rowParts(row) {
  return {
    fieldSelect: row.querySelector(".condition-field"),
    nameInput: row.querySelector(".condition-name"),
    operatorSelect: row.querySelector(".condition-operator"),
    valueInput: row.querySelector(".condition-value"),
    removeButton: row.querySelector(".condition-remove"),
  };
}

function syncOperatorOptions(row) {
  const parts = rowParts(row);
  const definition = findDefinition(parts.fieldSelect.value);
  const previousOperator = parts.operatorSelect.value;
  parts.operatorSelect.replaceChildren(...definition.operators.map((operator) => new Option(operatorLabels[operator], operator)));
  parts.operatorSelect.value = definition.operators.includes(previousOperator) ? previousOperator : definition.operators[0];
  parts.nameInput.hidden = !definition.keyed;
  parts.nameInput.placeholder = definition.value === "tag" ? "Tag name" : "Header name";
  syncValueInput(row);
}

function syncValueInput(row) {
  const parts = rowParts(row);
  parts.valueInput.hidden = isExistenceOperator(parts.operatorSelect.value);
  parts.valueInput.placeholder = valuePlaceholders[parts.operatorSelect.value] || "";
}

function syncRowControls() {
  const rows = Array.from(conditionRows.children);
  rows.forEach((row, index) => {
    const removeButton = rowParts(row).removeButton;
    removeButton.disabled = rows.length === 1;
    removeButton.setAttribute("aria-label", `Remove condition ${index + 1}`);
  });
  addConditionButton.disabled = rows.length >= maximumConditions;
}

function addConditionRow(condition) {
  const row = document.createElement("li");
  const fieldSelect = document.createElement("select");
  fieldSelect.className = "condition-field";
  fieldSelect.setAttribute("aria-label", "Field");
  fieldSelect.append(...fieldDefinitions.map((definition) => new Option(definition.label, definition.value)));
  const nameInputElement = document.createElement("input");
  nameInputElement.className = "condition-name";
  nameInputElement.setAttribute("aria-label", "Header or tag name");
  const operatorSelect = document.createElement("select");
  operatorSelect.className = "condition-operator";
  operatorSelect.setAttribute("aria-label", "Operator");
  const valueInput = document.createElement("input");
  valueInput.className = "condition-value";
  valueInput.setAttribute("aria-label", "Value");
  const removeButton = document.createElement("button");
  removeButton.type = "button";
  removeButton.className = "condition-remove";
  removeButton.textContent = "Remove";
  row.append(fieldSelect, nameInputElement, operatorSelect, valueInput, removeButton);
  conditionRows.append(row);

  fieldSelect.value = condition.field;
  syncOperatorOptions(row);
  operatorSelect.value = condition.operator;
  nameInputElement.value = condition.name || "";
  valueInput.value = condition.value || "";
  syncValueInput(row);

  fieldSelect.addEventListener("change", () => syncOperatorOptions(row));
  operatorSelect.addEventListener("change", () => syncValueInput(row));
  removeButton.addEventListener("click", () => {
    row.remove();
    syncRowControls();
    updatePreview();
  });
  syncRowControls();
}

function replaceConditionRows(conditions) {
  conditionRows.replaceChildren();
  for (const condition of conditions) {
    addConditionRow(condition);
  }
}

function readForm() {
  return {
    name: nameInput.value.trim(),
    project: projectInput.value.trim(),
    conditions: Array.from(conditionRows.children).map((row) => {
      const parts = rowParts(row);
      const definition = findDefinition(parts.fieldSelect.value);
      const condition = { field: definition.value, operator: parts.operatorSelect.value };
      if (definition.keyed) {
        condition.name = parts.nameInput.value.trim();
      }
      if (!isExistenceOperator(condition.operator)) {
        condition.value = parts.valueInput.value;
      }
      return condition;
    }),
    channel: checkedChannel(),
    discord_webhook_url: discordInput.value.trim(),
    telegram_bot_token: telegramTokenInput.value.trim(),
    telegram_chat_id: telegramChatInput.value.trim(),
    window_seconds: Number(windowInput.value) || 0,
    enabled: enabledCheckbox.checked,
  };
}

function updatePreview() {
  const draft = readForm();
  let preview = `${describeRule(draft)} Send to ${channelLabels[draft.channel]} at most once every ${draft.window_seconds || 60} seconds.`;
  if (!draft.enabled) {
    preview += " Paused.";
  }
  previewElement.textContent = preview;
}

function syncChannelFields() {
  const channel = checkedChannel();
  discordFields.hidden = channel !== "discord";
  telegramFields.hidden = channel !== "telegram";
}

function renderRule(rule) {
  ruleId = rule.rule_id;
  savedRule = rule;
  titleElement.textContent = rule.name;
  document.title = `${rule.name} · Alerts · scrapelog`;
  nameInput.value = rule.name;
  if (rule.project && !Array.from(projectInput.options).some((option) => option.value === rule.project)) {
    projectInput.append(new Option(rule.project, rule.project));
  }
  projectInput.value = rule.project;
  windowInput.value = rule.window_seconds;
  enabledCheckbox.checked = rule.enabled;
  for (const radio of channelRadios) {
    radio.checked = radio.value === rule.channel;
  }
  telegramChatInput.value = rule.telegram_chat_id;
  discordInput.value = "";
  telegramTokenInput.value = "";
  discordSavedTag.hidden = !rule.discord_webhook_set;
  telegramSavedTag.hidden = !rule.telegram_token_set;
  const keepPlaceholder = "Leave blank to keep the saved one";
  discordInput.placeholder = rule.discord_webhook_set ? keepPlaceholder : "";
  telegramTokenInput.placeholder = rule.telegram_token_set ? keepPlaceholder : "";
  replaceConditionRows(rule.conditions.map((condition) => ({ ...condition })));
  syncChannelFields();
  deleteButton.hidden = false;
  sendTestButton.disabled = false;
  testNote.textContent = "Sends a test using the saved settings.";
  updatePreview();
}

function ruleUrl() {
  return `${rulesPath}/${encodeURIComponent(ruleId)}`;
}

async function saveRule() {
  saveButton.disabled = true;
  statusElement.textContent = "";
  try {
    const creating = ruleId === null;
    const rule = await apiFetch(creating ? rulesPath : ruleUrl(), { method: creating ? "POST" : "PUT", body: readForm() });
    if (creating) {
      history.replaceState(null, "", "/alerts/" + encodeURIComponent(rule.rule_id));
    }
    renderRule(rule);
    statusElement.textContent = "Saved.";
  } catch (error) {
    statusElement.textContent = error.message;
  }
  saveButton.disabled = false;
}

async function sendTest() {
  sendTestButton.disabled = true;
  statusElement.textContent = "Sending test message…";
  try {
    const result = await apiFetch(`${ruleUrl()}/test`, { method: "POST", body: {} });
    if (result.delivery === "sent") {
      statusElement.textContent = `Test message sent (HTTP ${result.status_code}).`;
    } else if (result.delivery_error) {
      statusElement.textContent = `Test message failed: ${result.delivery_error}`;
    } else {
      statusElement.textContent = `Test message failed (HTTP ${result.status_code}).`;
    }
  } catch (error) {
    statusElement.textContent = error.message;
  }
  sendTestButton.disabled = false;
}

async function deleteRule() {
  if (!confirm(`Delete rule "${savedRule.name}"? This can't be undone.`)) {
    return;
  }
  try {
    await apiFetch(ruleUrl(), { method: "DELETE", body: {} });
    location.assign("/alerts");
  } catch (error) {
    statusElement.textContent = error.message;
  }
}

function showUnavailable(message) {
  form.hidden = true;
  titleElement.hidden = true;
  missingElement.textContent = message;
  missingElement.hidden = false;
}

async function loadProjects() {
  try {
    const projectNames = await apiFetch(projectsPath);
    for (const projectName of projectNames) {
      projectInput.append(new Option(projectName, projectName));
    }
  } catch {}
}

async function start() {
  await loadProjects();
  let segment;
  try {
    segment = decodeURIComponent(location.pathname.slice("/alerts/".length));
  } catch {
    showUnavailable(missingElement.textContent);
    return;
  }
  if (segment === "new") {
    replaceConditionRows([{ field: "status", operator: "equals" }]);
    syncChannelFields();
    updatePreview();
    return;
  }
  try {
    form.setAttribute("aria-busy", "true");
    const rules = await apiFetch(rulesPath);
    const rule = rules.find((candidate) => candidate.rule_id === segment);
    if (!rule) {
      showUnavailable(missingElement.textContent);
      return;
    }
    renderRule(rule);
  } catch (error) {
    showUnavailable(error.message);
  } finally {
    form.removeAttribute("aria-busy");
  }
}

form.addEventListener("input", updatePreview);
form.addEventListener("change", updatePreview);
for (const radio of channelRadios) {
  radio.addEventListener("change", syncChannelFields);
}
addConditionButton.addEventListener("click", () => {
  addConditionRow({ field: "status", operator: "equals" });
  updatePreview();
});
form.addEventListener("submit", (submitEvent) => {
  submitEvent.preventDefault();
  saveRule();
});
sendTestButton.addEventListener("click", sendTest);
deleteButton.addEventListener("click", deleteRule);

start();

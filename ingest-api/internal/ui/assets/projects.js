import { apiFetch } from "./shell.js";

const revokeDelayNote = "Revoked keys can keep working for up to 35 seconds on other instances.";

let keys = [];
let addingToExistingProject = false;
let confirmingKeyId = null;

const errorElement = document.getElementById("projects-error");
const projectList = document.getElementById("project-list");
const projectsEmpty = document.getElementById("projects-empty");
const keyForm = document.getElementById("key-form");
const formTitle = document.getElementById("key-form-title");
const projectInput = document.getElementById("key-project");
const keyNameInput = document.getElementById("key-name");
const statusElement = document.getElementById("key-status");
const saveButton = document.getElementById("save-key");
const newKeyPanel = document.getElementById("new-key-panel");
const newKeyNote = document.getElementById("new-key-note");
const newKeyValue = document.getElementById("new-key-value");
const copyButton = document.getElementById("copy-key");

function showError(message) {
  errorElement.textContent = message;
  errorElement.hidden = message === "";
}

function groupByProject() {
  const grouped = new Map();
  for (const key of keys) {
    if (!grouped.has(key.project)) {
      grouped.set(key.project, []);
    }
    grouped.get(key.project).push(key);
  }
  return new Map(Array.from(grouped).sort(([first], [second]) => first.localeCompare(second)));
}

function renderKey(key) {
  const item = document.createElement("li");
  const heading = document.createElement("span");
  heading.className = "rule-title";
  heading.textContent = key.name;
  const sentence = document.createElement("p");
  sentence.className = "rule-sentence";
  sentence.textContent = key.key_id + " · created " + new Date(key.created_at).toLocaleString();
  item.append(heading, " ");
  if (key.revoked) {
    item.append("(revoked)");
  } else if (confirmingKeyId === key.key_id) {
    const confirmButton = document.createElement("button");
    confirmButton.type = "button";
    confirmButton.textContent = "Confirm revoke";
    confirmButton.addEventListener("click", () => revokeKey(key));
    const cancelButton = document.createElement("button");
    cancelButton.type = "button";
    cancelButton.textContent = "Cancel";
    cancelButton.addEventListener("click", () => {
      confirmingKeyId = null;
      renderList();
    });
    const note = document.createElement("p");
    note.className = "note";
    note.textContent = revokeDelayNote;
    item.append(confirmButton, " ", cancelButton, note);
  } else {
    const revokeButton = document.createElement("button");
    revokeButton.type = "button";
    revokeButton.textContent = "Revoke";
    revokeButton.setAttribute("aria-label", "Revoke " + key.name);
    revokeButton.addEventListener("click", () => {
      confirmingKeyId = key.key_id;
      renderList();
    });
    item.append(revokeButton);
  }
  item.append(sentence);
  return item;
}

function renderProject(projectName, projectKeys) {
  const item = document.createElement("li");
  const heading = document.createElement("span");
  heading.className = "rule-title";
  heading.textContent = projectName;
  const addButton = document.createElement("button");
  addButton.type = "button";
  addButton.textContent = "Add key";
  addButton.setAttribute("aria-label", "Add key to " + projectName);
  addButton.addEventListener("click", () => openForm(projectName));
  const keyList = document.createElement("ul");
  keyList.className = "rule-list";
  keyList.replaceChildren(...projectKeys.map(renderKey));
  item.append(heading, addButton, keyList);
  return item;
}

function renderList() {
  const grouped = groupByProject();
  projectList.replaceChildren(...Array.from(grouped, ([projectName, projectKeys]) => renderProject(projectName, projectKeys)));
  projectsEmpty.hidden = grouped.size > 0;
}

function openForm(projectName) {
  keyForm.reset();
  statusElement.textContent = "";
  addingToExistingProject = projectName !== undefined;
  projectInput.value = addingToExistingProject ? projectName : "";
  projectInput.readOnly = addingToExistingProject;
  formTitle.textContent = addingToExistingProject ? "Add key to " + projectName : "New project";
  saveButton.textContent = "Create key";
  keyForm.hidden = false;
  keyForm.scrollIntoView({ block: "start" });
  (addingToExistingProject ? keyNameInput : projectInput).focus();
}

function closeForm() {
  keyForm.hidden = true;
  keyForm.reset();
  statusElement.textContent = "";
}

function dismissNewKey() {
  newKeyValue.textContent = "";
  newKeyNote.textContent = "";
  copyButton.textContent = "Copy";
  newKeyPanel.hidden = true;
}

async function submitForm(event) {
  event.preventDefault();
  const project = projectInput.value.trim();
  const name = keyNameInput.value.trim();
  if (project === "" || name === "") {
    statusElement.textContent = "Enter a project and a key name.";
    return;
  }
  const projectExists = keys.some((key) => key.project === project);
  saveButton.disabled = true;
  statusElement.textContent = "";
  try {
    const created = await apiFetch("/ui/api/keys", { method: "POST", body: { project, name } });
    newKeyValue.textContent = created.key;
    newKeyNote.textContent = projectExists ? "Key added to existing project " + project + "." : "";
    newKeyPanel.hidden = false;
    closeForm();
    showError("");
    await load();
  } catch (error) {
    statusElement.textContent = error.message;
  } finally {
    saveButton.disabled = false;
  }
}

async function revokeKey(key) {
  try {
    await apiFetch("/ui/api/keys/" + encodeURIComponent(key.key_id), { method: "DELETE", body: {} });
    confirmingKeyId = null;
    showError("");
    await load();
  } catch (error) {
    showError(error.message);
  }
}

async function load() {
  try {
    keys = await apiFetch("/ui/api/keys");
    renderList();
  } catch (error) {
    showError(error.message);
  }
}

document.getElementById("add-project").addEventListener("click", () => openForm());
document.getElementById("cancel-key").addEventListener("click", closeForm);
document.getElementById("dismiss-key").addEventListener("click", dismissNewKey);
copyButton.addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(newKeyValue.textContent);
    copyButton.textContent = "Copied";
  } catch (error) {
    showError("Copy failed. Select the key and copy it manually.");
  }
});
keyForm.addEventListener("submit", submitForm);
load();

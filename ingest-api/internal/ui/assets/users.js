import { apiFetch } from "./shell.js";

const permissionLabels = {
  view_requests: "View requests",
  view_bandwidth: "View bandwidth",
  view_alerts: "View alerts",
  manage_alerts: "Create and change alerts (includes viewing)",
  view_archives: "View and download archives",
  estimate_restores: "Estimate restores",
  manage_restores: "Start and delete restores (includes the estimate)",
};

const usersById = new Map();
let editingUserId = null;

const errorElement = document.getElementById("users-error");
const userList = document.getElementById("user-list");
const usersEmpty = document.getElementById("users-empty");
const userForm = document.getElementById("user-form");
const formTitle = document.getElementById("user-form-title");
const usernameInput = document.getElementById("user-username");
const passwordInput = document.getElementById("user-password");
const passwordLabel = document.getElementById("user-password-label");
const passwordNote = document.getElementById("user-password-note");
const disabledRow = document.getElementById("user-disabled-row");
const disabledInput = document.getElementById("user-disabled");
const statusElement = document.getElementById("user-status");
const saveButton = document.getElementById("save-user");
const permissionBoxes = Array.from(userForm.querySelectorAll('input[name="permission"]'));

const createPasswordNote = passwordNote.textContent;
const editPasswordNote = "Leave blank to keep the current password. Setting one signs the user out.";
const disableNote = " Disabling also signs the user out.";

function summarizePermissions(permissions) {
  if (permissions.length === 0) {
    return "No permissions";
  }
  if (permissions.length === Object.keys(permissionLabels).length) {
    return "All permissions";
  }
  return permissions.map((permission) => permissionLabels[permission] || permission).join(", ");
}

function renderUser(user) {
  const item = document.createElement("li");
  const heading = document.createElement("span");
  heading.className = "rule-title";
  const marker = document.createElement("span");
  marker.className = "rule-marker";
  marker.setAttribute("aria-hidden", "true");
  marker.textContent = user.disabled ? "○" : "●";
  heading.append(marker, " ", user.username);
  if (user.disabled) {
    heading.append(" (disabled)");
  }
  const editButton = document.createElement("button");
  editButton.type = "button";
  editButton.textContent = "Edit";
  editButton.setAttribute("aria-label", "Edit " + user.username);
  editButton.addEventListener("click", () => openEdit(user));
  const sentence = document.createElement("p");
  sentence.className = "rule-sentence";
  sentence.textContent = summarizePermissions(user.permissions);
  item.append(heading, editButton, sentence);
  return item;
}

function renderList() {
  userList.replaceChildren(...Array.from(usersById.values(), renderUser));
  usersEmpty.hidden = usersById.size > 0;
}

function showError(message) {
  errorElement.textContent = message;
  errorElement.hidden = message === "";
}

function resetForm() {
  userForm.reset();
  statusElement.textContent = "";
}

function openCreate() {
  editingUserId = null;
  resetForm();
  formTitle.textContent = "Add user";
  usernameInput.readOnly = false;
  passwordLabel.textContent = "Password";
  passwordNote.textContent = createPasswordNote;
  disabledRow.hidden = true;
  saveButton.textContent = "Create user";
  userForm.hidden = false;
  usernameInput.focus();
}

function openEdit(user) {
  editingUserId = user.user_id;
  resetForm();
  formTitle.textContent = "Edit " + user.username;
  usernameInput.value = user.username;
  usernameInput.readOnly = true;
  passwordLabel.textContent = "New password";
  passwordNote.textContent = editPasswordNote + disableNote;
  for (const box of permissionBoxes) {
    box.checked = user.permissions.includes(box.value);
  }
  disabledInput.checked = user.disabled;
  disabledRow.hidden = false;
  saveButton.textContent = "Save changes";
  userForm.hidden = false;
  userForm.scrollIntoView({ block: "start" });
  passwordInput.focus();
}

function checkedPermissions() {
  return permissionBoxes.filter((box) => box.checked).map((box) => box.value);
}

async function submitForm(event) {
  event.preventDefault();
  const permissions = checkedPermissions();
  let path = "/ui/api/users";
  let method = "POST";
  let body;
  if (editingUserId === null) {
    if (usernameInput.value.trim() === "" || passwordInput.value === "") {
      statusElement.textContent = "Enter a username and password.";
      return;
    }
    body = { username: usernameInput.value, password: passwordInput.value, permissions };
  } else {
    path += "/" + encodeURIComponent(editingUserId);
    method = "PUT";
    body = { permissions, password: passwordInput.value, disabled: disabledInput.checked };
  }
  saveButton.disabled = true;
  statusElement.textContent = "";
  try {
    const saved = await apiFetch(path, { method, body });
    usersById.set(saved.user_id, saved);
    userForm.hidden = true;
    resetForm();
    showError("");
    renderList();
  } catch (error) {
    statusElement.textContent = error.message;
  } finally {
    saveButton.disabled = false;
  }
}

async function load() {
  try {
    const body = await apiFetch("/ui/api/users");
    usersById.clear();
    for (const user of body.users) {
      usersById.set(user.user_id, user);
    }
    renderList();
  } catch (error) {
    showError(error.message);
  }
}

document.getElementById("add-user").addEventListener("click", openCreate);
document.getElementById("cancel-user").addEventListener("click", () => {
  userForm.hidden = true;
  resetForm();
});
document.getElementById("select-all-permissions").addEventListener("click", () => {
  for (const box of permissionBoxes) {
    box.checked = true;
  }
});
userForm.addEventListener("submit", submitForm);
load();

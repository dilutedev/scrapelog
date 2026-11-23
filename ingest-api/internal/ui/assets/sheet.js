import { trackVerticalDrag } from "./gestures.js";

const dismissDistance = 96;

function attachDragHandle(dialog) {
  const handle = document.createElement("div");
  handle.className = "sheet-handle";
  handle.setAttribute("aria-hidden", "true");
  dialog.prepend(handle);
  trackVerticalDrag(handle, {
    canStart: () => true,
    onMove: (distance) => {
      dialog.classList.add("sheet-dragging");
      dialog.style.transform = `translateY(${distance}px)`;
    },
    onRelease: (distance) => {
      dialog.classList.remove("sheet-dragging");
      if (distance >= dismissDistance) {
        dialog.close("cancel");
        return;
      }
      dialog.style.transform = "";
    },
    onCancel: () => {
      dialog.classList.remove("sheet-dragging");
      dialog.style.transform = "";
    },
  });
}

export function createSheet(contentElement) {
  const dialog = document.createElement("dialog");
  dialog.className = "sheet";
  const body = document.createElement("div");
  body.className = "sheet-body";
  body.append(contentElement);
  dialog.append(body);
  dialog.addEventListener("click", (clickEvent) => {
    if (clickEvent.target === dialog) {
      dialog.close("cancel");
    }
  });
  dialog.addEventListener("close", () => dialog.remove());
  attachDragHandle(dialog);
  document.body.append(dialog);
  return dialog;
}

export function confirmSheet({ message, confirmLabel, destructive = true }) {
  return new Promise((resolve) => {
    const form = document.createElement("form");
    form.method = "dialog";
    const messageElement = document.createElement("p");
    messageElement.className = "sheet-message";
    messageElement.id = "sheet-message";
    messageElement.textContent = message;
    const actions = document.createElement("div");
    actions.className = "sheet-actions";
    const cancelButton = document.createElement("button");
    cancelButton.type = "submit";
    cancelButton.value = "cancel";
    cancelButton.textContent = "Cancel";
    cancelButton.autofocus = true;
    const confirmButton = document.createElement("button");
    confirmButton.type = "submit";
    confirmButton.value = "confirm";
    confirmButton.textContent = confirmLabel;
    if (destructive) {
      confirmButton.className = "danger-button";
    }
    actions.append(cancelButton, confirmButton);
    form.append(messageElement, actions);
    const dialog = createSheet(form);
    dialog.setAttribute("aria-labelledby", messageElement.id);
    dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"));
    dialog.showModal();
  });
}

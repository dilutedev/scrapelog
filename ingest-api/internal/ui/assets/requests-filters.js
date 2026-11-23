import { createSheet } from "./sheet.js";

export function attachFiltersSheet(filtersButton, filtersPanel, exportMenu) {
  const panelHome = filtersPanel.parentElement;
  const panelNextSibling = filtersPanel.nextElementSibling;

  filtersButton.addEventListener("click", () => {
    const content = document.createElement("div");
    const heading = document.createElement("h2");
    heading.id = "filters-sheet-title";
    heading.className = "sheet-title";
    heading.textContent = "Filters";
    const actions = document.createElement("div");
    actions.className = "sheet-actions";
    const doneButton = document.createElement("button");
    doneButton.type = "button";
    doneButton.textContent = "Done";
    actions.append(doneButton);
    content.append(heading, filtersPanel, actions);
    const dialog = createSheet(content);
    dialog.setAttribute("aria-labelledby", heading.id);
    dialog.addEventListener("close", () => {
      exportMenu.open = false;
      panelHome.insertBefore(filtersPanel, panelNextSibling);
    });
    doneButton.addEventListener("click", () => dialog.close());
    exportMenu.open = true;
    dialog.showModal();
  });
}

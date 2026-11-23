(function () {
  const storageKey = "scrapelog-theme";
  const themeColors = { light: "#EEF0EC", dark: "#21222C" };

  function readStoredTheme() {
    try {
      const storedTheme = localStorage.getItem(storageKey);
      return storedTheme === "light" || storedTheme === "dark" ? storedTheme : null;
    } catch {
      return null;
    }
  }

  function applyTheme(themeName) {
    document.documentElement.dataset.theme = themeName;
    document.querySelector('meta[name="theme-color"]').content = themeColors[themeName];
    const toggleButton = document.getElementById("theme-toggle");
    if (toggleButton) {
      toggleButton.setAttribute("aria-pressed", String(themeName === "dark"));
    }
  }

  const systemPrefersDark = matchMedia("(prefers-color-scheme: dark)").matches;
  applyTheme(readStoredTheme() || (systemPrefersDark ? "dark" : "light"));

  document.addEventListener("DOMContentLoaded", () => applyTheme(document.documentElement.dataset.theme));

  document.addEventListener("click", (clickEvent) => {
    if (!clickEvent.target.closest("#theme-toggle")) {
      return;
    }
    const nextTheme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    applyTheme(nextTheme);
    try {
      localStorage.setItem(storageKey, nextTheme);
    } catch {}
    document.dispatchEvent(new CustomEvent("themechange"));
  });
})();

document.getElementById("sign-out").addEventListener("click", async () => {
  try {
    const logoutResponse = await fetch("/ui/api/logout", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    });
    if (logoutResponse.ok) {
      location.assign("/login");
    }
  } catch {}
});

const grantedPermissions = document.querySelector(".topbar").dataset.permissions.split(" ");

export function can(permission) {
  return grantedPermissions.includes(permission);
}

const byteUnits = ["KiB", "MiB", "GiB", "TiB"];

export const networkErrorMessage = "Couldn't reach the server. Check your connection and try again.";

export async function apiFetch(path, options = {}) {
  const requestInit = { method: options.method || "GET" };
  if (options.body !== undefined) {
    requestInit.body = JSON.stringify(options.body);
    requestInit.headers = { "Content-Type": "application/json" };
  }
  let apiResponse;
  try {
    apiResponse = await fetch(path, requestInit);
  } catch {
    throw new Error(networkErrorMessage);
  }
  if (apiResponse.status === 401) {
    location.assign("/login?next=" + encodeURIComponent(location.pathname + location.search));
    return new Promise(() => {});
  }
  if (!apiResponse.ok) {
    let errorMessage = "";
    try {
      const errorBody = await apiResponse.json();
      errorMessage = errorBody && errorBody.error;
    } catch {}
    throw new Error(errorMessage || `Request failed with status ${apiResponse.status}.`);
  }
  if ((apiResponse.headers.get("Content-Type") || "").startsWith("application/json")) {
    return apiResponse.json();
  }
  return apiResponse.text();
}

export function formatBytes(byteCount) {
  if (byteCount < 1024) {
    return `${byteCount} B`;
  }
  let scaled = byteCount / 1024;
  let unitIndex = 0;
  while (scaled >= 1024 && unitIndex < byteUnits.length - 1) {
    scaled /= 1024;
    unitIndex += 1;
  }
  return `${scaled.toFixed(1)} ${byteUnits[unitIndex]}`;
}

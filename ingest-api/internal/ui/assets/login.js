const defaultDestination = "/requests";

function safeNextPath(candidate) {
  if (typeof candidate !== "string" || !candidate.startsWith("/")) {
    return defaultDestination;
  }
  let resolved;
  try {
    resolved = new URL(candidate, location.origin);
  } catch {
    return defaultDestination;
  }
  if (resolved.origin !== location.origin || resolved.pathname === "/login") {
    return defaultDestination;
  }
  return resolved.href;
}

const destination = safeNextPath(new URLSearchParams(location.search).get("next"));
const loginForm = document.getElementById("login-form");
const usernameInput = document.getElementById("login-username");
const passwordInput = document.getElementById("login-password");
const errorMessage = document.getElementById("login-error");
const submitButton = loginForm.querySelector("button[type=submit]");

loginForm.addEventListener("submit", async (submitEvent) => {
  submitEvent.preventDefault();
  submitButton.disabled = true;
  errorMessage.hidden = true;
  try {
    const loginResponse = await fetch("/ui/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: usernameInput.value, password: passwordInput.value }),
    });
    if (loginResponse.status === 204) {
      location.replace(destination);
      return;
    }
    let message = `Sign in failed with status ${loginResponse.status}. Try again.`;
    try {
      const responseBody = await loginResponse.json();
      if (responseBody && responseBody.error) {
        message = responseBody.error;
      }
    } catch {}
    errorMessage.textContent = message;
  } catch {
    errorMessage.textContent = "Couldn't reach the server. Check your connection and try again.";
  }
  errorMessage.hidden = false;
  submitButton.disabled = false;
});

try {
  const sessionResponse = await fetch("/ui/api/session");
  if (sessionResponse.status === 204) {
    location.replace(destination);
  }
} catch {}

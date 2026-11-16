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

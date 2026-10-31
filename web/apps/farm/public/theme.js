// Runs before first paint (index.html loads it without defer): sets the
// theme the user pinned, so a dark choice on a light OS doesn't flash. The
// same logic as @agentkit/ui's applyThemePreference, which takes over once
// the app has loaded. Keep the key in step with themeStorageKey.
try {
  var t = localStorage.getItem("agentkit.theme");
  if (t === "light" || t === "dark") document.documentElement.setAttribute("data-theme", t);
} catch {
  // Storage blocked: follow the OS.
}

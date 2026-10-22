// Applies the saved or system color theme before the first paint, so a
// dark page does not flash light while the app loads. src/theme/theme.ts
// owns the same key and keeps the attribute current afterwards.
(function () {
  var saved = null;
  try {
    saved = localStorage.getItem("tend.theme");
  } catch {
    // Storage may be unavailable; follow the system.
  }
  var dark =
    saved === "dark" ||
    (saved !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  if (dark) {
    document.body.setAttribute("data-dark", "");
  }
})();

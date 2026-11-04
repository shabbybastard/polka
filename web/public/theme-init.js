// Apply the saved theme before the SPA bundle loads, so a dark-theme
// user never sees a white flash on startup. Kept as an external script
// because the server's CSP forbids inline JavaScript.
(function () {
  var mode = "auto";
  try {
    var stored = localStorage.getItem("polka-theme");
    if (stored === "auto" || stored === "light" || stored === "dark") mode = stored;
  } catch (e) {
    /* localStorage can be unavailable in private mode */
  }
  document.documentElement.dataset.theme = mode;
})();

// Apply the saved theme before the page paints: "light", "dark", or
// "system" (default), which follows the OS and updates when it changes.
(function () {
  var mq = window.matchMedia("(prefers-color-scheme: dark)");
  function choice() {
    try { return localStorage.getItem("crc-theme") || "system"; } catch (e) { return "system"; }
  }
  function apply() {
    var t = choice();
    document.documentElement.classList.toggle("dark", t === "dark" || (t === "system" && mq.matches));
  }
  apply();
  mq.addEventListener("change", apply);
  window.__crcApplyTheme = apply;
})();

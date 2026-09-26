// Loaded synchronously in <head>: applies dark mode before first paint (no flash).
(function () { try { var t = localStorage.getItem("theme"); if (t === "dark" || (!t && matchMedia("(prefers-color-scheme: dark)").matches)) document.documentElement.classList.add("dark"); } catch (e) {} })();

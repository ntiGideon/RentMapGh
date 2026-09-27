// Chart hover: each day's column has a hit area wider than the bar; the
// tooltip shows the date and value. The table below carries every value
// too, so nothing depends on hovering.
for (const chart of document.querySelectorAll("[data-chart]")) {
  const tip = chart.querySelector("[data-chart-tip]");
  const show = (g) => {
    const box = g.getBoundingClientRect();
    const host = chart.getBoundingClientRect();
    tip.textContent = g.dataset.tip; // text only
    tip.style.left = box.left - host.left + box.width / 2 + "px";
    tip.style.top = Math.max(0, box.top - host.top + 8) + "px";
    tip.hidden = false;
  };
  for (const g of chart.querySelectorAll("g[data-tip]")) {
    g.setAttribute("tabindex", "0");
    g.addEventListener("pointerenter", () => show(g));
    g.addEventListener("focus", () => show(g));
    g.addEventListener("pointerleave", () => (tip.hidden = true));
    g.addEventListener("blur", () => (tip.hidden = true));
  }
}

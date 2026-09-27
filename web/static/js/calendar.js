// Week calendar: place hour lines, viewing-hour bands and viewings from
// their data-top / data-height percentages (the CSP forbids inline style
// attributes; setting styles from script is fine).
document.documentElement.classList.add("js");
for (const el of document.querySelectorAll("[data-calendar] [data-top]")) {
  el.style.top = el.dataset.top;
  if (el.dataset.height) el.style.height = el.dataset.height;
}

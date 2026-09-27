// Walk-through video on the photos step: send the file in 2 MB pieces, and
// when the connection drops, ask the server how much arrived and carry on.
// Without this script the same form posts the whole file in one go.
// The panel is replaced after uploads and deletes, so everything is wired
// by delegation from the document.
const CHUNK = 2 * 1024 * 1024;
const MAX_WAIT = 30000; // longest pause between retries

let active = null; // the upload in progress: { cancelled, xhr }

function els(form) {
  return {
    pick: form.querySelector("[data-video-pick]"),
    noJs: form.querySelector("[data-no-js]"),
    box: form.querySelector("[data-video-progress]"),
    status: form.querySelector("[data-video-status]"),
    bar: form.querySelector("[data-video-bar]"),
    error: form.querySelector("[data-video-error]"),
    input: form.querySelector("input[type=file]"),
  };
}

function mb(bytes) {
  return (bytes / 1048576).toFixed(bytes < 10485760 ? 1 : 0) + " MB";
}

function showProgress(ui, done, total) {
  const pct = Math.floor((done / total) * 100);
  ui.bar.style.width = pct + "%";
  ui.status.textContent = pct < 100 ? `Uploading ${pct}% · ${mb(done)} of ${mb(total)}` : "Checking the video…";
}

function reset(ui, message) {
  active = null;
  ui.box.hidden = !message;
  ui.pick.hidden = false;
  ui.bar.style.width = "0";
  ui.input.value = "";
  ui.error.hidden = !message;
  ui.error.textContent = message || "";
  if (message) ui.status.textContent = "Not uploaded";
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// send makes one request and resolves with the XHR (status 0 = no connection).
function send(job, method, url, body, onProgress) {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    job.xhr = xhr;
    xhr.open(method, url);
    xhr.setRequestHeader("HX-Request", "true");
    if (body instanceof Blob) xhr.setRequestHeader("Content-Type", "application/octet-stream");
    if (onProgress) xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded);
    xhr.onload = xhr.onerror = xhr.onabort = () => resolve(xhr);
    xhr.send(body);
  });
}

function offsetOf(xhr) {
  const n = Number(xhr.getResponseHeader("Upload-Offset"));
  return Number.isFinite(n) && n >= 0 ? n : null;
}

async function upload(form, file) {
  const ui = els(form);
  const job = { cancelled: false, xhr: null };
  active = job;
  ui.pick.hidden = true;
  ui.box.hidden = false;
  ui.error.hidden = true;
  ui.status.textContent = "Starting…";

  const refuse = (xhr) => reset(ui, xhr.responseText || "That video can't be used.");

  // Open the upload.
  let xhr = await send(job, "POST", form.dataset.startUrl, new URLSearchParams({ size: String(file.size) }));
  if (job.cancelled) return;
  if (xhr.status !== 201) {
    if (xhr.status === 0) return reset(ui, "No connection. Check your data and try again.");
    if (xhr.status === 429) return reset(ui, "Too many uploads. Wait a while, then try again.");
    return refuse(xhr);
  }
  const url = form.dataset.startUrl + "/" + JSON.parse(xhr.responseText).id;

  let offset = 0;
  let wait = 2000;
  let serverErrors = 0;
  while (!job.cancelled) {
    const piece = file.slice(offset, Math.min(offset + CHUNK, file.size));
    xhr = await send(job, "POST", url + "?offset=" + offset, piece, (n) => showProgress(ui, offset + n, file.size));
    if (job.cancelled) return;
    const s = xhr.status;
    if (s === 200) {
      wait = 2000;
      if (xhr.getResponseHeader("Upload-Complete") === "1") {
        active = null;
        htmx.swap("#video-panel", xhr.responseText, { swapStyle: "outerHTML" });
        return;
      }
      offset = offsetOf(xhr) ?? offset + piece.size;
      showProgress(ui, offset, file.size);
      continue;
    }
    if (s === 409) {
      offset = offsetOf(xhr) ?? offset;
      continue;
    }
    if (s === 422 || s === 410 || s === 404 || s === 413) return refuse(xhr);
    if (s === 401 || s === 403) return reset(ui, "Your session expired. Refresh the page and sign in again.");
    if (s >= 500 && ++serverErrors >= 3) return reset(ui, "Something went wrong on our side. Please try again later.");

    // No connection, rate limit or a server hiccup: wait, then ask where we are.
    for (let left = Math.ceil(wait / 1000); left > 0 && !job.cancelled; left--) {
      ui.status.textContent = `Connection lost · retrying in ${left}s (${mb(offset)} of ${mb(file.size)} sent)`;
      await sleep(1000);
    }
    wait = Math.min(wait * 2, MAX_WAIT);
    if (job.cancelled) return;
    const probe = await send(job, "GET", url);
    if (probe.status === 204) offset = offsetOf(probe) ?? offset;
    else if (probe.status === 410 || probe.status === 404) return refuse(probe);
  }
}

document.addEventListener("change", (e) => {
  const input = e.target.closest("[data-video-uploader] input[type=file]");
  if (!input || !input.files.length || active) return;
  const form = input.closest("[data-video-uploader]");
  const file = input.files[0];
  const ui = els(form);
  if (file.type && !file.type.startsWith("video/")) {
    reset(ui, "That file isn't a video.");
    return;
  }
  if (file.size > Number(form.dataset.maxBytes)) {
    reset(ui, `That video is ${mb(file.size)}. The limit is ${mb(Number(form.dataset.maxBytes))} — record a shorter one, or at 720p.`);
    return;
  }
  upload(form, file);
});

document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-video-cancel]");
  if (!btn) return;
  const form = btn.closest("[data-video-uploader]");
  if (active) {
    active.cancelled = true;
    active.xhr?.abort();
  }
  reset(els(form));
});

document.addEventListener("submit", (e) => {
  if (e.target.matches("[data-video-uploader]")) e.preventDefault();
});

window.addEventListener("beforeunload", (e) => {
  if (active) e.preventDefault();
});

// The one-shot submit button is only for browsers without this script.
function hideNoJs(root) {
  root.querySelectorAll?.("[data-video-uploader] [data-no-js]").forEach((b) => (b.hidden = true));
}
htmx.onLoad(hideNoJs);
hideNoJs(document);

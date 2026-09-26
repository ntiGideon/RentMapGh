// Photos step of the listing wizard: shrink photos on the phone, upload them
// one at a time with progress, and drag to reorder. Without this script the
// same page still works with plain forms (multi-file upload, arrow buttons).
const MAX_SIDE = 1600; // the largest size the server keeps
const QUALITY = 0.88;

const form = document.querySelector("[data-photo-uploader]");

function toast(kind, message) {
  document.body.dispatchEvent(new CustomEvent("showToast", { detail: { kind, message } }));
}

function photoCount() {
  return document.querySelectorAll("[data-photo-list] [data-photo-id]").length;
}

// Swap the server's fresh grid in (htmx processes its buttons and forms).
// While a photo is being dragged, the latest grid waits until it's dropped.
let dragging = false;
let pendingGrid = null;
function showGrid(html) {
  if (dragging) {
    pendingGrid = html;
    return;
  }
  htmx.swap("#photo-grid", html, { swapStyle: "outerHTML" });
}

// ── Shrinking ────────────────────────────────────────────────────────────
// Re-encode as JPEG ≤1600 px: a 4 MB phone photo becomes ~400 KB, which
// matters on a 3G data bundle. It also turns HEIC/AVIF (where the browser
// can decode them) into something the server accepts. The server re-checks
// and re-encodes everything anyway.
async function shrink(file) {
  const accepted = /^image\/(jpeg|png|webp)$/.test(file.type);
  if (!window.createImageBitmap) return file;
  let bmp;
  try {
    bmp = await createImageBitmap(file, { imageOrientation: "from-image" });
  } catch (_) {
    return file; // let the server explain what's wrong with it
  }
  const scale = Math.min(1, MAX_SIDE / Math.max(bmp.width, bmp.height));
  if (accepted && scale === 1 && file.size < 1500000) {
    bmp.close();
    return file;
  }
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(bmp.width * scale);
  canvas.height = Math.round(bmp.height * scale);
  const ctx = canvas.getContext("2d");
  ctx.fillStyle = "#fff"; // transparent PNGs would turn black as JPEG
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.drawImage(bmp, 0, 0, canvas.width, canvas.height);
  bmp.close();
  const blob = await new Promise((res) => canvas.toBlob(res, "image/jpeg", QUALITY));
  if (!blob || (accepted && blob.size >= file.size)) return file;
  return new File([blob], file.name.replace(/\.\w+$/, "") + ".jpg", { type: "image/jpeg" });
}

// ── Upload queue ─────────────────────────────────────────────────────────
// One at a time: on a slow link parallel uploads just compete, and the
// photos land in the order they were picked.
const queue = [];
let busy = false;

function makeTile(file) {
  const tpl = form.querySelector("template[data-upload-tile]");
  const tile = tpl.content.firstElementChild.cloneNode(true);
  const img = tile.querySelector("[data-preview]");
  img.src = URL.createObjectURL(file);
  img.onload = () => URL.revokeObjectURL(img.src);
  img.onerror = () => img.classList.add("invisible"); // e.g. HEIC on desktop
  form.querySelector("[data-upload-queue]").appendChild(tile);
  return tile;
}

function setStatus(tile, text) {
  tile.querySelector("[data-status]").textContent = text;
}

function setProgress(tile, pct) {
  tile.querySelector("[data-bar]").style.width = pct + "%";
}

function fail(job, message, canRetry) {
  const { tile } = job;
  const box = tile.querySelector("[data-failed]");
  box.textContent = message;
  box.classList.remove("hidden");
  box.classList.add("grid");
  setStatus(tile, canRetry ? "Not uploaded" : "Not added");
  setProgress(tile, 0);
  const retry = tile.querySelector("[data-retry]");
  const dismiss = tile.querySelector("[data-dismiss]");
  dismiss.hidden = false;
  dismiss.onclick = () => tile.remove();
  if (canRetry) {
    retry.hidden = false;
    retry.onclick = () => {
      retry.hidden = true;
      dismiss.hidden = true;
      box.classList.add("hidden");
      box.classList.remove("grid");
      setStatus(tile, "Waiting…");
      queue.push(job);
      pump();
    };
  }
}

function send(job, file) {
  return new Promise((resolve) => {
    const body = new FormData();
    body.append("photo", file, file.name);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", form.dataset.uploadUrl);
    xhr.setRequestHeader("HX-Request", "true");
    xhr.setRequestHeader("X-Photo-Upload", "1");
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;
      const pct = Math.round((e.loaded / e.total) * 100);
      setProgress(job.tile, pct);
      setStatus(job.tile, pct < 100 ? "Uploading " + pct + "%" : "Processing…");
    };
    xhr.onload = () => {
      const s = xhr.status;
      if (s === 200) {
        showGrid(xhr.responseText);
        job.tile.remove();
      } else if (s === 422 || s === 413) {
        fail(job, xhr.responseText || "That photo can't be used.", false);
      } else if (s === 429) {
        fail(job, "Too many uploads. Wait a minute, then try again.", true);
      } else if (s === 403 || s === 401) {
        fail(job, "Your session expired. Refresh the page and sign in again.", false);
      } else {
        fail(job, "Something went wrong on our side.", true);
      }
      resolve();
    };
    xhr.onerror = () => {
      fail(job, "No connection. Check your data and try again.", true);
      resolve();
    };
    xhr.send(body);
  });
}

async function pump() {
  if (busy) return;
  const job = queue.shift();
  if (!job) return;
  busy = true;
  setStatus(job.tile, "Preparing…");
  const file = await shrink(job.file);
  setStatus(job.tile, "Uploading…");
  await send(job, file);
  busy = false;
  pump();
}

function enqueue(files) {
  const max = Number(form.dataset.max) || 30;
  const pending = form.querySelectorAll("[data-upload-queue] > li").length;
  let room = max - photoCount() - pending;
  let skipped = 0;
  for (const file of files) {
    if (file.type && !file.type.startsWith("image/")) {
      skipped++;
      continue;
    }
    if (room <= 0) {
      toast("error", "You can add up to " + max + " photos.");
      break;
    }
    room--;
    queue.push({ file, tile: makeTile(file) });
  }
  if (skipped) toast("error", skipped === 1 ? "That file isn't a photo." : skipped + " files weren't photos.");
  pump();
}

// ── Reordering ───────────────────────────────────────────────────────────
function order(list) {
  return [...list.querySelectorAll(":scope > [data-photo-id]")].map((li) => li.dataset.photoId);
}

async function saveOrder(ids) {
  const body = new URLSearchParams();
  ids.forEach((id) => body.append("order", id));
  try {
    const res = await fetch(form.dataset.orderUrl, { method: "POST", body, headers: { "HX-Request": "true" } });
    if (!res.ok) throw new Error(String(res.status));
    showGrid(await res.text());
  } catch (_) {
    toast("error", "Couldn't save the new order. Check your connection.");
  }
}

// Pointer events cover mouse, touch and pen with one code path. The
// handle has touch-action: none, so dragging it doesn't scroll the page.
// The pointer is captured by the list, not the tile: moving the tile in the
// DOM would release a capture held by the tile itself.
document.addEventListener("pointerdown", (e) => {
  const handle = e.target.closest("[data-drag-handle]");
  if (!handle || e.button > 0 || dragging) return;
  const item = handle.closest("[data-photo-id]");
  const list = item.parentElement;
  e.preventDefault();
  list.setPointerCapture(e.pointerId);
  dragging = true;
  item.classList.add("is-dragging");
  const before = order(list).join();

  const move = (ev) => {
    if (ev.clientY < 70) window.scrollBy(0, -12);
    else if (ev.clientY > window.innerHeight - 70) window.scrollBy(0, 12);
    const over = document.elementFromPoint(ev.clientX, ev.clientY)?.closest("[data-photo-id]");
    if (!over || over === item || over.parentElement !== list) return;
    const items = [...list.children];
    list.insertBefore(item, items.indexOf(item) < items.indexOf(over) ? over.nextSibling : over);
  };
  const end = () => {
    list.removeEventListener("pointermove", move);
    list.removeEventListener("pointerup", end);
    list.removeEventListener("pointercancel", end);
    item.classList.remove("is-dragging");
    dragging = false;
    const after = order(list);
    const pending = pendingGrid;
    pendingGrid = null;
    if (after.join() !== before) saveOrder(after); // its answer includes any upload that landed meanwhile
    else if (pending) showGrid(pending);
  };
  list.addEventListener("pointermove", move);
  list.addEventListener("pointerup", end);
  list.addEventListener("pointercancel", end);
});

// ── Wiring ───────────────────────────────────────────────────────────────
if (form) {
  form.querySelector("[data-no-js]").hidden = true;
  const input = form.querySelector("input[type=file]");
  input.addEventListener("change", () => {
    enqueue([...input.files]);
    input.value = ""; // so picking the same photo again still fires
  });
  form.addEventListener("submit", (e) => e.preventDefault());

  const zone = form.querySelector("[data-dropzone]");
  const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  zone.addEventListener("dragover", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    zone.classList.add("is-over");
  });
  zone.addEventListener("dragleave", () => zone.classList.remove("is-over"));
  zone.addEventListener("drop", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    zone.classList.remove("is-over");
    enqueue([...e.dataTransfer.files]);
  });
  // A photo dropped just outside the box shouldn't navigate away from the form.
  window.addEventListener("dragover", (e) => hasFiles(e) && e.preventDefault());
  window.addEventListener("drop", (e) => hasFiles(e) && e.preventDefault());

  window.addEventListener("beforeunload", (e) => {
    if (busy || queue.length) e.preventDefault();
  });
}

// Drag handles only work with this script, so they start hidden.
htmx.onLoad((el) => {
  el.querySelectorAll?.("[data-drag-handle]").forEach((h) => (h.hidden = false));
});
document.querySelectorAll("[data-drag-handle]").forEach((h) => (h.hidden = false));

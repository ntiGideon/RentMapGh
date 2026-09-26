// RentMap app shell: theme toggle, toasts, data-saver toggle.
// Plain JS, no build step, no eval (strict CSP). Keep this file tiny.
(function () {
  "use strict";

  const root = document.documentElement;

  // ── Theme ────────────────────────────────────────────────────────
  function setTheme(dark) {
    root.classList.toggle("dark", dark);
    try { localStorage.setItem("theme", dark ? "dark" : "light"); } catch (_) {}
    document.querySelectorAll("[data-theme-toggle]").forEach((b) => b.setAttribute("aria-pressed", String(dark)));
  }

  // ── Toasts (server fires them via `HX-Trigger: {"showToast": {...}}`) ─
  const toastTone = {
    success: "bg-graphite-900 text-white dark:bg-mint-200 dark:text-graphite-900",
    error: "bg-danger text-white",
    info: "bg-graphite-900 text-white dark:bg-graphite-800",
  };

  function showToast(kind, message) {
    const host = document.getElementById("toasts");
    if (!host || !message) return;
    const el = document.createElement("div");
    el.className = "pointer-events-auto max-w-md rounded-lg px-3.5 py-2.5 text-sm font-medium shadow-float animate-rise " + (toastTone[kind] || toastTone.info);
    el.setAttribute("role", kind === "error" ? "alert" : "status");
    el.textContent = message;
    host.appendChild(el);
    setTimeout(() => {
      el.classList.add("opacity-0", "transition-opacity", "duration-300");
      setTimeout(() => el.remove(), 320);
    }, 4000);
  }

  document.body.addEventListener("showToast", (e) => showToast(e.detail.kind, e.detail.message));

  // Network/server errors on htmx requests that don't swap anything.
  document.body.addEventListener("htmx:sendError", () => showToast("error", "You seem to be offline. Check your connection and try again."));
  document.body.addEventListener("htmx:responseError", (e) => {
    const s = e.detail.xhr.status;
    if (s === 429) showToast("error", "Too many attempts. Please wait a minute and try again.");
    else if (s === 403) showToast("error", "Your session expired. Refresh the page and try again.");
    else if (s >= 500) showToast("error", "Something went wrong on our side. Please try again.");
  });

  // ── Delegated clicks ─────────────────────────────────────────────
  document.addEventListener("click", (e) => {
    const t = e.target.closest("[data-theme-toggle]");
    if (t) {
      setTheme(!root.classList.contains("dark"));
      return;
    }
    const ds = e.target.closest("[data-saver-toggle]");
    if (ds) {
      const on = !root.classList.contains("data-saver");
      document.cookie = "ds=" + (on ? "1" : "0") + "; path=/; max-age=31536000; samesite=lax";
      location.reload();
    }
  });

  document.querySelectorAll("[data-theme-toggle]").forEach((b) => b.setAttribute("aria-pressed", String(root.classList.contains("dark"))));

  // ── OTP code input: digits only, submit once all 6 are in ─────────
  document.addEventListener("input", (e) => {
    const el = e.target;
    if (!el.matches || !el.matches("[data-otp-input]")) return;
    const digits = el.value.replace(/\D/g, "").slice(0, 6);
    if (el.value !== digits) el.value = digits;
    if (digits.length === 6 && el.form && !el.form.dataset.submitted) {
      el.form.dataset.submitted = "1";
      el.form.requestSubmit();
    }
  });

  // ── Countdown buttons (e.g. "Resend code"): disabled until a time ──
  function initCountdowns(scope) {
    scope.querySelectorAll("[data-countdown-until]").forEach((btn) => {
      if (btn.dataset.countdownInit) return;
      btn.dataset.countdownInit = "1";
      const until = Number(btn.dataset.countdownUntil) * 1000;
      const label = btn.textContent.trim();
      const tick = () => {
        const left = Math.ceil((until - Date.now()) / 1000);
        if (left <= 0) {
          btn.disabled = false;
          btn.textContent = label;
          return;
        }
        btn.disabled = true;
        btn.textContent = label + " in " + left + "s";
        setTimeout(tick, 1000);
      };
      tick();
    });
    const otp = scope.querySelector("[data-otp-input]");
    if (otp && scope !== document) otp.focus();
  }
  initCountdowns(document);

  // ── Photo uploads: shrink on the phone before sending (saves data on
  // 3G), preview in place. The server re-validates and re-encodes anyway.
  async function shrink(file, max) {
    if (!/^image\/(jpeg|png|webp)$/.test(file.type) || !window.createImageBitmap) return file;
    let bmp;
    try {
      bmp = await createImageBitmap(file, { imageOrientation: "from-image" });
    } catch (_) {
      return file;
    }
    const scale = Math.min(1, max / Math.max(bmp.width, bmp.height));
    if (scale === 1 && file.size < 1500000) return file;
    const canvas = document.createElement("canvas");
    canvas.width = Math.round(bmp.width * scale);
    canvas.height = Math.round(bmp.height * scale);
    canvas.getContext("2d").drawImage(bmp, 0, 0, canvas.width, canvas.height);
    const blob = await new Promise((res) => canvas.toBlob(res, "image/jpeg", 0.85));
    if (!blob || blob.size >= file.size) return file;
    return new File([blob], file.name.replace(/\.\w+$/, "") + ".jpg", { type: "image/jpeg" });
  }

  function replaceFile(input, file) {
    try {
      const dt = new DataTransfer();
      dt.items.add(file);
      input.files = dt.files;
    } catch (_) {} // old browsers: keep the original file
  }

  document.addEventListener("change", async (e) => {
    const input = e.target;
    if (!input.matches) return;

    if (input.matches("[data-photo-input]") && input.files && input.files[0]) {
      const tile = input.closest("label");
      const file = await shrink(input.files[0], 1600);
      replaceFile(input, file);
      const img = tile && tile.querySelector("[data-photo-preview]");
      if (img) {
        if (img.src) URL.revokeObjectURL(img.src);
        img.src = URL.createObjectURL(file);
        img.classList.remove("hidden");
        tile.querySelector("[data-photo-empty]").classList.add("invisible");
        tile.querySelector("[data-photo-change]").classList.remove("hidden");
      }
    }

    if (input.matches("[data-avatar-input]") && input.files && input.files[0]) {
      replaceFile(input, await shrink(input.files[0], 800));
      input.form.requestSubmit();
    }
  });

  // Upload progress for multipart forms sent by htmx.
  document.body.addEventListener("htmx:xhr:progress", (e) => {
    const form = e.target.closest && e.target.closest("[data-upload-form]");
    const box = form && form.querySelector("[data-upload-progress]");
    if (!box || !e.detail.lengthComputable) return;
    const pct = Math.round((e.detail.loaded / e.detail.total) * 100);
    box.classList.remove("hidden");
    box.querySelector("[data-upload-bar]").style.width = pct + "%";
    box.querySelector("[data-upload-percent]").textContent = pct + "%";
    if (pct >= 100) box.querySelector("[data-upload-label]").textContent = "Checking photos…";
  });
  document.body.addEventListener("htmx:afterSwap", (e) => initCountdowns(e.detail.elt.parentElement || document));
})();

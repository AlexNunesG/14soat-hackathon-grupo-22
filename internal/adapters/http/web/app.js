// FIAP X Video Processor web UI. Plain JavaScript, served by the api from
// the same origin as /api/v1, so there is no CORS. The access token lives in
// sessionStorage: it is gone when the tab is closed.
"use strict";

(function () {
  const API = "/api/v1";
  const POLL_MS = 2000;
  const TOKEN_KEY = "vp.token";
  const EXPIRES_KEY = "vp.expires";
  const EMAIL_KEY = "vp.email";

  const $ = (id) => document.getElementById(id);
  const state = { page: 1, pageSize: 20, total: 0, timer: null, loading: false };

  // --- session -------------------------------------------------------------

  function storage() {
    try { return window.sessionStorage; } catch (_) { return null; }
  }
  function token() {
    const s = storage();
    if (!s) return null;
    const t = s.getItem(TOKEN_KEY);
    const exp = Number(s.getItem(EXPIRES_KEY) || 0);
    if (!t || (exp && Date.now() >= exp)) return null;
    return t;
  }
  function saveSession(accessToken, expiresIn, email) {
    const s = storage();
    if (!s) return;
    s.setItem(TOKEN_KEY, accessToken);
    s.setItem(EXPIRES_KEY, String(Date.now() + expiresIn * 1000));
    s.setItem(EMAIL_KEY, email);
  }
  function clearSession() {
    const s = storage();
    if (s) [TOKEN_KEY, EXPIRES_KEY, EMAIL_KEY].forEach((k) => s.removeItem(k));
  }

  // --- API helpers ---------------------------------------------------------

  class ApiError extends Error {
    constructor(status, code, message) {
      super(message);
      this.status = status;
      this.code = code;
    }
  }

  // errorFrom turns a failed response into an ApiError, using the error
  // envelope {"error": {"code", "message"}} when there is one.
  async function errorFrom(resp) {
    let code = "http_" + resp.status;
    let message = "Request failed (HTTP " + resp.status + ").";
    try {
      const body = await resp.json();
      if (body && body.error) {
        code = body.error.code || code;
        message = body.error.message || message;
      }
    } catch (_) { /* not JSON */ }
    return new ApiError(resp.status, code, message);
  }

  async function api(path, options) {
    const opts = Object.assign({ headers: {} }, options || {});
    const t = token();
    if (t) opts.headers["Authorization"] = "Bearer " + t;
    let resp;
    try {
      resp = await fetch(API + path, opts);
    } catch (_) {
      throw new ApiError(0, "network", "Cannot reach the server. Check your connection and try again.");
    }
    if (resp.status === 401 && t) {
      sessionExpired();
      throw new ApiError(401, "unauthorized", "Your session has expired. Please log in again.");
    }
    if (!resp.ok) throw await errorFrom(resp);
    return resp;
  }

  function postJSON(path, body) {
    return api(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }

  // --- UI helpers ----------------------------------------------------------

  function notice(message, isError) {
    const el = $("notice");
    el.textContent = message;
    el.classList.toggle("error", !!isError);
    el.hidden = !message;
  }
  function formError(form, message) {
    const el = form.querySelector(".form-error");
    el.textContent = message || "";
    el.hidden = !message;
  }
  function busy(form, isBusy) {
    form.querySelectorAll("button, input").forEach((el) => { el.disabled = isBusy; });
  }
  function formatDate(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    return isNaN(d) ? iso : d.toLocaleString();
  }
  function cell(row, text, className, label) {
    const td = document.createElement("td");
    td.textContent = text;
    if (className) td.className = className;
    if (label) td.dataset.label = label;
    row.appendChild(td);
    return td;
  }

  // --- views ---------------------------------------------------------------

  function render() {
    const signedIn = !!token();
    $("auth-view").hidden = signedIn;
    $("app-view").hidden = !signedIn;
    $("session").hidden = !signedIn;
    if (signedIn) {
      const s = storage();
      $("session-user").textContent = (s && s.getItem(EMAIL_KEY)) || "";
      loadVideos();
    } else {
      stopPolling();
      $("videos-body").replaceChildren();
    }
  }

  function sessionExpired() {
    clearSession();
    render();
    notice("Your session has expired. Please log in again.", true);
  }

  // --- auth ----------------------------------------------------------------

  async function login(email, password) {
    const resp = await postJSON("/auth/login", { email: email, password: password });
    const body = await resp.json();
    saveSession(body.access_token, body.expires_in, email.trim().toLowerCase());
  }

  $("login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const form = ev.currentTarget;
    formError(form, "");
    busy(form, true);
    try {
      await login(form.email.value, form.password.value);
      form.reset();
      notice("");
      render();
    } catch (err) {
      formError(form, err.message);
    } finally {
      busy(form, false);
    }
  });

  $("register-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const form = ev.currentTarget;
    formError(form, "");
    busy(form, true);
    try {
      await postJSON("/auth/register", {
        name: form.name.value, email: form.email.value, password: form.password.value,
      });
      await login(form.email.value, form.password.value);
      form.reset();
      notice("Account created. Welcome!");
      render();
    } catch (err) {
      formError(form, err.message);
    } finally {
      busy(form, false);
    }
  });

  $("logout-button").addEventListener("click", () => {
    clearSession();
    notice("You have been logged out.");
    render();
  });

  // --- upload --------------------------------------------------------------

  // upload sends the files with XMLHttpRequest, which reports progress.
  function upload(files, onProgress) {
    return new Promise((resolve, reject) => {
      const data = new FormData();
      for (const f of files) data.append("videos", f, f.name);
      const xhr = new XMLHttpRequest();
      xhr.open("POST", API + "/videos");
      xhr.setRequestHeader("Authorization", "Bearer " + token());
      xhr.responseType = "json";
      xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); };
      xhr.onerror = () => reject(new ApiError(0, "network", "Upload failed: cannot reach the server."));
      xhr.onload = () => {
        const body = xhr.response;
        if (xhr.status === 202) return resolve(body);
        if (xhr.status === 401) sessionExpired();
        const e = body && body.error;
        reject(new ApiError(xhr.status, e ? e.code : "http_" + xhr.status,
          e && e.message ? e.message : "Upload failed (HTTP " + xhr.status + ")."));
      };
      xhr.send(data);
    });
  }

  $("upload-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const form = ev.currentTarget;
    const input = $("upload-files");
    const progress = $("upload-progress");
    formError(form, "");
    if (!input.files || input.files.length === 0) {
      formError(form, "Choose at least one video file.");
      input.focus();
      return;
    }
    busy(form, true);
    progress.value = 0;
    progress.hidden = false;
    try {
      const body = await upload(Array.from(input.files), (p) => { progress.value = Math.round(p * 100); });
      const n = body && body.videos ? body.videos.length : input.files.length;
      notice(n + (n === 1 ? " video" : " videos") + " queued for processing.");
      form.reset();
      state.page = 1;
      await loadVideos();
    } catch (err) {
      formError(form, err.message);
    } finally {
      busy(form, false);
      progress.hidden = true;
    }
  });

  // --- video list ----------------------------------------------------------

  function stopPolling() {
    if (state.timer) clearTimeout(state.timer);
    state.timer = null;
    $("polling").hidden = true;
  }

  async function loadVideos() {
    if (!token() || state.loading) return;
    state.loading = true;
    stopPolling();
    const listError = $("list-error");
    try {
      const resp = await api("/videos?page=" + state.page + "&page_size=" + state.pageSize);
      const body = await resp.json();
      listError.hidden = true;
      state.total = body.total;
      const pages = Math.max(1, Math.ceil(body.total / state.pageSize));
      if (state.page > pages) {
        // The page emptied (e.g. the page size grew): show the last one.
        state.page = pages;
        state.loading = false;
        return await loadVideos();
      }
      renderVideos(body.items);
      renderPager(pages);
      const active = body.items.some((v) => v.status === "PENDING" || v.status === "PROCESSING");
      if (active) schedulePoll();
    } catch (err) {
      if (err.status !== 401) {
        listError.textContent = err.message;
        listError.hidden = false;
        schedulePoll(); // try again: the server may be back soon
      }
    } finally {
      state.loading = false;
    }
  }

  function schedulePoll() {
    if (!token()) return;
    $("polling").hidden = false;
    state.timer = setTimeout(loadVideos, POLL_MS);
  }

  function renderVideos(items) {
    const body = $("videos-body");
    const rows = items.map((v) => {
      const tr = document.createElement("tr");
      tr.dataset.id = v.id;
      cell(tr, v.original_name, "name", "File");
      const status = cell(tr, "", "", "Status");
      const badge = document.createElement("span");
      badge.className = "badge " + v.status;
      badge.textContent = v.status;
      status.appendChild(badge);
      cell(tr, v.frame_count == null ? "—" : String(v.frame_count), "num", "Frames");
      cell(tr, v.error_message || "", v.error_message ? "error" : "", v.error_message ? "Error" : "");
      cell(tr, formatDate(v.created_at), "date", "Created");
      cell(tr, formatDate(v.updated_at), "date", "Updated");
      const actions = cell(tr, "", "", "");
      if (v.status === "DONE") {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "button small";
        btn.textContent = "Download";
        btn.setAttribute("aria-label", "Download the frames of " + v.original_name);
        btn.addEventListener("click", () => download(v, btn));
        actions.appendChild(btn);
      }
      return tr;
    });
    body.replaceChildren(...rows);
    $("empty").hidden = items.length > 0 || state.total > 0;
  }

  function renderPager(pages) {
    $("page-info").textContent = "Page " + state.page + " of " + pages + " · " + state.total +
      (state.total === 1 ? " video" : " videos");
    $("prev-page").disabled = state.page <= 1;
    $("next-page").disabled = state.page >= pages;
  }

  $("prev-page").addEventListener("click", () => { if (state.page > 1) { state.page--; loadVideos(); } });
  $("next-page").addEventListener("click", () => { state.page++; loadVideos(); });
  $("page-size").addEventListener("change", (ev) => {
    state.pageSize = Number(ev.target.value) || 20;
    state.page = 1;
    loadVideos();
  });
  $("refresh-button").addEventListener("click", () => loadVideos());

  // Poll again at once when the tab comes back to the foreground.
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && state.timer) loadVideos();
  });

  // --- download ------------------------------------------------------------

  // filenameFrom reads the file name of a Content-Disposition header,
  // preferring the RFC 5987 filename* parameter.
  function filenameFrom(header, fallback) {
    if (!header) return fallback;
    const star = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(header);
    if (star) {
      try { return decodeURIComponent(star[1].trim()); } catch (_) { /* fall through */ }
    }
    const plain = /filename\s*=\s*"([^"]*)"/i.exec(header) || /filename\s*=\s*([^;]+)/i.exec(header);
    return plain ? plain[1].trim() : fallback;
  }

  async function download(video, button) {
    button.disabled = true;
    const label = button.textContent;
    button.textContent = "Downloading…";
    try {
      const resp = await api("/videos/" + encodeURIComponent(video.id) + "/download");
      const blob = await resp.blob();
      const name = filenameFrom(resp.headers.get("Content-Disposition"), video.original_name + "_frames.zip");
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (err) {
      if (err.status !== 401) notice("Download failed: " + err.message, true);
    } finally {
      button.disabled = false;
      button.textContent = label;
    }
  }

  render();
})();

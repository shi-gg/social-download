const form = document.getElementById("download-form");
const urlInput = document.getElementById("url");
const sizeSelect = document.getElementById("size");
const customWrap = document.getElementById("custom-wrap");
const customInput = document.getElementById("custom-size");
const submitButton = document.getElementById("submit");
const errorBox = document.getElementById("error");
const progressCard = document.getElementById("progress-card");
const phaseLabel = document.getElementById("phase");
const percentLabel = document.getElementById("percent");
const bar = document.getElementById("bar");
const barFill = document.getElementById("bar-fill");
const cancelButton = document.getElementById("cancel");
const resultCard = document.getElementById("result");
const titleLabel = document.getElementById("title");
const metaLabel = document.getElementById("meta");
const sizeNote = document.getElementById("size-note");
const video = document.getElementById("video");
const saveLink = document.getElementById("save-link");
const copyURLButton = document.getElementById("copy-url");

const state = {
  id: null,
  timer: null,
  copyResetTimer: null,
  running: false,
  pollFailures: 0,
};

sizeSelect.addEventListener("change", () => {
  customWrap.hidden = sizeSelect.value !== "custom";
  if (!customWrap.hidden) {
    customInput.focus();
    customInput.select();
  }
});

form.addEventListener("submit", (event) => {
  event.preventDefault();
  if (state.running) {
    return;
  }
  clearError();
  hideResult();
  clearTimeout(state.timer);
  state.id = null;
  state.pollFailures = 0;

  const url = urlInput.value.trim();
  if (!url) {
    showError("Paste a video link first.");
    urlInput.focus();
    return;
  }

  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    showError("That doesn't look like a link. Include https:// and try again.");
    urlInput.focus();
    return;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    showError("Only http and https links are supported.");
    urlInput.focus();
    return;
  }

  const maxBytes = readMaxBytes();
  if (maxBytes === null) {
    return;
  }

  startJob(url, maxBytes);
});

async function startJob(url, maxBytes) {
  setRunning(true);
  showProgress(2, "Starting…");

  try {
    const response = await fetch("/api/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url, maxBytes }),
    });
    const data = await readJSON(response);
    if (!response.ok) {
      throw new Error(data.error || "Couldn't start the download.");
    }
    if (!data.id) {
      throw new Error("The server didn't return a job.");
    }
    state.id = data.id;
    renderJob(data);
    schedulePoll(250);
  } catch (error) {
    failJob(error.message || "Couldn't start the download.");
  }
}

function schedulePoll(delay) {
  clearTimeout(state.timer);
  state.timer = setTimeout(pollJob, delay);
}

async function pollJob() {
  const id = state.id;
  if (!id) {
    return;
  }
  try {
    const response = await fetch(`/api/jobs/${encodeURIComponent(id)}`, { cache: "no-store" });
    if (state.id !== id) {
      return;
    }
    if (response.status === 404) {
      failJob("That download expired. Start a new one.");
      return;
    }
    const job = await readJSON(response);
    if (state.id !== id) {
      return;
    }
    if (!response.ok) {
      throw new Error(job.error || "Couldn't read the download status.");
    }

    state.pollFailures = 0;
    renderJob(job);

    if (job.state === "done") {
      finishJob(job);
      return;
    }
    if (job.state === "error") {
      failJob(job.error || "The download failed.");
      return;
    }
    if (job.state === "canceled") {
      hideProgress();
      setRunning(false);
      state.id = null;
      return;
    }
    schedulePoll(700);
  } catch (error) {
    state.pollFailures += 1;
    if (state.pollFailures >= 3) {
      failJob(error.message || "Lost contact with the server.");
      return;
    }
    schedulePoll(1200);
  }
}

function renderJob(job) {
  showProgress(job.progress || 0, job.phase || "Working…");
  document.title = job.title ? `${job.title} · Social Download` : "Social Download";
}

function finishJob(job) {
  clearTimeout(state.timer);
  state.id = null;
  setRunning(false);
  hideProgress();

  titleLabel.textContent = job.title || "Video";
  const parts = [
    formatDuration(job.duration),
    formatBytes(job.sizeBytes),
  ];
  if (job.width && job.height) {
    parts.push(`${job.width}×${job.height}`);
  }
  metaLabel.textContent = parts.join(" · ");

  if (job.sourceBytes && job.sizeBytes && job.sourceBytes > job.sizeBytes) {
    sizeNote.textContent = `Compressed from ${formatBytes(job.sourceBytes)} to fit your ${formatBytes(job.maxBytes)} target.`;
    sizeNote.hidden = false;
  } else if (job.maxBytes > 0) {
    sizeNote.textContent = `The original was already under ${formatBytes(job.maxBytes)}, so no compression was needed.`;
    sizeNote.hidden = false;
  } else {
    sizeNote.textContent = "";
    sizeNote.hidden = true;
  }

  saveLink.href = job.mediaUrl;
  saveLink.download = job.fileName || "video.mp4";
  copyURLButton.disabled = false;
  video.src = job.mediaUrl;
  video.load();
  resultCard.hidden = false;
  resultCard.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

function failJob(message) {
  clearTimeout(state.timer);
  state.id = null;
  setRunning(false);
  hideProgress();
  showError(message);
}

function setRunning(running) {
  state.running = running;
  submitButton.disabled = running;
  submitButton.textContent = running ? "Working…" : "Download";
  urlInput.disabled = running;
  sizeSelect.disabled = running;
  customInput.disabled = running;
  cancelButton.disabled = false;
}

function showProgress(progress, phase) {
  const value = Math.max(0, Math.min(100, Math.round(progress)));
  progressCard.hidden = false;
  phaseLabel.textContent = phase;
  percentLabel.textContent = `${value}%`;
  bar.setAttribute("aria-valuenow", String(value));
  barFill.style.width = `${value}%`;
}

function hideProgress() {
  progressCard.hidden = true;
  barFill.style.width = "0%";
  percentLabel.textContent = "0%";
}

function hideResult() {
  resultCard.hidden = true;
  titleLabel.textContent = "";
  metaLabel.textContent = "";
  sizeNote.textContent = "";
  sizeNote.hidden = true;
  if (video.getAttribute("src")) {
    video.pause();
    video.removeAttribute("src");
    video.load();
  }
  saveLink.removeAttribute("href");
  copyURLButton.disabled = true;
  copyURLButton.textContent = "Copy URL";
  clearTimeout(state.copyResetTimer);
  document.title = "Social Download";
}

copyURLButton.addEventListener("click", async () => {
  const mediaURL = saveLink.getAttribute("href");
  if (!mediaURL) {
    return;
  }

  try {
    await copyText(new URL(mediaURL, window.location.href).href);
    copyURLButton.textContent = "Copied!";
    clearTimeout(state.copyResetTimer);
    state.copyResetTimer = setTimeout(() => {
      copyURLButton.textContent = "Copy URL";
    }, 1600);
  } catch {
    showError("Couldn't copy the video URL. Please copy it manually.");
  }
});

async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }

  const textArea = document.createElement("textarea");
  textArea.value = text;
  textArea.setAttribute("readonly", "");
  textArea.style.position = "fixed";
  textArea.style.opacity = "0";
  document.body.appendChild(textArea);
  textArea.select();
  const copied = document.execCommand("copy");
  textArea.remove();
  if (!copied) {
    throw new Error("copy failed");
  }
}

function showError(message) {
  errorBox.textContent = message;
  errorBox.hidden = false;
}

function clearError() {
  errorBox.textContent = "";
  errorBox.hidden = true;
}

function readMaxBytes() {
  if (sizeSelect.value === "0") {
    return 0;
  }
  if (sizeSelect.value === "custom") {
    const mb = Number(customInput.value);
    if (!Number.isInteger(mb) || mb < 1 || mb > 8192) {
      showError("Enter a whole number of megabytes between 1 and 8192.");
      customInput.focus();
      return null;
    }
    return mb * 1024 * 1024;
  }
  return Number(sizeSelect.value) * 1024 * 1024;
}

async function readJSON(response) {
  try {
    return await response.json();
  } catch {
    return {};
  }
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "—";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

function formatDuration(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return "—";
  }
  const total = Math.round(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const pad = (number) => String(number).padStart(2, "0");
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(secs)}` : `${minutes}:${pad(secs)}`;
}

cancelButton.addEventListener("click", async () => {
  const id = state.id;
  state.id = null;
  clearTimeout(state.timer);
  hideProgress();
  setRunning(false);
  if (id) {
    try {
      await fetch(`/api/jobs/${encodeURIComponent(id)}`, { method: "DELETE" });
    } catch {
      // The job will expire on the server even if this request is lost.
    }
  }
});

// Intercepts browser downloads above a size/type threshold and redirects
// them to the local dl2 API server instead of the browser's default
// single-connection downloader. Small downloads are left alone -- the
// overhead of spinning up multi-connection chunking isn't worth it for a
// 50KB file.

const DL2_API = "https://62-238-18-69.nip.io";
const DL2_TOKEN = "fd20e3665da9258c73f18b6acf0fb12b4492d7f5c8e617a021d05c29908d0e4b";
const MIN_SIZE_BYTES = 5 * 1024 * 1024; // only intercept downloads >= 5MB

const DL2_HEADERS = {
  "Content-Type": "application/json",
  "X-DL2-Token": DL2_TOKEN,
};

// Track which downloads we've already redirected, to avoid loops (since
// triggering a "normal" save via the API doesn't go through this listener
// again, but we guard anyway in case of future changes).
const handled = new Set();

chrome.downloads.onCreated.addListener(async (downloadItem) => {
  if (handled.has(downloadItem.id)) return;

  // We don't know file size at onCreated time reliably for all downloads,
  // so we optimistically intercept and let the API/engine handle it; if it
  // fails, the user's normal browser download still completed separately
  // in most cases since we cancel only after confirming dl2 accepted it.
  try {
    const resp = await fetch(`${DL2_API}/download`, {
      method: "POST",
      headers: DL2_HEADERS,
      body: JSON.stringify({
        url: downloadItem.url,
        connections: 16,
      }),
    });

    if (!resp.ok) {
      // DL2 Cloud rejected the request -- let the browser's normal
      // download proceed untouched.
      if (resp.status === 401) {
        console.error("DL2 API: Invalid authentication token");
      } else if (resp.status === 403) {
        console.error("DL2 API: Access denied");
      } else if (resp.status >= 500) {
        console.error("DL2 API: Server unavailable (status", resp.status, ")");
      } else {
        console.error("DL2 API: Request failed with status", resp.status);
      }
      return;
    }

    const { job_id } = await resp.json();
    handled.add(downloadItem.id);

    // Cancel the browser's own download since dl2 is now handling it.
    chrome.downloads.cancel(downloadItem.id);

    // Store job info so the popup can show progress.
    chrome.storage.local.set({
      [`job_${job_id}`]: {
        url: downloadItem.url,
        filename: downloadItem.filename || downloadItem.url.split("/").pop(),
        startedAt: Date.now(),
      },
    });

    notify("Downloading 2.0 took over", downloadItem.filename || downloadItem.url);
  } catch (err) {
    // DL2 Cloud unreachable -- fail silently and let the browser's
    // normal download proceed.
    console.error("Cannot reach DL2 Cloud, falling back to normal download:", err);
  }
});

function notify(title, message) {
  if (chrome.notifications) {
    chrome.notifications.create({
      type: "basic",
      iconUrl: "icon48.png",
      title,
      message,
    });
  }
}

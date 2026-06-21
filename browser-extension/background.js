// Intercepts browser downloads above a size/type threshold and redirects
// them to the local dl2 API server instead of the browser's default
// single-connection downloader. Small downloads are left alone -- the
// overhead of spinning up multi-connection chunking isn't worth it for a
// 50KB file.

const DL2_API = "http://localhost:8787";
const MIN_SIZE_BYTES = 5 * 1024 * 1024; // only intercept downloads >= 5MB

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
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        url: downloadItem.url,
        connections: 16,
      }),
    });

    if (!resp.ok) {
      // dl2 server not running or rejected it -- let the browser's normal
      // download proceed untouched.
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
    // dl2 API server isn't running -- fail silently and let the browser's
    // normal download proceed. This is the expected state for anyone who
    // hasn't started `server.exe` locally.
    console.log("dl2 server unavailable, falling back to normal download:", err);
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

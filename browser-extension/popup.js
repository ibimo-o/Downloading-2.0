const DL2_API = "https://62-238-18-69.nip.io";
const DL2_TOKEN = "fd20e3665da9258c73f18b6acf0fb12b4492d7f5c8e617a021d05c29908d0e4b";

const DL2_HEADERS = {
  "Content-Type": "application/json",
  "X-DL2-Token": DL2_TOKEN,
};

async function refresh() {
  const statusEl = document.getElementById("serverStatus");
  const jobsEl = document.getElementById("jobs");

  let jobs = [];
  try {
    const resp = await fetch(`${DL2_API}/jobs`, {
      headers: DL2_HEADERS,
    });
    if (!resp.ok) {
      if (resp.status === 401) {
        statusEl.textContent = "Invalid DL2 API token";
        statusEl.className = "status err";
        jobsEl.innerHTML = "";
        return;
      } else if (resp.status === 403) {
        statusEl.textContent = "Access denied to DL2 Cloud";
        statusEl.className = "status err";
        jobsEl.innerHTML = "";
        return;
      } else if (resp.status >= 500) {
        statusEl.textContent = "DL2 Cloud unavailable";
        statusEl.className = "status err";
        jobsEl.innerHTML = "";
        return;
      } else {
        throw new Error("bad status: " + resp.status);
      }
    }
    jobs = await resp.json();
    statusEl.textContent = "Connected to DL2 Cloud";
    statusEl.className = "status ok";
  } catch (err) {
    statusEl.textContent = "Cannot reach DL2 Cloud";
    statusEl.className = "status err";
    jobsEl.innerHTML = "";
    return;
  }

  if (!jobs || jobs.length === 0) {
    jobsEl.innerHTML = '<div class="empty">No downloads yet</div>';
    return;
  }

  jobsEl.innerHTML = jobs
    .slice()
    .reverse()
    .map((j) => {
      const pct = j.progress && j.progress.TotalBytes
        ? Math.min(100, (j.progress.DownloadedBytes / j.progress.TotalBytes) * 100)
        : 0;
      const speed = j.progress && j.progress.SpeedBytesPerS
        ? (j.progress.SpeedBytesPerS / 1e6).toFixed(2) + " MB/s"
        : "--";
      const stateLabel = j.state === "done" ? "Done" : j.state === "failed" ? "Failed" : `${pct.toFixed(0)}%`;
      const name = j.output ? j.output.split(/[\\/]/).pop() : j.url;

      return `
        <div class="job">
          <div class="job-name" title="${name}">${name}</div>
          <div class="bar-bg">
            <div class="bar-fill" style="width:${j.state === 'done' ? 100 : pct}%; background:${j.state === 'failed' ? '#f87171' : '#6366f1'}"></div>
          </div>
          <div class="job-meta">
            <span>${stateLabel}</span>
            <span>${j.state === "running" ? speed : ""}</span>
          </div>
        </div>
      `;
    })
    .join("");
}

refresh();
setInterval(refresh, 1500);

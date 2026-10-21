// State Management
let currentTorrent = null;
let activePollingInterval = null;
let expiryCountdownInterval = null;

// DOM Elements
const magnetForm = document.getElementById('magnet-form');
const magnetInput = document.getElementById('magnet-input');
const btnProcess = document.getElementById('btn-process');
const resolveSpinner = document.getElementById('resolve-spinner');
const btnText = btnProcess ? btnProcess.querySelector('.btn-text') : null;

const notificationBar = document.getElementById('notification-bar');
const notificationMsg = document.getElementById('notification-msg');
const notificationClose = document.getElementById('notification-close');

const torrentDetailsSection = document.getElementById('torrent-details-section');
const torrentNameEl = document.getElementById('torrent-name');
const torrentSizeEl = document.getElementById('torrent-size');
const torrentPeersEl = document.getElementById('torrent-peers');
const torrentHashEl = document.getElementById('torrent-hash');
const fileTableBody = document.getElementById('file-table-body');

// Progress Card Elements
const jobProgressSection = document.getElementById('job-progress-section');
const jobServiceName = document.getElementById('job-service-name');
const jobPercent = document.getElementById('job-percent');
const jobProgressBar = document.getElementById('job-progress-bar');
const jobDetail = document.getElementById('job-detail');
const jobBytes = document.getElementById('job-bytes');
const jobResultBox = document.getElementById('job-result-box');
const jobResultTitle = document.getElementById('job-result-title');
const jobResultDesc = document.getElementById('job-result-desc');
const jobDownloadLink = document.getElementById('job-download-link');
const jobCopyUrl = document.getElementById('job-copy-url');
const expiryCountdownText = document.getElementById('expiry-countdown-text');
const jobQueueBox = document.getElementById('job-queue-box');
const jobQueueTitle = document.getElementById('job-queue-title');
const jobQueueDesc = document.getElementById('job-queue-desc');

// Player Modal Elements
const playerModal = document.getElementById('player-modal');
const btnClosePlayer = document.getElementById('btn-close-player');
const playerTitle = document.getElementById('player-title');
const html5Player = document.getElementById('html5-player');
const playerDownloadBtn = document.getElementById('player-download-btn');
const playerCopyLink = document.getElementById('player-copy-link');

// Initialization
document.addEventListener('DOMContentLoaded', () => {
  checkStorageStatus();
  fetchUserQuota();
  setupEventListeners();
});

async function fetchUserQuota() {
  try {
    const res = await fetch('/api/user/quota');
    const data = await res.json();
    const quotaBadge = document.getElementById('user-quota-badge');
    const quotaText = document.getElementById('quota-text');
    if (data.success && quotaText && quotaBadge) {
      if (data.remaining <= 0) {
        quotaBadge.classList.add('exhausted');
        quotaText.textContent = `0/${data.limit} Free Used`;
      } else {
        quotaBadge.classList.remove('exhausted');
        quotaText.textContent = `${data.remaining}/${data.limit} Free Left`;
      }
    }
  } catch (e) {
    console.warn('Quota check error:', e.message);
  }
}

async function checkStorageStatus() {
  try {
    const res = await fetch('/api/storage/status');
    const data = await res.json();
    const runtimeText = document.getElementById('status-runtime-text');

    if (data.success && runtimeText) {
      if (data.isConfigured) {
        runtimeText.textContent = 'Cloud Engine Active';
      } else {
        runtimeText.textContent = 'Cloud Engine Standby';
      }
    }
  } catch (err) {
    console.warn('Storage status check skipped:', err.message);
  }
}

function setupEventListeners() {
  // Magnet Form Submit
  if (magnetForm) {
    magnetForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const magnet = magnetInput.value.trim();
      if (!magnet) return;
      await resolveMagnet(magnet);
    });
  }

  // Quick Sample Magnet Buttons
  document.querySelectorAll('.chip-tag').forEach(tag => {
    tag.addEventListener('click', () => {
      magnetInput.value = tag.getAttribute('data-magnet');
      magnetForm.dispatchEvent(new Event('submit'));
    });
  });

  // Player Modal
  if (btnClosePlayer) btnClosePlayer.addEventListener('click', closePlayer);
  if (playerModal) {
    playerModal.addEventListener('click', (e) => {
      if (e.target === playerModal) closePlayer();
    });
  }

  if (playerCopyLink) {
    playerCopyLink.addEventListener('click', () => {
      const src = html5Player ? html5Player.src : '';
      if (src) {
        navigator.clipboard.writeText(src);
        showNotification('Stream link copied to clipboard.', 'success');
      }
    });
  }

  // Notification Dismiss
  if (notificationClose) {
    notificationClose.addEventListener('click', () => notificationBar.classList.add('hidden'));
  }
}

function showNotification(msg, type = 'error') {
  if (!notificationBar || !notificationMsg) return;
  notificationMsg.textContent = msg;
  notificationBar.className = `notice-ribbon ${type}`;
  notificationBar.classList.remove('hidden');
  setTimeout(() => {
    notificationBar.classList.add('hidden');
  }, 6000);
}

function setResolvingState(isResolving) {
  if (!btnProcess) return;
  const btnArrow = btnProcess.querySelector('.btn-arrow');
  if (isResolving) {
    btnProcess.disabled = true;
    if (resolveSpinner) resolveSpinner.classList.remove('hidden');
    if (btnArrow) btnArrow.classList.add('hidden');
    if (btnText) btnText.textContent = 'Inspecting...';
  } else {
    btnProcess.disabled = false;
    if (resolveSpinner) resolveSpinner.classList.add('hidden');
    if (btnArrow) btnArrow.classList.remove('hidden');
    if (btnText) btnText.textContent = 'Inspect';
  }
}

async function resolveMagnet(magnet) {
  setResolvingState(true);
  if (notificationBar) notificationBar.classList.add('hidden');

  try {
    const res = await fetch('/api/torrent/resolve', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ magnet })
    });

    const data = await res.json();

    if (!res.ok || !data.success) {
      throw new Error(data.error || 'Failed to inspect magnet. Please ensure the link is valid and has active seeds.');
    }

    currentTorrent = data.torrent;
    renderTorrentDetails(data.torrent);
  } catch (err) {
    showNotification(err.message, 'error');
  } finally {
    setResolvingState(false);
  }
}

function renderTorrentDetails(torrent) {
  if (torrentNameEl) torrentNameEl.textContent = torrent.name || 'Unnamed Download';
  if (torrentSizeEl) torrentSizeEl.textContent = torrent.formattedSize || '0 B';
  if (torrentPeersEl) torrentPeersEl.textContent = `${torrent.numPeers || 0} Seeds/Peers`;
  if (torrentHashEl) torrentHashEl.textContent = (torrent.infoHash || '').substring(0, 10).toUpperCase() + '...';

  if (!fileTableBody) return;
  fileTableBody.innerHTML = '';

  torrent.files.forEach((file) => {
    const tr = document.createElement('tr');
    const ext = (file.name.split('.').pop() || 'bin').toLowerCase();
    const isVideo = file.isVideo || ['mkv', 'mp4', 'avi', 'mov', 'webm', 'ts', 'flv'].includes(ext);
    const isAudio = file.isAudio || ['mp3', 'wav', 'flac', 'aac', 'ogg', 'opus'].includes(ext);

    if (file.isDisallowed) {
      // Security Guard: Blocked script or batch file
      tr.className = 'row-disallowed';
      tr.innerHTML = `
        <td>
          <div class="file-cell">
            <span class="file-badge-type badge-restricted-type">${ext.toUpperCase()}</span>
            <div class="file-title-wrap">
              <span class="file-title text-dimmed" title="${escapeHtml(file.name)}">${escapeHtml(file.name)}</span>
              <span class="disallow-reason">${escapeHtml(file.disallowedMsg || 'Script file restricted')}</span>
            </div>
          </div>
        </td>
        <td class="size-cell">${file.formattedSize}</td>
        <td>
          <span class="status-pill pill-blocked">Restricted</span>
        </td>
        <td>
          <button class="btn-restricted" disabled title="Script execution files (.bat, .cmd, .sh, etc.) are restricted for security reasons">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect>
              <path d="M7 11V7a5 5 0 0 1 10 0v4"></path>
            </svg>
            <span>Blocked Script</span>
          </button>
        </td>
      `;
    } else if (file.isTooLarge) {
      // 5 GB Limit Enforced
      tr.className = 'row-toolarge';
      tr.innerHTML = `
        <td>
          <div class="file-cell">
            <span class="file-badge-type" style="background: rgba(245, 158, 11, 0.15); color: #f59e0b; border: 1px solid rgba(245, 158, 11, 0.3);">${ext.toUpperCase()}</span>
            <div class="file-title-wrap">
              <span class="file-title text-dimmed" title="${escapeHtml(file.name)}">${escapeHtml(file.name)}</span>
              <span class="disallow-reason" style="color: #f59e0b;">${escapeHtml(file.tooLargeMsg || 'Exceeds 5 GB max limit')}</span>
            </div>
          </div>
        </td>
        <td class="size-cell">${file.formattedSize}</td>
        <td>
          <span class="status-pill pill-blocked" style="border-color: rgba(245, 158, 11, 0.4); background: rgba(245, 158, 11, 0.1); color: #f59e0b;">Exceeds 5GB</span>
        </td>
        <td>
          <button class="btn-restricted" disabled title="Files exceeding 5 GB cannot be processed for direct cloud download">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="10"></circle>
              <line x1="12" y1="8" x2="12" y2="12"></line>
              <line x1="12" y1="16" x2="12.01" y2="16"></line>
            </svg>
            <span>Max 5 GB Cap</span>
          </button>
        </td>
      `;
    } else {
      // Allowed file: Executables (.exe), archives, media, documents, etc.
      tr.innerHTML = `
        <td>
          <div class="file-cell">
            <span class="file-badge-type">${ext.toUpperCase()}</span>
            <span class="file-title" title="${escapeHtml(file.name)}">${escapeHtml(file.name)}</span>
          </div>
        </td>
        <td class="size-cell">${file.formattedSize}</td>
        <td>
          <span class="status-pill pill-ready">Available</span>
        </td>
        <td>
          <div class="btn-group-tight">
            <button class="btn-solid btn-sm btn-cloud-action" onclick="prepareCloudDownload('${torrent.infoHash}', ${file.index}, '${escapeHtml(file.name)}')">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path>
                <polyline points="7 10 12 15 17 10"></polyline>
                <line x1="12" y1="15" x2="12" y2="3"></line>
              </svg>
              <span>Prepare Download</span>
            </button>
            ${(isVideo || isAudio) && file.streamUrl ? `
              <button class="btn-ghost btn-sm" onclick="openPlayer('${torrent.infoHash}', ${file.index}, '${escapeHtml(file.name)}')">
                Preview
              </button>
            ` : ''}
          </div>
        </td>
      `;
    }

    fileTableBody.appendChild(tr);
  });

  if (torrentDetailsSection) {
    torrentDetailsSection.classList.remove('hidden');
    torrentDetailsSection.scrollIntoView({ behavior: 'smooth' });
  }
}

window.prepareCloudDownload = async function(infoHash, fileIndex, filename) {
  if (jobProgressSection) jobProgressSection.classList.remove('hidden');
  if (jobResultBox) jobResultBox.classList.add('hidden');
  if (jobQueueBox) jobQueueBox.classList.add('hidden');
  if (jobServiceName) jobServiceName.textContent = `Preparing "${filename}" on Cloud Storage...`;
  if (jobPercent) jobPercent.textContent = '0%';
  if (jobProgressBar) {
    jobProgressBar.style.width = '0%';
    jobProgressBar.style.background = 'var(--text-pure)';
  }
  if (jobDetail) jobDetail.textContent = 'Connecting to cloud storage pipeline...';
  if (jobBytes) jobBytes.textContent = '';
  if (jobProgressSection) jobProgressSection.scrollIntoView({ behavior: 'smooth' });

  try {
    const res = await fetch('/api/storage/prepare', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ infoHash, fileIndex })
    });

    const data = await res.json();

    if (!res.ok || !data.success) {
      if (data.quotaExceeded) {
        fetchUserQuota();
        throw new Error(data.error || 'Free limit reached: You have used your 5 free cloud downloads.');
      }
      throw new Error(data.error || 'Failed to initiate cloud download.');
    }

    // Instant cache hit: already stored and valid within 6-hour retention window!
    if (data.cached && data.result) {
      if (jobPercent) jobPercent.textContent = 'Ready';
      if (jobProgressBar) {
        jobProgressBar.style.width = '100%';
        jobProgressBar.style.background = 'var(--accent-emerald)';
      }
      if (jobDetail) jobDetail.textContent = 'Retrieved immediately from cloud storage.';
      handleStorageCompletion(data.result);
      fetchUserQuota();
      return;
    }

    // Ongoing upload: poll job progress
    if (data.jobId) {
      pollJobProgress(data.jobId);
    }
  } catch (err) {
    if (jobDetail) jobDetail.textContent = `Error: ${err.message}`;
    if (jobPercent) jobPercent.textContent = 'Failed';
    if (jobProgressBar) jobProgressBar.style.background = 'var(--accent-rose)';
    showNotification(err.message, 'error');
  }
};

function pollJobProgress(jobId) {
  if (activePollingInterval) clearInterval(activePollingInterval);

  activePollingInterval = setInterval(async () => {
    try {
      const res = await fetch(`/api/jobs/${jobId}`);
      const data = await res.json();
      if (!res.ok || !data.success) return;

      const job = data.job;
      if (job.status === 'queued') {
        const pos = job.queuePosition || 1;
        if (jobQueueBox) jobQueueBox.classList.remove('hidden');
        if (jobQueueTitle) jobQueueTitle.textContent = `In Transfer Queue — Position #${pos}`;
        if (jobQueueDesc) {
          jobQueueDesc.textContent = `All 4 cloud transfer workers are currently active. You are #${pos} in line. Your download will start automatically as soon as a slot opens.`;
        }
        if (jobPercent) jobPercent.textContent = `Queue #${pos}`;
        if (jobProgressBar) {
          jobProgressBar.style.width = '100%';
          jobProgressBar.style.background = 'var(--accent-amber)';
        }
        if (jobDetail) jobDetail.textContent = `Waiting for available transfer slot (Position #${pos} in queue)...`;
      } else if (job.status === 'uploading') {
        if (jobQueueBox) jobQueueBox.classList.add('hidden');
        if (jobProgressBar) jobProgressBar.style.background = 'var(--text-pure)';
        const pct = job.progress || 0;
        if (jobPercent) jobPercent.textContent = `${pct.toFixed(1)}%`;
        if (jobProgressBar) jobProgressBar.style.width = `${pct}%`;

        if (pct >= 99.8) {
          if (jobDetail) jobDetail.textContent = 'Finalizing cloud object and generating 6-hour signed link...';
          if (jobPercent) jobPercent.textContent = '100%';
        } else {
          let speedText = '';
          if (job.speedBytesPerSec > 0) {
            speedText = ` • ${formatBytes(job.speedBytesPerSec)}/s`;
            const remainingBytes = (job.totalBytes || 0) - (job.uploadedBytes || 0);
            if (remainingBytes > 0) {
              const secs = Math.ceil(remainingBytes / job.speedBytesPerSec);
              const mins = Math.floor(secs / 60);
              const remSecs = secs % 60;
              const eta = mins > 60 ? `${Math.floor(mins / 60)}h ${mins % 60}m` : (mins > 0 ? `${mins}m ${remSecs}s` : `${remSecs}s`);
              speedText += ` [ETA: ~${eta}]`;
            }
          }

          if (jobDetail) jobDetail.textContent = `Streaming file directly to cloud storage${speedText}`;
          if (jobBytes && job.uploadedBytes && job.totalBytes) {
            jobBytes.textContent = `${formatBytes(job.uploadedBytes)} / ${formatBytes(job.totalBytes)}`;
          }
        }
      } else if (job.status === 'completed') {
        if (jobQueueBox) jobQueueBox.classList.add('hidden');
        clearInterval(activePollingInterval);
        if (jobPercent) jobPercent.textContent = '100%';
        if (jobProgressBar) {
          jobProgressBar.style.width = '100%';
          jobProgressBar.style.background = 'var(--accent-emerald)';
        }
        if (jobDetail) jobDetail.textContent = 'File transfer complete. Your 6-hour download link is ready!';
        handleStorageCompletion(job.result);
        fetchUserQuota();
      } else if (job.status === 'failed') {
        if (jobQueueBox) jobQueueBox.classList.add('hidden');
        clearInterval(activePollingInterval);
        if (jobPercent) jobPercent.textContent = 'Failed';
        if (jobProgressBar) jobProgressBar.style.background = 'var(--accent-rose)';
        if (jobDetail) jobDetail.textContent = `Transfer failed: ${job.error || 'Unknown error'}`;
      }
    } catch (e) {
      console.warn('Poll error:', e.message);
    }
  }, 1000);
}

function handleStorageCompletion(result) {
  if (jobResultBox) jobResultBox.classList.remove('hidden');

  if (jobResultTitle) jobResultTitle.textContent = 'Your Download Link Is Ready!';
  if (jobResultDesc) {
    jobResultDesc.textContent = 'Your signed download link is active for 6 hours. This file will be automatically purged from cloud storage after 6 hours.';
  }

  const downloadUrl = result?.downloadUrl || '#';
  if (jobDownloadLink) {
    jobDownloadLink.href = downloadUrl;
    jobDownloadLink.setAttribute('download', result?.fileName || '');
  }

  if (jobCopyUrl) {
    jobCopyUrl.onclick = () => {
      navigator.clipboard.writeText(downloadUrl);
      showNotification('Signed download URL copied to clipboard.', 'success');
    };
  }

  // Start 6-Hour Countdown
  startExpiryCountdown(result?.expiresInSeconds || 21600);
}

function startExpiryCountdown(secondsRemaining) {
  if (expiryCountdownInterval) clearInterval(expiryCountdownInterval);

  let current = secondsRemaining;

  const updateText = () => {
    if (current <= 0) {
      if (expiryCountdownText) expiryCountdownText.textContent = 'Expired (Auto-purged)';
      clearInterval(expiryCountdownInterval);
      return;
    }

    const hours = Math.floor(current / 3600);
    const mins = Math.floor((current % 3600) / 60);
    const secs = current % 60;
    const pad = (n) => String(n).padStart(2, '0');

    if (expiryCountdownText) {
      expiryCountdownText.textContent = `Expires in: ${pad(hours)}h ${pad(mins)}m ${pad(secs)}s`;
    }
    current--;
  };

  updateText();
  expiryCountdownInterval = setInterval(updateText, 1000);
}

window.openPlayer = function(infoHash, fileIndex, filename) {
  const streamUrl = `/api/stream/${infoHash}/${fileIndex}`;
  if (playerTitle) playerTitle.textContent = filename;
  if (html5Player) {
    html5Player.src = streamUrl;
    html5Player.play().catch(() => {});
  }
  if (playerDownloadBtn) playerDownloadBtn.href = `${streamUrl}?download=1`;
  if (playerModal) playerModal.classList.remove('hidden');
};

function closePlayer() {
  if (html5Player) {
    html5Player.pause();
    html5Player.src = '';
  }
  if (playerModal) playerModal.classList.add('hidden');
}

function formatBytes(bytes) {
  if (!bytes) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/[&<>"']/g, (m) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }[m]));
}

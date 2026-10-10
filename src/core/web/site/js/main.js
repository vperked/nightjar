
let isDarkMode = true;

function initTheme() {
    const savedTheme = localStorage.getItem('theme');
    if (savedTheme) {
        isDarkMode = savedTheme === 'dark';
    } else {
        isDarkMode = window.matchMedia('(prefers-color-scheme: dark)').matches;
    }
    applyTheme();
}

function toggleTheme() {
    isDarkMode = !isDarkMode;
    localStorage.setItem('theme', isDarkMode ? 'dark' : 'light');
    applyTheme();
}

function applyTheme() {
    document.documentElement.setAttribute('data-theme', isDarkMode ? 'dark' : 'light');
    const moonIcon = document.getElementById('moonIcon');
    const sunIcon = document.getElementById('sunIcon');
    if (moonIcon && sunIcon) {
        if (isDarkMode) {
            moonIcon.classList.remove('hidden');
            sunIcon.classList.add('hidden');
        } else {
            moonIcon.classList.add('hidden');
            sunIcon.classList.remove('hidden');
        }
    }
}

function showToast(message, type = 'info', duration = 3000) {
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    toast.setAttribute('role', 'alert');
    toast.setAttribute('aria-live', 'polite');
    toast.textContent = message;
    
    document.body.appendChild(toast);
    
    setTimeout(() => toast.classList.add('show'), 10);
    
    setTimeout(() => {
        toast.classList.remove('show');
        setTimeout(() => toast.remove(), 300);
    }, duration);
}

function updateBarColor(element, percent, type = 'default') {
    const baseClasses = 'progress-fill';
    let gradientClass = '';
    let warningClass = '';
    
    const gradients = {
        'cpu': 'bg-gradient-to-r from-red-500 via-red-600 to-red-700',
        'memory': 'bg-gradient-to-r from-red-500 via-red-600 to-red-700',
        'disk': 'bg-gradient-to-r from-red-500 via-red-600 to-red-700',
        'gpu': 'bg-gradient-to-r from-red-500 via-red-600 to-red-700',
        'default': 'bg-gradient-to-r from-red-500 via-red-600 to-red-700'
    };
    
    gradientClass = gradients[type] || gradients['default'];
    
    if (percent >= 90) {
        warningClass = 'progress-critical';
        element.style.boxShadow = '0 0 20px rgba(239, 68, 68, 0.6), 0 0 40px rgba(239, 68, 68, 0.3)';
    } else if (percent >= 80) {
        warningClass = 'progress-warning';
        element.style.boxShadow = '0 0 16px rgba(251, 191, 36, 0.5), 0 0 32px rgba(251, 191, 36, 0.2)';
    } else {
        element.style.boxShadow = '';
    }
    
    element.className = `${baseClasses} ${gradientClass} ${warningClass}`;
}

function updateThresholdIndicator(cardId, percent, thresholds = { warning: 80, critical: 90 }) {
    const card = document.getElementById(cardId);
    if (!card) return;
    
    card.classList.remove('threshold-warning', 'threshold-critical');
    
    if (percent >= thresholds.critical) {
        card.classList.add('threshold-critical');
    } else if (percent >= thresholds.warning) {
        card.classList.add('threshold-warning');
    }
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
}

function formatBytesPerSecond(bytes) {
    return formatBytes(bytes) + '/s';
}

function fetchWithTimeout(url, timeout = 15000) {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), timeout);
    
    return fetch(url, { 
        signal: controller.signal,
        cache: 'no-cache',
        headers: {
            'Cache-Control': 'no-cache'
        }
    })
        .finally(() => clearTimeout(timeoutId));
}

let refreshInterval = null;
let isRefreshing = false;
let retryCount = 0;
const MAX_RETRY_DELAY = 30000;

async function fetchStats(showLoading = false) {
    if (isRefreshing && !showLoading) {
        return;
    }
    
    const loading = document.getElementById('loading');
    const content = document.getElementById('content');
    const error = document.getElementById('error');
    const errorMessage = document.getElementById('errorMessage');
    const refreshBtn = document.getElementById('refreshBtn');
    const refreshIcon = document.getElementById('refreshIcon');
    
    if (refreshBtn) {
        refreshBtn.disabled = true;
        refreshBtn.style.opacity = '0.6';
        refreshBtn.style.cursor = 'not-allowed';
    }

    if (showLoading || !content.classList.contains('hidden')) {
        if (refreshIcon) {
            refreshIcon.classList.add('spinning');
        }
        isRefreshing = true;
    }
    
    const hasContent = !content.classList.contains('hidden');
    if (!hasContent) {
        loading.classList.remove('hidden');
    }
    error.classList.add('hidden');

    try {
        const response = await fetchWithTimeout('/api/stats', 15000);
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }
        const data = await response.json();

        document.getElementById('hostname').textContent = data.system?.hostname || 'N/A';
        document.getElementById('platform').textContent = `${data.system?.platform || 'N/A'} ${data.system?.platform_release || ''}`.trim();
        document.getElementById('uptime').textContent = data.uptime?.formatted || 'N/A';

        const cpuPercent = data.cpu?.cpu_percent || 0;
        document.getElementById('cpuPercent').textContent = cpuPercent.toFixed(1) + '%';
        document.getElementById('cpuBar').style.width = cpuPercent + '%';
        updateBarColor(document.getElementById('cpuBar'), cpuPercent, 'cpu');
        updateThresholdIndicator('cpuCard', cpuPercent);
        document.getElementById('cpuCores').textContent = `${data.cpu?.cpu_count_logical || 0} logical, ${data.cpu?.cpu_count_physical || 0} physical`;

        const memoryPercent = data.memory?.memory_percent || 0;
        const memoryInfo = data.memory?.virtual_memory || {};
        document.getElementById('memoryPercent').textContent = memoryPercent.toFixed(1) + '%';
        document.getElementById('memoryBar').style.width = memoryPercent + '%';
        updateBarColor(document.getElementById('memoryBar'), memoryPercent, 'memory');
        updateThresholdIndicator('memoryCard', memoryPercent);
        document.getElementById('memoryUsed').textContent = formatBytes(memoryInfo.used || 0);
        document.getElementById('memoryTotal').textContent = formatBytes(memoryInfo.total || 0);

        const diskInfo = data.disk?.total || {};
        const diskPercent = diskInfo.percent || 0;
        document.getElementById('diskPercent').textContent = diskPercent.toFixed(1) + '%';
        document.getElementById('diskBar').style.width = diskPercent + '%';
        updateBarColor(document.getElementById('diskBar'), diskPercent, 'disk');
        updateThresholdIndicator('diskCard', diskPercent);
        document.getElementById('diskUsed').textContent = formatBytes(diskInfo.used || 0);
        document.getElementById('diskTotal').textContent = formatBytes(diskInfo.total || 0);

        if (data.gpu && data.gpu.utilization) {
            const gpuPercent = data.gpu.utilization.gpu || 0;
            document.getElementById('gpuCard').classList.remove('hidden');
            document.getElementById('gpuName').textContent = data.gpu.info?.name || 'N/A';
            document.getElementById('gpuPercent').textContent = gpuPercent.toFixed(1) + '%';
            document.getElementById('gpuBar').style.width = gpuPercent + '%';
            updateBarColor(document.getElementById('gpuBar'), gpuPercent, 'gpu');
            updateThresholdIndicator('gpuCard', gpuPercent);
            const gpuMem = data.gpu.memory || {};
            document.getElementById('gpuMemory').textContent = `${formatBytes((gpuMem.used || 0) * 1024 * 1024)} / ${formatBytes((gpuMem.total || 0) * 1024 * 1024)}`;
        } else {
            document.getElementById('gpuCard').classList.add('hidden');
        }

        if (data.temperature && data.temperature > 0) {
            document.getElementById('tempCard').classList.remove('hidden');
            document.getElementById('cpuTemp').textContent = data.temperature.toFixed(1) + '°C';
        } else {
            document.getElementById('tempCard').classList.add('hidden');
        }

        const networkInfo = data.network || {};
        document.getElementById('networkUpload').textContent = formatBytesPerSecond(networkInfo.bytes_sent_per_sec || 0);
        document.getElementById('networkDownload').textContent = formatBytesPerSecond(networkInfo.bytes_recv_per_sec || 0);
        
        updateNetworkDetails(networkInfo);

        document.getElementById('processCount').textContent = data.processes?.total || 0;
        
        updateTopProcesses(data.top_processes_cpu || [], data.top_processes_memory || []);

        const now = new Date();
        const timeStr = now.toLocaleTimeString('en-US', { 
            hour: '2-digit', 
            minute: '2-digit', 
            second: '2-digit',
            hour12: false
        });
        document.getElementById('lastUpdate').textContent = timeStr;

        loading.classList.add('hidden');
        content.classList.remove('hidden');
        error.classList.add('hidden');
        
        updateCharts(data);
    
        retryCount = 0;
        
    } catch (err) {
        loading.classList.add('hidden');
        
        let errorMsg = 'Error fetching stats: ';
        if (err.name === 'AbortError') {
            errorMsg += 'Request timeout. ';
            console.error('Request timeout: Stats fetch took longer than 15 seconds');
        } else if (err.message.includes('NetworkError') || err.message.includes('Failed to fetch')) {
            errorMsg += 'Network error. ';
            console.error('Network error:', err);
        } else {
            errorMsg += err.message + '. ';
            console.error('Error fetching stats:', err);
        }
        
        showToast(errorMsg, 'error', 5000);

        retryCount++;
        const baseDelay = 2000;
        const retryDelay = Math.min(baseDelay * Math.pow(2, Math.min(retryCount - 1, 5)), MAX_RETRY_DELAY);
        
        if (content.classList.contains('hidden')) {
            error.classList.remove('hidden');
            errorMessage.textContent = `${errorMsg}Retrying in ${Math.round(retryDelay / 1000)}s...`;
            setTimeout(() => {
                error.classList.add('hidden');
                fetchStats();
            }, retryDelay);
        } else {
            setTimeout(() => {
                fetchStats();
            }, retryDelay);
        }
    } finally {
        isRefreshing = false;
        if (refreshIcon) {
            refreshIcon.classList.remove('spinning');
        }
        if (refreshBtn) {
            refreshBtn.disabled = false;
            refreshBtn.style.opacity = '1';
            refreshBtn.style.cursor = 'pointer';
        }
    }
}

function updateNetworkDetails(networkInfo) {
    const detailsContainer = document.getElementById('networkDetails');
    if (!detailsContainer) return;
    
    const interfaceName = networkInfo.interface_name || 'N/A';
    const interfaceEl = document.getElementById('networkInterface');
    if (interfaceEl) {
        interfaceEl.textContent = interfaceName;
    }
    
    const statusEl = document.getElementById('networkStatus');
    if (statusEl) {
        statusEl.textContent = networkInfo.bytes_sent_per_sec > 0 || networkInfo.bytes_recv_per_sec > 0 ? 'Active' : 'Idle';
    }
}

function updateTopProcesses(topCpu, topMemory) {
    const cpuList = document.getElementById('topProcessesCpu');
    const memoryList = document.getElementById('topProcessesMemory');
    
    if (cpuList) {
        cpuList.innerHTML = '';
        if (topCpu && topCpu.length > 0) {
            topCpu.forEach((proc, index) => {
                const item = createProcessItem(proc, 'cpu', index + 1);
                cpuList.appendChild(item);
            });
        } else {
            cpuList.innerHTML = '<div class="text-gray-500 text-sm py-2">No data available</div>';
        }
    }
    
    if (memoryList) {
        memoryList.innerHTML = '';
        if (topMemory && topMemory.length > 0) {
            topMemory.forEach((proc, index) => {
                const item = createProcessItem(proc, 'memory', index + 1);
                memoryList.appendChild(item);
            });
        } else {
            memoryList.innerHTML = '<div class="text-gray-500 text-sm py-2">No data available</div>';
        }
    }
}

function createProcessItem(proc, type, rank) {
    const item = document.createElement('div');
    item.className = 'process-item';
    item.setAttribute('role', 'listitem');
    
    const percent = type === 'cpu' ? (proc.cpu_percent || 0) : (proc.memory_percent || 0);
    const name = proc.name || 'Unknown';
    const pid = proc.pid || 'N/A';
    
    item.innerHTML = `
        <div class="flex items-center justify-between w-full">
            <div class="flex items-center gap-3 flex-1 min-w-0">
                <span class="text-gray-400 font-mono text-xs">#${Number(rank)}</span>
                <div class="flex-1 min-w-0">
                    <div class="font-semibold text-sm truncate" data-field="name"></div>
                    <div class="text-xs text-gray-500 font-mono" data-field="pid"></div>
                </div>
            </div>
            <div class="text-right">
                <div class="font-bold text-sm">${Number(percent).toFixed(1)}%</div>
                <div class="text-xs text-gray-500">${type === 'cpu' ? 'CPU' : 'Mem'}</div>
            </div>
        </div>
    `;

    // Process names are attacker-controlled (any local user can name a process), so
    // they are only ever written as text, never parsed as HTML.
    const nameEl = item.querySelector('[data-field="name"]');
    nameEl.textContent = String(name);
    nameEl.title = String(name);
    item.querySelector('[data-field="pid"]').textContent = `PID: ${pid}`;

    return item;
}

const chartData = {
    cpu: [],
    memory: [],
    disk: [],
    maxPoints: 50
};

function updateCharts(data) {
    const now = Date.now();
    
    chartData.cpu.push({ time: now, value: data.cpu?.cpu_percent || 0 });
    chartData.memory.push({ time: now, value: data.memory?.memory_percent || 0 });
    chartData.disk.push({ time: now, value: data.disk?.total?.percent || 0 });
    
    Object.keys(chartData).forEach(key => {
        if (key !== 'maxPoints' && chartData[key].length > chartData.maxPoints) {
            chartData[key].shift();
        }
    });
    
    updateMiniChart('cpuChart', chartData.cpu);
    updateMiniChart('memoryChart', chartData.memory);
    updateMiniChart('diskChart', chartData.disk);
}

function updateMiniChart(canvasId, data) {
    const canvas = document.getElementById(canvasId);
    if (!canvas || !data || data.length < 2) return;
    
    const ctx = canvas.getContext('2d');
    const width = canvas.width;
    const height = canvas.height;
    
    ctx.clearRect(0, 0, width, height);

    let chartColor = '#ef4444'; // red
    if (canvasId === 'cpuChart') chartColor = '#ef4444'; // red
    else if (canvasId === 'memoryChart') chartColor = '#ef4444'; // red
    else if (canvasId === 'diskChart') chartColor = '#ef4444'; // red
    
    ctx.strokeStyle = chartColor;
    ctx.lineWidth = 2;
    ctx.beginPath();
    
    const maxValue = 100;
    const stepX = width / (data.length - 1);
    
    data.forEach((point, index) => {
        const x = index * stepX;
        const y = height - (point.value / maxValue) * height;
        
        if (index === 0) {
            ctx.moveTo(x, y);
        } else {
            ctx.lineTo(x, y);
        }
    });
    
    ctx.stroke();
}

function manualRefresh() {
    isRefreshing = false;
    retryCount = 0;
    fetchStats(true);
}

function initializeApp() {
    initTheme();
    fetchStats();
    refreshInterval = setInterval(fetchStats, 5000);
    
    window.manualRefresh = manualRefresh;
    window.toggleTheme = toggleTheme;
}

if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initializeApp);
} else {
    initializeApp();
}

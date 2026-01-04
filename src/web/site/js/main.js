function updateBarColor(element, percent, type = 'default') {
    const baseClasses = 'progress-fill';
    
    if (percent >= 90) {
        element.className = `${baseClasses} bg-gradient-to-r from-red-600 via-red-700 to-red-800`;
    } else if (percent >= 75) {
        element.className = `${baseClasses} bg-gradient-to-r from-orange-500 via-red-500 to-red-600`;
    } else {
        if (type === 'cpu') {
            element.className = `${baseClasses} bg-gradient-to-r from-red-500 via-rose-500 to-pink-500`;
        } else if (type === 'memory') {
            element.className = `${baseClasses} bg-gradient-to-r from-red-600 via-red-500 to-rose-500`;
        } else if (type === 'disk') {
            element.className = `${baseClasses} bg-gradient-to-r from-rose-500 via-pink-500 to-red-400`;
        } else if (type === 'gpu') {
            element.className = `${baseClasses} bg-gradient-to-r from-orange-500 via-red-500 to-rose-600`;
        } else {
            element.className = `${baseClasses} bg-gradient-to-r from-red-500 via-rose-500 to-pink-500`;
        }
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

async function fetchStats() {
    const loading = document.getElementById('loading');
    const content = document.getElementById('content');
    const error = document.getElementById('error');
    const errorMessage = document.getElementById('errorMessage');

    // Only show loading if we don't have content yet
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
        document.getElementById('cpuCores').textContent = `${data.cpu?.cpu_count_logical || 0} logical, ${data.cpu?.cpu_count_physical || 0} physical`;

        const memoryPercent = data.memory?.memory_percent || 0;
        const memoryInfo = data.memory?.virtual_memory || {};
        document.getElementById('memoryPercent').textContent = memoryPercent.toFixed(1) + '%';
        document.getElementById('memoryBar').style.width = memoryPercent + '%';
        updateBarColor(document.getElementById('memoryBar'), memoryPercent, 'memory');
        document.getElementById('memoryUsed').textContent = formatBytes(memoryInfo.used || 0);
        document.getElementById('memoryTotal').textContent = formatBytes(memoryInfo.total || 0);

        const diskInfo = data.disk?.total || {};
        const diskPercent = diskInfo.percent || 0;
        document.getElementById('diskPercent').textContent = diskPercent.toFixed(1) + '%';
        document.getElementById('diskBar').style.width = diskPercent + '%';
        updateBarColor(document.getElementById('diskBar'), diskPercent, 'disk');
        document.getElementById('diskUsed').textContent = formatBytes(diskInfo.used || 0);
        document.getElementById('diskTotal').textContent = formatBytes(diskInfo.total || 0);

        if (data.gpu && data.gpu.utilization) {
            const gpuPercent = data.gpu.utilization.gpu || 0;
            document.getElementById('gpuCard').classList.remove('hidden');
            document.getElementById('gpuName').textContent = data.gpu.info?.name || 'N/A';
            document.getElementById('gpuPercent').textContent = gpuPercent.toFixed(1) + '%';
            document.getElementById('gpuBar').style.width = gpuPercent + '%';
            updateBarColor(document.getElementById('gpuBar'), gpuPercent, 'gpu');
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

        document.getElementById('processCount').textContent = data.processes?.total || 0;

        const topCpuDiv = document.getElementById('topCpuProcesses');
        const topMemoryDiv = document.getElementById('topMemoryProcesses');
        
        if (data.top_processes_cpu && data.top_processes_cpu.length > 0) {
            topCpuDiv.innerHTML = data.top_processes_cpu.map(proc => {
                const name = proc.name || 'Unknown';
                const cpu = (proc.cpu_percent || 0).toFixed(1);
                return `<div class="process-item justify-between text-xs">
                    <span class="value font-mono truncate flex-1 mr-2">${name}</span>
                    <span class="text-red-300 font-semibold font-mono flex-shrink-0">${cpu}%</span>
                </div>`;
            }).join('');
        } else {
            topCpuDiv.innerHTML = '<div class="text-xs sm:text-sm text-gray-500">Not available</div>';
        }
        
        if (data.top_processes_memory && data.top_processes_memory.length > 0) {
            topMemoryDiv.innerHTML = data.top_processes_memory.map(proc => {
                const name = proc.name || 'Unknown';
                const mem = (proc.memory_percent || 0).toFixed(1);
                return `<div class="process-item justify-between text-xs">
                    <span class="value font-mono truncate flex-1 mr-2">${name}</span>
                    <span class="text-rose-300 font-semibold font-mono flex-shrink-0">${mem}%</span>
                </div>`;
            }).join('');
        } else {
            topMemoryDiv.innerHTML = '<div class="text-xs sm:text-sm text-gray-500">Not available</div>';
        }

        document.getElementById('lastUpdate').textContent = 'Last updated: ' + new Date().toLocaleTimeString();

        loading.classList.add('hidden');
        content.classList.remove('hidden');
        error.classList.add('hidden');
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
        errorMsg += 'Retrying...';
        errorMessage.textContent = errorMsg;
        
        // Only show error if we don't have content, otherwise silently retry
        if (content.classList.contains('hidden')) {
            error.classList.remove('hidden');
            setTimeout(() => {
                error.classList.add('hidden');
                fetchStats();
            }, 2000);
        } else {
            // If we have content, retry silently without showing error
            setTimeout(fetchStats, 2000);
        }
    }
}

// Wait for DOM to be ready before initializing
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => {
        fetchStats();
        setInterval(fetchStats, 5000);
    });
} else {
    fetchStats();
    setInterval(fetchStats, 5000);
}


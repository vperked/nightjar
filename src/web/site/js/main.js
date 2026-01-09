function updateBarColor(element, percent, type = 'default') {
    const baseClasses = 'progress-fill';
    
    if (percent >= 90) {
        element.className = `${baseClasses} bg-gradient-to-r from-gray-700 via-gray-800 to-gray-900`;
        element.style.boxShadow = '0 0 20px rgba(0, 0, 0, 0.8)';
    } else if (percent >= 80) {
        element.className = `${baseClasses} bg-gradient-to-r from-gray-600 via-gray-700 to-gray-800`;
        element.style.boxShadow = '0 0 16px rgba(0, 0, 0, 0.6)';
    } else if (percent >= 70) {
        element.className = `${baseClasses} bg-gradient-to-r from-gray-500 via-gray-600 to-gray-700`;
        element.style.boxShadow = '0 0 12px rgba(0, 0, 0, 0.5)';
    } else {
        element.className = `${baseClasses} bg-gradient-to-r from-gray-300 via-gray-400 to-gray-500`;
        element.style.boxShadow = '0 0 12px rgba(255, 255, 255, 0.3)';
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
            topCpuDiv.innerHTML = data.top_processes_cpu.map((proc, index) => {
                const name = proc.name || 'Unknown';
                const cpu = (proc.cpu_percent || 0).toFixed(1);
                const barWidth = Math.min(cpu, 100);
                const barColor = cpu >= 80 ? 'bg-gray-800' : cpu >= 60 ? 'bg-gray-600' : 'bg-gray-400';
                return `<div class="process-item flex-col items-start gap-2 text-xs" style="animation-delay: ${index * 0.05}s">
                    <div class="flex justify-between w-full items-center">
                        <span class="value font-mono truncate flex-1 mr-2 text-sm">${name}</span>
                        <span class="text-white font-bold font-mono flex-shrink-0 text-sm">${cpu}%</span>
                    </div>
                    <div class="w-full h-1.5 bg-black/30 rounded-full overflow-hidden">
                        <div class="${barColor} h-full rounded-full transition-all duration-500" style="width: ${barWidth}%"></div>
                    </div>
                </div>`;
            }).join('');
        } else {
            topCpuDiv.innerHTML = '<div class="text-xs sm:text-sm text-gray-500">Not available</div>';
        }
        
        if (data.top_processes_memory && data.top_processes_memory.length > 0) {
            topMemoryDiv.innerHTML = data.top_processes_memory.map((proc, index) => {
                const name = proc.name || 'Unknown';
                const mem = (proc.memory_percent || 0).toFixed(1);
                const barWidth = Math.min(mem * 10, 100);
                const barColor = mem >= 8 ? 'bg-gray-800' : mem >= 5 ? 'bg-gray-600' : 'bg-gray-400';
                return `<div class="process-item flex-col items-start gap-2 text-xs" style="animation-delay: ${index * 0.05}s">
                    <div class="flex justify-between w-full items-center">
                        <span class="value font-mono truncate flex-1 mr-2 text-sm">${name}</span>
                        <span class="text-white font-bold font-mono flex-shrink-0 text-sm">${mem}%</span>
                    </div>
                    <div class="w-full h-1.5 bg-black/30 rounded-full overflow-hidden">
                        <div class="${barColor} h-full rounded-full transition-all duration-500" style="width: ${barWidth}%"></div>
                    </div>
                </div>`;
            }).join('');
        } else {
            topMemoryDiv.innerHTML = '<div class="text-xs sm:text-sm text-gray-500">Not available</div>';
        }

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


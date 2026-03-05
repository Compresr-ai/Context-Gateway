/**
 * Context Gateway Plugin for OpenClaw
 *
 * Automatically routes LLM requests through Context Gateway when available.
 *
 * Strategy:
 * 1. Get ALL providers from agents.defaults.models (e.g., "anthropic", "openai")
 * 2. Create/modify models.providers.{provider}.baseUrl for ALL providers to point to gateway
 * 3. On disable, clean up what we created or restore original values
 *
 * Note: /model in chat uses session-level overrides (doesn't update openclaw.json),
 * so we must route ALL providers upfront, not just the primary.
 *
 * OpenClaw Config Rules:
 * - If models.providers.{provider} exists, baseUrl MUST be a string (not undefined)
 * - If provider entry is incomplete, remove it entirely rather than leaving invalid state
 */

import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { exec } from 'child_process';

// Track what we created/modified for cleanup (per provider)
interface OriginalProviderState {
  hadProviderEntry: boolean;
  originalProviderEntry?: any; // Full original entry to restore (deep copy)
}

interface OriginalState {
  hadModels: boolean;
  hadProviders: boolean;
  providers: Map<string, OriginalProviderState>;
}

let originalState: OriginalState | null = null;
let healthCheckTimer: ReturnType<typeof setInterval> | null = null;
let isRoutingEnabled = false;
let lastKnownPort: number | null = null;
let portFileWatcher: fs.FSWatcher | null = null;
let pendingRestoreRetry = false; // Track if we need to retry config restoration

// Faster health check interval (1s) for quicker crash detection
const HEALTH_CHECK_INTERVAL = 1000;

function getConfigPath(): string {
  return path.join(os.homedir(), '.openclaw', 'openclaw.json');
}

// Agent-level config can override global config
// Check common agent paths (main is default, but could be others)
function findAgentModelsOverrides(): string[] {
  const agentsDir = path.join(os.homedir(), '.openclaw', 'agents');
  const overrides: string[] = [];

  try {
    if (!fs.existsSync(agentsDir)) return overrides;

    const agents = fs.readdirSync(agentsDir);
    for (const agent of agents) {
      const modelsPath = path.join(agentsDir, agent, 'agent', 'models.json');
      if (fs.existsSync(modelsPath)) {
        overrides.push(modelsPath);
      }
    }
  } catch {}

  return overrides;
}

// Remove all agent-level models.json overrides that could conflict
function removeAgentModelsOverrides(): void {
  const overrides = findAgentModelsOverrides();
  for (const filepath of overrides) {
    try {
      fs.unlinkSync(filepath);
      console.log('[context-gateway] Removed override: ' + filepath);
    } catch (e) {
      console.log('[context-gateway] Could not remove: ' + filepath);
    }
  }
}

// Clean gateway URLs from agent-level overrides in-place
// (deleting triggers OpenClaw daemon to re-persist from cache, so modify instead)
function cleanAgentOverrideGatewayUrls(): void {
  for (const filepath of findAgentModelsOverrides()) {
    try {
      const content = JSON.parse(fs.readFileSync(filepath, 'utf-8'));
      if (!isPlainObject(content?.providers)) continue;
      let changed = false;
      for (const [prov, entry] of Object.entries<any>(content.providers)) {
        if (!isPlainObject(entry)) continue;
        if (typeof entry.baseUrl === 'string' && /^http:\/\/127\.0\.0\.1:\d+$/.test(entry.baseUrl)) {
          delete content.providers[prov];
          changed = true;
        }
      }
      if (changed) {
        if (Object.keys(content.providers).length === 0) {
          delete content.providers;
        }
        fs.writeFileSync(filepath, JSON.stringify(content, null, 2), 'utf-8');
        console.log('[context-gateway] Cleaned stale override: ' + filepath);
      }
    } catch {}
  }
}

function getGatewayPort(): number | null {
  try {
    const portFile = path.join(os.tmpdir(), 'context-gateway.port');
    if (fs.existsSync(portFile)) {
      const port = parseInt(fs.readFileSync(portFile, 'utf-8').trim(), 10);
      if (port > 0 && port <= 65535) return port;
    }
  } catch {}
  return null;
}

function getGatewayUrl(): string | null {
  const port = getGatewayPort();
  return port ? `http://127.0.0.1:${port}` : null;
}

// Check if gateway process is running by PID (faster than HTTP health check)
function isGatewayProcessAlive(): boolean {
  try {
    const pidFile = path.join(os.tmpdir(), 'context-gateway.pid');
    if (!fs.existsSync(pidFile)) return false;

    const pid = parseInt(fs.readFileSync(pidFile, 'utf-8').trim(), 10);
    if (isNaN(pid) || pid <= 0) return false;

    // process.kill(pid, 0) checks if process exists without sending signal
    process.kill(pid, 0);
    return true;
  } catch {
    // ESRCH = no such process, EPERM = process exists but no permission
    return false;
  }
}

async function isGatewayAvailable(): Promise<boolean> {
  // Quick PID check first (instant, no network)
  if (!isGatewayProcessAlive()) {
    return false;
  }

  // Then verify with HTTP health check
  const url = getGatewayUrl();
  if (!url) return false;
  try {
    const ctrl = new AbortController();
    const id = setTimeout(() => ctrl.abort(), 2000);
    const res = await fetch(`${url}/health`, { signal: ctrl.signal });
    clearTimeout(id);
    return res.ok;
  } catch {
    return false;
  }
}

function readConfig(): any {
  try {
    const configPath = getConfigPath();
    if (fs.existsSync(configPath)) {
      return JSON.parse(fs.readFileSync(configPath, 'utf-8'));
    }
  } catch (e) {
    console.log('[context-gateway] Error reading config:', e);
  }
  return null;
}

function writeConfig(config: any): boolean {
  try {
    const configPath = getConfigPath();
    const tempPath = configPath + '.tmp.' + process.pid;
    // Write to temp file first, then rename atomically
    fs.writeFileSync(tempPath, JSON.stringify(config, null, 2), 'utf-8');
    fs.renameSync(tempPath, configPath);
    return true;
  } catch (e) {
    console.log('[context-gateway] Error writing config:', e);
    // Clean up temp file if it exists
    try {
      const tempPath = getConfigPath() + '.tmp.' + process.pid;
      if (fs.existsSync(tempPath)) fs.unlinkSync(tempPath);
    } catch {}
    return false;
  }
}

// Get ALL unique provider names from agents.defaults.models
// e.g., { "openai/gpt-5.1-codex": {}, "anthropic/claude-sonnet-4-6": {} } → ["openai", "anthropic"]
function getAllProviders(config: any): string[] {
  const models = config?.agents?.defaults?.models;
  if (!models || typeof models !== 'object') return [];
  const providers = new Set<string>();
  for (const key of Object.keys(models)) {
    if (key.includes('/')) {
      const provider = key.split('/')[0];
      if (/^[a-zA-Z0-9_-]+$/.test(provider)) providers.add(provider);
    }
  }
  return [...providers];
}

// Safely check if value is a plain object
function isPlainObject(val: any): boolean {
  return val !== null && typeof val === 'object' && !Array.isArray(val);
}

// Enable routing by setting baseUrl for ALL providers
function enableGatewayRouting(): boolean {
  const currentPort = getGatewayPort();
  if (!currentPort) return false;

  const gatewayUrl = `http://127.0.0.1:${currentPort}`;

  // Skip if already routing to same port
  if (isRoutingEnabled && lastKnownPort === currentPort) return true;

  const config = readConfig();
  if (!config) return false;

  const providers = getAllProviders(config);
  if (providers.length === 0) {
    // No providers configured yet (e.g., during onboarding) - silently skip
    return false;
  }

  // If already routing but port changed, disable first then re-enable
  if (isRoutingEnabled && lastKnownPort !== currentPort) {
    disableGatewayRouting();
    const freshConfig = readConfig();
    if (!freshConfig) return false;
    return enableGatewayRoutingForProviders(freshConfig, getAllProviders(freshConfig), gatewayUrl, currentPort);
  }

  return enableGatewayRoutingForProviders(config, providers, gatewayUrl, currentPort);
}

// Internal helper to enable routing for all providers
function enableGatewayRoutingForProviders(config: any, providers: string[], gatewayUrl: string, port: number): boolean {
  // Save original state before first modification
  if (!originalState) {
    originalState = {
      hadModels: isPlainObject(config.models),
      hadProviders: isPlainObject(config.models?.providers),
      providers: new Map(),
    };

    for (const provider of providers) {
      const existingEntry = isPlainObject(config.models?.providers?.[provider])
        ? config.models.providers[provider]
        : null;

      const isStaleGatewayUrl = typeof existingEntry?.baseUrl === 'string' &&
        /^http:\/\/127\.0\.0\.1:\d+$/.test(existingEntry.baseUrl);

      originalState.providers.set(provider, {
        hadProviderEntry: existingEntry !== null && !isStaleGatewayUrl,
        originalProviderEntry: isStaleGatewayUrl ? undefined : (existingEntry ? JSON.parse(JSON.stringify(existingEntry)) : undefined),
      });
    }
  }

  // Create/fix structure with type safety
  if (!isPlainObject(config.models)) {
    config.models = {};
  }
  if (!isPlainObject(config.models.providers)) {
    config.models.providers = {};
  }

  // Set baseUrl for ALL providers
  for (const provider of providers) {
    const providerEntry = config.models.providers[provider];
    if (!isPlainObject(providerEntry)) {
      config.models.providers[provider] = {
        models: [],
        baseUrl: gatewayUrl,
        headers: { "X-Client-Agent": "OpenClaw" }
      };
    } else {
      if (!Array.isArray(providerEntry.models)) {
        providerEntry.models = [];
      }
      providerEntry.baseUrl = gatewayUrl;
      if (!isPlainObject(providerEntry.headers)) {
        providerEntry.headers = {};
      }
      providerEntry.headers["X-Client-Agent"] = "OpenClaw";
    }
  }

  if (writeConfig(config)) {
    // Remove any agent-level overrides that could conflict
    removeAgentModelsOverrides();

    isRoutingEnabled = true;
    lastKnownPort = port;
    console.log('[context-gateway] ✓ Routing [' + providers.join(', ') + '] via ' + gatewayUrl);
    return true;
  }

  return false;
}

// Disable routing - restore original state for ALL providers
// CRITICAL: Never leave provider entry without baseUrl (OpenClaw requires string)
// CRITICAL: Always reset state flags to prevent stuck routing
function disableGatewayRouting(): boolean {
  if (!isRoutingEnabled && !pendingRestoreRetry) return true;
  if (!originalState) {
    // No state to restore, just reset flags
    isRoutingEnabled = false;
    lastKnownPort = null;
    pendingRestoreRetry = false;
    return true;
  }

  const config = readConfig();
  if (!config) {
    console.log('[context-gateway] ✗ Failed to read config, will retry');
    pendingRestoreRetry = true;
    isRoutingEnabled = false;
    lastKnownPort = null;
    return false;
  }

  // Restore each provider to its original state
  if (!originalState.hadModels) {
    // We created entire models section - remove it
    delete config.models;
  } else if (!originalState.hadProviders) {
    // We created providers section - remove it
    if (isPlainObject(config.models)) {
      delete config.models.providers;
      if (Object.keys(config.models).length === 0) {
        delete config.models;
      }
    }
  } else {
    // Restore individual providers
    for (const [provider, provState] of originalState.providers) {
      if (!isPlainObject(config.models?.providers)) break;

      if (!provState.hadProviderEntry) {
        // We created this provider entry - remove it entirely
        delete config.models.providers[provider];
      } else if (provState.originalProviderEntry) {
        // Provider existed before - restore full original entry
        config.models.providers[provider] = provState.originalProviderEntry;
      }
    }

    // Clean up empty parents
    if (isPlainObject(config.models?.providers) && Object.keys(config.models.providers).length === 0) {
      delete config.models.providers;
      if (isPlainObject(config.models) && Object.keys(config.models).length === 0) {
        delete config.models;
      }
    }
  }

  if (writeConfig(config)) {
    cleanAgentOverrideGatewayUrls();
    isRoutingEnabled = false;
    lastKnownPort = null;
    pendingRestoreRetry = false;
    originalState = null;
    console.log('[context-gateway] ✓ Config restored successfully');
    return true;
  }

  console.log('[context-gateway] ✗ Failed to write config, will retry');
  pendingRestoreRetry = true;
  isRoutingEnabled = false;
  lastKnownPort = null;
  return false;
}

async function healthCheckLoop(): Promise<void> {
  const currentPort = getGatewayPort();
  const processAlive = isGatewayProcessAlive();
  const available = currentPort && processAlive ? await isGatewayAvailable() : false;

  // Handle pending restore retry
  if (pendingRestoreRetry && !available) {
    console.log('[context-gateway] Retrying config restoration...');
    disableGatewayRouting();
    return;
  }

  if (available && currentPort) {
    pendingRestoreRetry = false; // Clear retry flag on successful connection
    if (isRoutingEnabled && lastKnownPort !== currentPort) {
      console.log('[context-gateway] Port changed: ' + lastKnownPort + ' -> ' + currentPort);
      enableGatewayRouting();
    } else if (!isRoutingEnabled) {
      enableGatewayRouting();
    }
  } else if (isRoutingEnabled || pendingRestoreRetry) {
    console.log('[context-gateway] Gateway offline - disabling routing');
    disableGatewayRouting();
  }
}

function startHealthMonitor(): void {
  if (healthCheckTimer) return;
  healthCheckTimer = setInterval(() => {
    healthCheckLoop().catch(e => console.log('[context-gateway] Health check error:', e));
  }, HEALTH_CHECK_INTERVAL);
}

function stopHealthMonitor(): void {
  if (healthCheckTimer) {
    clearInterval(healthCheckTimer);
    healthCheckTimer = null;
  }
}

// Watch port file for instant deletion detection (faster than polling)
function startPortFileWatcher(): void {
  if (portFileWatcher) return;

  const portFile = path.join(os.tmpdir(), 'context-gateway.port');
  const portDir = os.tmpdir();

  try {
    // Watch the temp directory for changes to the port file
    portFileWatcher = fs.watch(portDir, (eventType, filename) => {
      if (filename === 'context-gateway.port') {
        if (eventType === 'rename' && !fs.existsSync(portFile)) {
          // Port file was deleted - gateway is shutting down
          console.log('[context-gateway] Port file removed - gateway shutting down');
          if (isRoutingEnabled || pendingRestoreRetry) {
            disableGatewayRouting();
          }
        } else if (eventType === 'rename' && fs.existsSync(portFile)) {
          // Port file was created - gateway starting up
          console.log('[context-gateway] Port file created - checking gateway...');
          // Trigger health check to enable routing
          healthCheckLoop().catch(() => {});
        }
      }
    });

    portFileWatcher.on('error', (err) => {
      console.log('[context-gateway] Port file watcher error:', err);
      // Try to restart watcher
      stopPortFileWatcher();
      setTimeout(startPortFileWatcher, 1000);
    });
  } catch (e) {
    console.log('[context-gateway] Could not start port file watcher:', e);
  }
}

function stopPortFileWatcher(): void {
  if (portFileWatcher) {
    try {
      portFileWatcher.close();
    } catch {}
    portFileWatcher = null;
  }
}

function cleanupStaleGatewayUrls(): void {
  // Clean global config
  const config = readConfig();
  let modified = false;
  if (config && isPlainObject(config.models?.providers)) {
    for (const [provider, entry] of Object.entries<any>(config.models.providers)) {
      if (!isPlainObject(entry)) continue;
      if (typeof entry.baseUrl === 'string' && /^http:\/\/127\.0\.0\.1:\d+$/.test(entry.baseUrl)) {
        delete entry.baseUrl;
        if (isPlainObject(entry.headers)) {
          delete entry.headers['X-Client-Agent'];
          if (Object.keys(entry.headers).length === 0) delete entry.headers;
        }
        // If entry is now empty (or only had our fields), remove it entirely
        if (Object.keys(entry).length === 0 ||
            (entry.models && Object.keys(entry).length === 1 && Array.isArray(entry.models) && entry.models.length === 0)) {
          delete config.models.providers[provider];
        }
        modified = true;
      }
    }
    // Clean up empty parents
    if (modified && isPlainObject(config.models?.providers) && Object.keys(config.models.providers).length === 0) {
      delete config.models.providers;
      if (isPlainObject(config.models) && Object.keys(config.models).length === 0) {
        delete config.models;
      }
    }
    if (modified) {
      writeConfig(config);
      console.log('[context-gateway] Cleaned up stale gateway URLs from config');
    }
  }

  // Clean agent-level overrides in-place
  cleanAgentOverrideGatewayUrls();
}

export async function activate(): Promise<void> {
  console.log('[context-gateway] Activating...');

  let available = await isGatewayAvailable();
  let port = getGatewayPort();

  // If gateway not available but port file exists, retry after short delay
  // (gateway might be starting up)
  if (!available && port) {
    console.log('[context-gateway] Gateway starting, waiting...');
    await new Promise(resolve => setTimeout(resolve, 2000));
    available = await isGatewayAvailable();
  }

  // Clean up any stale gateway URLs if gateway is not running
  if (!available) {
    cleanupStaleGatewayUrls();
  }

  if (available && port) {
    enableGatewayRouting();
  } else {
    console.log('[context-gateway] Gateway not detected');
  }

  // Start both health monitor (polling) and port file watcher (instant)
  startHealthMonitor();
  startPortFileWatcher();
  console.log('[context-gateway] Activated');
}

export async function deactivate(): Promise<void> {
  console.log('[context-gateway] Deactivating...');
  stopHealthMonitor();
  stopPortFileWatcher();
  disableGatewayRouting();

  // Reset all state
  originalState = null;
  isRoutingEnabled = false;
  lastKnownPort = null;
  pendingRestoreRetry = false;

  console.log('[context-gateway] Deactivated');
}
